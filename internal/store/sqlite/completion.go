package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/completion"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

// CompletionFacts is the persisted, re-readable input to the deterministic completion predicate.
type CompletionFacts struct {
	IntegrationTree           string
	ExpectedTree              string
	Criteria                  []completion.CriterionStatus
	OpenBlockingFindings      int
	ScopePolicyPassed         bool
	FinalValidationSetCurrent bool
	FinalEvidenceSetID        string
	FinalReportHash           string
	HumanAcceptanceRequired   bool
	HumanAcceptanceSatisfied  bool
}

// SetCompletionFacts replaces the current completion snapshot without advancing Goal state.
func (s *Store) SetCompletionFacts(
	ctx context.Context,
	goalID string,
	facts CompletionFacts,
	event EventInput,
) (int64, error) {
	if err := validateCompletionFacts(goalID, facts); err != nil {
		return 0, err
	}
	criteriaJSON, err := canonical.Marshal(facts.Criteria)
	if err != nil {
		return 0, fmt.Errorf("canonicalize completion criteria: %w", err)
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return 0, err
	}
	var version int64
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		goal, err := readGoal(ctx, tx, goalID)
		if err != nil {
			return err
		}
		if goal.State != domain.GoalVerifying {
			return fmt.Errorf("goal %q is not verifying: %w", goalID, basestore.ErrConflict)
		}
		now := s.source.Now().UTC().Format(time.RFC3339Nano)
		var currentVersion int64
		err = tx.QueryRowContext(ctx, `
SELECT version
FROM goal_completion_facts
WHERE goal_id = ?`, goalID).Scan(&currentVersion)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			version = 1
			_, err = tx.ExecContext(ctx, `
INSERT INTO goal_completion_facts(
    goal_id, integration_tree, expected_tree, criteria_json,
    open_blocking_findings, scope_policy_passed, final_validation_current,
    final_evidence_set_id, final_report_hash, human_acceptance_required,
    human_acceptance_satisfied, version, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
				goalID,
				facts.IntegrationTree,
				facts.ExpectedTree,
				criteriaJSON,
				facts.OpenBlockingFindings,
				boolInteger(facts.ScopePolicyPassed),
				boolInteger(facts.FinalValidationSetCurrent),
				facts.FinalEvidenceSetID,
				facts.FinalReportHash,
				boolInteger(facts.HumanAcceptanceRequired),
				boolInteger(facts.HumanAcceptanceSatisfied),
				now,
			)
			if err != nil {
				return fmt.Errorf("insert completion facts for goal %q: %w", goalID, err)
			}
		case err != nil:
			return fmt.Errorf("read completion facts version for goal %q: %w", goalID, err)
		default:
			version = currentVersion + 1
			updated, updateErr := tx.ExecContext(ctx, `
UPDATE goal_completion_facts
SET integration_tree = ?, expected_tree = ?, criteria_json = ?,
    open_blocking_findings = ?, scope_policy_passed = ?,
    final_validation_current = ?, final_evidence_set_id = ?,
    final_report_hash = ?, human_acceptance_required = ?,
    human_acceptance_satisfied = ?, version = version + 1, updated_at = ?
WHERE goal_id = ? AND version = ?`,
				facts.IntegrationTree,
				facts.ExpectedTree,
				criteriaJSON,
				facts.OpenBlockingFindings,
				boolInteger(facts.ScopePolicyPassed),
				boolInteger(facts.FinalValidationSetCurrent),
				facts.FinalEvidenceSetID,
				facts.FinalReportHash,
				boolInteger(facts.HumanAcceptanceRequired),
				boolInteger(facts.HumanAcceptanceSatisfied),
				now,
				goalID,
				currentVersion,
			)
			if updateErr != nil {
				return fmt.Errorf("update completion facts for goal %q: %w", goalID, updateErr)
			}
			affected, updateErr := updated.RowsAffected()
			if updateErr != nil {
				return fmt.Errorf("read completion facts update result: %w", updateErr)
			}
			if affected != 1 {
				return fmt.Errorf("completion facts for goal %q: %w", goalID, basestore.ErrConflict)
			}
		}
		return s.appendEvent(ctx, tx, "goal", goalID, prepared)
	})
	if err != nil {
		return 0, err
	}
	return version, nil
}

// CompleteGoal re-reads every durable predicate input and commits the final Goal tuple atomically.
func (s *Store) CompleteGoal(
	ctx context.Context,
	goalID string,
	expectedGoalVersion int64,
	event EventInput,
) (completion.Result, error) {
	if goalID == "" || expectedGoalVersion <= 0 {
		return completion.Result{}, errors.New("invalid goal completion request")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return completion.Result{}, err
	}
	var result completion.Result
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		goal, err := readGoal(ctx, tx, goalID)
		if err != nil {
			return err
		}
		if goal.Version != expectedGoalVersion {
			return fmt.Errorf("goal %q: %w", goalID, basestore.ErrConflict)
		}
		facts, err := readCompletionFacts(ctx, tx, goalID)
		if err != nil {
			return err
		}
		states, err := requiredWorkStates(ctx, tx, goal)
		if err != nil {
			return err
		}
		openRequiredGates, err := countBlockingRequiredGates(ctx, tx, goalID, "", s.source.Now())
		if err != nil {
			return fmt.Errorf("count open required gates for goal %q: %w", goalID, err)
		}
		result = completion.Evaluate(completion.Input{
			GoalState:                 goal.State,
			RequiredWorkStates:        states,
			IntegrationTree:           facts.IntegrationTree,
			ExpectedTree:              facts.ExpectedTree,
			Criteria:                  facts.Criteria,
			OpenBlockingFindings:      facts.OpenBlockingFindings,
			OpenRequiredGates:         openRequiredGates,
			ScopePolicyPassed:         facts.ScopePolicyPassed,
			FinalValidationSetCurrent: facts.FinalValidationSetCurrent,
			FinalReportGenerated:      facts.FinalReportHash != "",
			HumanAcceptanceRequired:   facts.HumanAcceptanceRequired,
			HumanAcceptanceSatisfied:  facts.HumanAcceptanceSatisfied,
		})
		var activeLeases int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM leases WHERE state = ?`, domain.LeaseActive).Scan(&activeLeases); err != nil {
			return fmt.Errorf("count active leases during goal completion: %w", err)
		}
		if activeLeases != 0 {
			result.Complete = false
			result.Reasons = append(result.Reasons, "active leases remain")
		}
		if !result.Complete {
			return nil
		}
		if err := domain.ValidateGoalTransition(goal.State, domain.GoalCompleted); err != nil {
			return err
		}
		updated, err := tx.ExecContext(ctx, `
UPDATE goals
SET state = ?, final_tree = ?, final_evidence_set_id = ?,
    final_report_hash = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ? AND state = ?`,
			domain.GoalCompleted,
			facts.IntegrationTree,
			facts.FinalEvidenceSetID,
			facts.FinalReportHash,
			s.source.Now().UTC().Format(time.RFC3339Nano),
			goalID,
			expectedGoalVersion,
			domain.GoalVerifying,
		)
		if err != nil {
			return fmt.Errorf("complete goal %q: %w", goalID, err)
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return fmt.Errorf("read goal completion result: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("goal %q: %w", goalID, basestore.ErrConflict)
		}
		return s.appendEvent(ctx, tx, "goal", goalID, prepared)
	})
	if err != nil {
		return completion.Result{}, err
	}
	return result, nil
}

