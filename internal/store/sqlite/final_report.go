package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/completion"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/evidence"
	finalreport "github.com/monshunter/xgoal/internal/report"
	basestore "github.com/monshunter/xgoal/internal/store"
)

type FinalReportState string

const (
	FinalReportPending   FinalReportState = "PENDING_RENAME"
	FinalReportCommitted FinalReportState = "COMMITTED"
)

type FinalReportRecord struct {
	GoalRevisionID   string
	GoalRevisionHash string
	ConfigHash       string
	TreeHash         string
	EvidenceSetID    string
	Files            finalreport.PreparedFiles
	State            FinalReportState
	Version          int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// FinalizeGoal atomically persists report identity and completion facts with the final Goal tuple.
// Report files must already be privately prepared and are intentionally published after this commit.
func (s *Store) FinalizeGoal(
	ctx context.Context,
	goalID string,
	expectedGoalVersion int64,
	facts CompletionFacts,
	files finalreport.PreparedFiles,
	event EventInput,
) (completion.Result, FinalReportRecord, error) {
	if goalID == "" || expectedGoalVersion <= 0 || files.GoalID != goalID || !safeReportPaths(files) {
		return completion.Result{}, FinalReportRecord{}, errors.New("invalid finalization request")
	}
	decoded, err := finalreport.VerifyArtifact(finalreport.Artifact{
		JSON: files.JSON, Markdown: files.Markdown, JSONHash: files.JSONHash,
		MarkdownHash: files.MarkdownHash, ReportHash: files.ReportHash,
	})
	if err != nil {
		return completion.Result{}, FinalReportRecord{}, fmt.Errorf("verify final report: %w", err)
	}
	if decoded.Goal.ID != goalID || decoded.Final.Tree != facts.IntegrationTree ||
		decoded.Final.EvidenceSetID != facts.FinalEvidenceSetID || files.ReportHash != facts.FinalReportHash {
		return completion.Result{}, FinalReportRecord{}, errors.New("final report and completion facts have different bindings")
	}
	if err := validateCompletionFacts(goalID, facts); err != nil {
		return completion.Result{}, FinalReportRecord{}, err
	}
	if !completionCriteriaMatch(decoded.Criteria, facts.Criteria, facts.IntegrationTree) {
		return completion.Result{}, FinalReportRecord{}, errors.New("final report and completion facts have different criteria")
	}
	preparedEvent, err := prepareEvent(event)
	if err != nil {
		return completion.Result{}, FinalReportRecord{}, err
	}
	var result completion.Result
	var record FinalReportRecord
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		goal, err := readGoal(ctx, tx, goalID)
		if err != nil {
			return err
		}
		if goal.Version != expectedGoalVersion {
			return fmt.Errorf("goal %q: %w", goalID, basestore.ErrConflict)
		}
		revision, err := readGoalRevision(ctx, tx, goal.ActiveRevisionID)
		if err != nil {
			return err
		}
		if decoded.Goal.Revision != revision.Revision || decoded.Goal.RevisionHash != revision.Hash {
			return errors.New("final report does not bind the active Goal Revision")
		}
		set, err := readEvidenceSet(ctx, tx, facts.FinalEvidenceSetID)
		if err != nil {
			return err
		}
		if set.Phase != evidence.SetFinal || set.GoalRevisionHash != revision.Hash || set.ConfigHash != decoded.Goal.ConfigHash || set.TreeHash != facts.IntegrationTree {
			return errors.New("final evidence set binding does not match report and active revision")
		}
		current, err := evidenceSetCurrentInTx(ctx, tx, set)
		if err != nil {
			return err
		}
		criteriaCurrent := reportCriteriaCurrent(decoded.Criteria, set)
		states, err := requiredWorkStates(ctx, tx, goal)
		if err != nil {
			return err
		}
		openFindings, err := countOpenBlockingFindings(ctx, tx, goalID)
		if err != nil {
			return err
		}
		openGates, err := countOpenRequiredGates(ctx, tx, goalID)
		if err != nil {
			return err
		}
		activeLeases, err := countActiveGoalLeases(ctx, tx, goalID)
		if err != nil {
			return err
		}
		facts.OpenBlockingFindings = openFindings
		facts.FinalValidationSetCurrent = current && criteriaCurrent
		result = completion.Evaluate(completion.Input{
			GoalState: goal.State, RequiredWorkStates: states,
			IntegrationTree: facts.IntegrationTree, ExpectedTree: facts.ExpectedTree,
			Criteria: facts.Criteria, OpenBlockingFindings: openFindings, OpenRequiredGates: openGates,
			ScopePolicyPassed: facts.ScopePolicyPassed, FinalValidationSetCurrent: facts.FinalValidationSetCurrent,
			FinalReportGenerated: true, HumanAcceptanceRequired: facts.HumanAcceptanceRequired,
			HumanAcceptanceSatisfied: facts.HumanAcceptanceSatisfied,
		})
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
		criteriaJSON, err := canonical.Marshal(facts.Criteria)
		if err != nil {
			return err
		}
		now := s.source.Now().UTC()
		if err := upsertCompletionFactsTx(ctx, tx, goalID, facts, criteriaJSON, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO final_reports(
    goal_id, goal_revision_id, protocol_version, goal_revision_hash,
    config_hash, tree_hash, evidence_set_id, report_hash,
    json_path, json_temp_path, json_hash, json_blob,
    markdown_path, markdown_temp_path, markdown_hash, markdown_blob,
    state, version, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			goalID, revision.ID, files.ProtocolVersion, revision.Hash,
			set.ConfigHash, set.TreeHash, set.ID, files.ReportHash,
			files.JSONPath, files.JSONTempPath, files.JSONHash, files.JSON,
			files.MarkdownPath, files.MarkdownTempPath, files.MarkdownHash, files.Markdown,
			FinalReportPending, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("insert final report for goal %q: %w", goalID, err)
		}
		updated, err := tx.ExecContext(ctx, `
UPDATE goals
SET state = ?, final_tree = ?, final_evidence_set_id = ?, final_report_hash = ?,
    version = version + 1, updated_at = ?
WHERE id = ? AND version = ? AND state = ?`,
			domain.GoalCompleted, set.TreeHash, set.ID, files.ReportHash,
			now.Format(time.RFC3339Nano), goalID, expectedGoalVersion, domain.GoalVerifying,
		)
		if err != nil {
			return fmt.Errorf("complete goal %q with report: %w", goalID, err)
		}
		affected, err := updated.RowsAffected()
		if err != nil || affected != 1 {
			return fmt.Errorf("goal %q completion CAS: %w", goalID, basestore.ErrConflict)
		}
		if err := s.appendEvent(ctx, tx, "goal", goalID, preparedEvent); err != nil {
			return err
		}
		record = finalReportRecord(goalID, revision.ID, revision.Hash, set.ConfigHash, set.TreeHash, set.ID, files, FinalReportPending, 1, now, now)
		return nil
	})
	if err != nil {
		return completion.Result{}, FinalReportRecord{}, err
	}
	return result, record, nil
}

func (s *Store) MarkFinalReportCommitted(ctx context.Context, goalID string, expectedVersion int64) (FinalReportRecord, error) {
	if goalID == "" || expectedVersion <= 0 {
		return FinalReportRecord{}, errors.New("invalid final report commit request")
	}
	var committed FinalReportRecord
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		current, err := readFinalReport(ctx, tx, goalID)
		if err != nil {
			return err
		}
		if current.State == FinalReportCommitted {
			committed = current
			return nil
		}
		if current.Version != expectedVersion {
			return fmt.Errorf("final report %q commit CAS: %w", goalID, basestore.ErrConflict)
		}
		now := s.source.Now().UTC()
		result, err := tx.ExecContext(ctx, `
UPDATE final_reports SET state = ?, version = version + 1, updated_at = ?
WHERE goal_id = ? AND version = ? AND state = ?`,
			FinalReportCommitted, now.Format(time.RFC3339Nano), goalID, expectedVersion, FinalReportPending)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil || affected != 1 {
			return fmt.Errorf("final report %q commit CAS: %w", goalID, basestore.ErrConflict)
		}
		current.State = FinalReportCommitted
		current.Version++
		current.UpdatedAt = now
		committed = current
		return nil
	})
	if err != nil {
		return FinalReportRecord{}, err
	}
	return committed, nil
}