func validateCompletionFacts(goalID string, facts CompletionFacts) error {
	if goalID == "" || strings.TrimSpace(facts.IntegrationTree) == "" || strings.TrimSpace(facts.ExpectedTree) == "" ||
		strings.TrimSpace(facts.FinalEvidenceSetID) == "" || strings.TrimSpace(facts.FinalReportHash) == "" || facts.OpenBlockingFindings < 0 || len(facts.Criteria) == 0 {
		return errors.New("invalid completion facts")
	}
	seen := make(map[string]struct{}, len(facts.Criteria))
	for _, criterion := range facts.Criteria {
		if strings.TrimSpace(criterion.ID) == "" || strings.TrimSpace(criterion.TreeHash) == "" {
			return errors.New("completion criterion id and tree hash are required")
		}
		if _, exists := seen[criterion.ID]; exists {
			return fmt.Errorf("duplicate completion criterion %q", criterion.ID)
		}
		seen[criterion.ID] = struct{}{}
	}
	return nil
}

func readCompletionFacts(ctx context.Context, queryer rowQueryer, goalID string) (CompletionFacts, error) {
	var facts CompletionFacts
	var criteriaJSON []byte
	var scopePassed, validationCurrent, acceptanceRequired, acceptanceSatisfied int
	err := queryer.QueryRowContext(ctx, `
SELECT integration_tree, expected_tree, criteria_json, open_blocking_findings,
       scope_policy_passed, final_validation_current, final_evidence_set_id,
       final_report_hash, human_acceptance_required, human_acceptance_satisfied
FROM goal_completion_facts
WHERE goal_id = ?`, goalID).Scan(
		&facts.IntegrationTree,
		&facts.ExpectedTree,
		&criteriaJSON,
		&facts.OpenBlockingFindings,
		&scopePassed,
		&validationCurrent,
		&facts.FinalEvidenceSetID,
		&facts.FinalReportHash,
		&acceptanceRequired,
		&acceptanceSatisfied,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CompletionFacts{}, fmt.Errorf("completion facts for goal %q: %w", goalID, basestore.ErrNotFound)
	}
	if err != nil {
		return CompletionFacts{}, fmt.Errorf("read completion facts for goal %q: %w", goalID, err)
	}
	if err := json.Unmarshal(criteriaJSON, &facts.Criteria); err != nil {
		return CompletionFacts{}, fmt.Errorf("decode completion criteria for goal %q: %w", goalID, err)
	}
	facts.ScopePolicyPassed = scopePassed == 1
	facts.FinalValidationSetCurrent = validationCurrent == 1
	facts.HumanAcceptanceRequired = acceptanceRequired == 1
	facts.HumanAcceptanceSatisfied = acceptanceSatisfied == 1
	return facts, nil
}

func requiredWorkStates(ctx context.Context, tx *sql.Tx, goal domain.Goal) ([]domain.WorkState, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT work.state
FROM work_items work
JOIN plan_revisions plan ON plan.id = work.plan_revision_id
WHERE plan.goal_revision_id = ?
  AND plan.status = ?
  AND work.required = 1
ORDER BY work.id`, goal.ActiveRevisionID, domain.PlanActive)
	if err != nil {
		return nil, fmt.Errorf("read required work states for goal %q: %w", goal.ID, err)
	}
	defer rows.Close()
	var states []domain.WorkState
	for rows.Next() {
		var state domain.WorkState
		if err := rows.Scan(&state); err != nil {
			return nil, fmt.Errorf("scan required work state: %w", err)
		}
		if !state.Valid() {
			return nil, fmt.Errorf("goal %q has invalid required work state %q", goal.ID, state)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate required work states for goal %q: %w", goal.ID, err)
	}
	return states, nil
}