func (s *Store) FinalReport(ctx context.Context, goalID string) (FinalReportRecord, error) {
	if goalID == "" {
		return FinalReportRecord{}, errors.New("goal id is required")
	}
	return readFinalReport(ctx, s.db, goalID)
}

func (s *Store) PendingFinalReports(ctx context.Context) ([]FinalReportRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT goal_id FROM final_reports WHERE state = ? ORDER BY goal_id`, FinalReportPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	records := make([]FinalReportRecord, 0, len(ids))
	for _, id := range ids {
		record, err := s.FinalReport(ctx, id)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func readFinalReport(ctx context.Context, queryer rowQueryer, goalID string) (FinalReportRecord, error) {
	var record FinalReportRecord
	var createdAt, updatedAt string
	record.Files.GoalID = goalID
	err := queryer.QueryRowContext(ctx, `
SELECT goal_revision_id, protocol_version, goal_revision_hash, config_hash,
       tree_hash, evidence_set_id, report_hash,
       json_path, json_temp_path, json_hash, json_blob,
       markdown_path, markdown_temp_path, markdown_hash, markdown_blob,
       state, version, created_at, updated_at
FROM final_reports WHERE goal_id = ?`, goalID).Scan(
		&record.GoalRevisionID, &record.Files.ProtocolVersion, &record.GoalRevisionHash, &record.ConfigHash,
		&record.TreeHash, &record.EvidenceSetID, &record.Files.ReportHash,
		&record.Files.JSONPath, &record.Files.JSONTempPath, &record.Files.JSONHash, &record.Files.JSON,
		&record.Files.MarkdownPath, &record.Files.MarkdownTempPath, &record.Files.MarkdownHash, &record.Files.Markdown,
		&record.State, &record.Version, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return FinalReportRecord{}, fmt.Errorf("final report for goal %q: %w", goalID, basestore.ErrNotFound)
	}
	if err != nil {
		return FinalReportRecord{}, err
	}
	record.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return FinalReportRecord{}, err
	}
	record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	return record, err
}

func upsertCompletionFactsTx(ctx context.Context, tx *sql.Tx, goalID string, facts CompletionFacts, criteriaJSON []byte, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO goal_completion_facts(
    goal_id, integration_tree, expected_tree, criteria_json,
    open_blocking_findings, scope_policy_passed, final_validation_current,
    final_evidence_set_id, final_report_hash, human_acceptance_required,
    human_acceptance_satisfied, version, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
ON CONFLICT(goal_id) DO UPDATE SET
    integration_tree=excluded.integration_tree, expected_tree=excluded.expected_tree,
    criteria_json=excluded.criteria_json, open_blocking_findings=excluded.open_blocking_findings,
    scope_policy_passed=excluded.scope_policy_passed, final_validation_current=excluded.final_validation_current,
    final_evidence_set_id=excluded.final_evidence_set_id, final_report_hash=excluded.final_report_hash,
    human_acceptance_required=excluded.human_acceptance_required,
    human_acceptance_satisfied=excluded.human_acceptance_satisfied,
    version=goal_completion_facts.version+1, updated_at=excluded.updated_at`,
		goalID, facts.IntegrationTree, facts.ExpectedTree, criteriaJSON,
		facts.OpenBlockingFindings, boolInteger(facts.ScopePolicyPassed), boolInteger(facts.FinalValidationSetCurrent),
		facts.FinalEvidenceSetID, facts.FinalReportHash, boolInteger(facts.HumanAcceptanceRequired),
		boolInteger(facts.HumanAcceptanceSatisfied), now.UTC().Format(time.RFC3339Nano))
	return err
}

func evidenceSetCurrentInTx(ctx context.Context, tx *sql.Tx, set evidence.Set) (bool, error) {
	for _, evidenceID := range set.EvidenceIDs {
		snapshot, err := readEvidence(ctx, tx, evidenceID)
		if err != nil {
			return false, err
		}
		if snapshot.State != domain.EvidenceCurrent || snapshot.Evidence.GoalRevisionHash != set.GoalRevisionHash ||
			snapshot.Evidence.ConfigHash != set.ConfigHash || snapshot.Evidence.TreeHash != set.TreeHash {
			return false, nil
		}
	}
	return len(set.EvidenceIDs) > 0, nil
}

func reportCriteriaCurrent(criteria []finalreport.CriterionTrace, set evidence.Set) bool {
	members := make(map[string]struct{}, len(set.EvidenceIDs))
	for _, id := range set.EvidenceIDs {
		members[id] = struct{}{}
	}
	for _, criterion := range criteria {
		if criterion.Status != "PASS" || len(criterion.EvidenceIDs) == 0 {
			return false
		}
		for _, id := range criterion.EvidenceIDs {
			if _, exists := members[id]; !exists {
				return false
			}
		}
	}
	return len(criteria) > 0
}

func completionCriteriaMatch(reportCriteria []finalreport.CriterionTrace, facts []completion.CriterionStatus, tree string) bool {
	if len(reportCriteria) != len(facts) {
		return false
	}
	statuses := make(map[string]completion.CriterionStatus, len(facts))
	for _, status := range facts {
		statuses[status.ID] = status
	}
	for _, criterion := range reportCriteria {
		status, exists := statuses[criterion.ID]
		if !exists || criterion.Status != "PASS" || !status.Satisfied || !status.Current || status.TreeHash != tree {
			return false
		}
	}
	return true
}

func countOpenBlockingFindings(ctx context.Context, tx *sql.Tx, goalID string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM review_findings finding
JOIN review_runs review ON review.id = finding.review_id
JOIN attempts attempt ON attempt.id = review.attempt_id
JOIN work_items work ON work.id = attempt.work_item_id
JOIN plan_revisions plan ON plan.id = work.plan_revision_id
JOIN goal_revisions revision ON revision.id = plan.goal_revision_id
WHERE revision.goal_id = ? AND finding.state = 'OPEN' AND finding.severity IN ('blocker','high')`, goalID).Scan(&count)
	return count, err
}

func countOpenRequiredGates(ctx context.Context, tx *sql.Tx, goalID string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gates WHERE goal_id = ? AND required = 1 AND state = ?`, goalID, domain.GateOpen).Scan(&count)
	return count, err
}

func countActiveGoalLeases(ctx context.Context, tx *sql.Tx, goalID string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM leases lease
JOIN work_items work ON work.id = lease.work_item_id
JOIN plan_revisions plan ON plan.id = work.plan_revision_id
JOIN goal_revisions revision ON revision.id = plan.goal_revision_id
WHERE revision.goal_id = ? AND lease.state = ?`, goalID, domain.LeaseActive).Scan(&count)
	return count, err
}

func safeReportPaths(files finalreport.PreparedFiles) bool {
	for _, value := range []string{files.JSONPath, files.JSONTempPath, files.MarkdownPath, files.MarkdownTempPath} {
		if value == "" || filepath.IsAbs(value) || filepath.Clean(value) != value || value == "." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
			return false
		}
	}
	var decoded map[string]any
	return json.Unmarshal(files.JSON, &decoded) == nil
}

func finalReportRecord(goalID, revisionID, revisionHash, configHash, treeHash, evidenceSetID string, files finalreport.PreparedFiles, state FinalReportState, version int64, createdAt, updatedAt time.Time) FinalReportRecord {
	return FinalReportRecord{GoalRevisionID: revisionID, GoalRevisionHash: revisionHash, ConfigHash: configHash, TreeHash: treeHash, EvidenceSetID: evidenceSetID, Files: files, State: state, Version: version, CreatedAt: createdAt, UpdatedAt: updatedAt}
}
