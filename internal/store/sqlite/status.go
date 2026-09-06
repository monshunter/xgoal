package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/reconcile"
)

type FailureSummary struct {
	ID               string                 `json:"id"`
	Class            reconcile.FailureClass `json:"class"`
	Error            string                 `json:"error"`
	Fingerprint      string                 `json:"fingerprint"`
	Strategy         string                 `json:"strategy"`
	SnapshotHash     string                 `json:"snapshot_hash"`
	MaterialProgress bool                   `json:"material_progress"`
	RepeatCount      int64                  `json:"repeat_count"`
	CreatedAt        time.Time              `json:"created_at"`
}

type GoalRevisionSummary struct {
	ID        string    `json:"id"`
	Revision  int64     `json:"revision"`
	Hash      string    `json:"hash"`
	StartedAt time.Time `json:"started_at"`
}

type ValidationSummary struct {
	Current    int64 `json:"current"`
	Stale      int64 `json:"stale"`
	Superseded int64 `json:"superseded"`
	Invalid    int64 `json:"invalid"`
}

type GoalStatus struct {
	Activity           GoalActivity                `json:"activity"`
	ExecutionModel     string                      `json:"execution_model"`
	ExecutionBlocker   string                      `json:"execution_blocker,omitempty"`
	Goal               domain.Goal                 `json:"goal"`
	GoalRevision       *GoalRevisionSummary        `json:"goal_revision,omitempty"`
	WorkItems          []domain.WorkItem           `json:"work_items"`
	Attempts           []domain.Attempt            `json:"attempts"`
	Leases             []domain.Lease              `json:"leases"`
	Workspaces         []WorkspaceArtifact         `json:"workspaces"`
	Gates              []domain.Gate               `json:"gates"`
	Failures           []FailureSummary            `json:"failures"`
	Findings           []ReviewFinding             `json:"findings"`
	LatestTree         string                      `json:"latest_tree,omitempty"`
	Validation         ValidationSummary           `json:"validation_summary"`
	LatestProgressHash string                      `json:"latest_material_progress_hash,omitempty"`
	Authority          map[string]domain.Authority `json:"authority"`
}

// GoalStatus projects current persisted facts without an LLM-generated summary.
func (s *Store) GoalStatus(ctx context.Context, goalID string) (GoalStatus, error) {
	goal, err := s.Goal(ctx, goalID)
	if err != nil {
		return GoalStatus{}, err
	}
	model, err := s.GoalExecutionModel(ctx, goalID)
	if err != nil {
		return GoalStatus{}, err
	}
	result := GoalStatus{Goal: goal, Authority: map[string]domain.Authority{
		"goal": domain.AuthorityFact, "goal_revision": domain.AuthorityDecision, "work_items": domain.AuthorityFact, "attempts": domain.AuthorityFact,
		"leases": domain.AuthorityFact, "workspaces": domain.AuthorityFact, "gates": domain.AuthorityDecision,
		"failures": domain.AuthorityFact, "findings": domain.AuthorityInference,
		"latest_tree": domain.AuthorityFact, "validation_summary": domain.AuthorityDeterministic,
		"latest_material_progress_hash": domain.AuthorityDeterministic,
		"activity":                      domain.AuthorityDeterministic,
	}}
	result.ExecutionModel = model
	if model != "current-directory" && goal.State != domain.GoalCompleted && goal.State != domain.GoalCancelled {
		result.ExecutionBlocker = ErrExecutionMigrationRequired.Error()
	}
	if goal.ActiveRevisionID != "" {
		revision, revisionErr := s.GoalRevision(ctx, goal.ActiveRevisionID)
		if revisionErr != nil {
			return GoalStatus{}, revisionErr
		}
		result.GoalRevision = &GoalRevisionSummary{ID: revision.ID, Revision: revision.Revision, Hash: revision.Hash, StartedAt: revision.FrozenAt}
	}
	result.WorkItems, err = s.GoalWorkItems(ctx, goalID)
	if err != nil {
		return GoalStatus{}, err
	}
	result.Attempts, err = s.goalAttempts(ctx, goalID)
	if err != nil {
		return GoalStatus{}, err
	}
	result.Leases, err = s.goalLeases(ctx, goalID)
	if err != nil {
		return GoalStatus{}, err
	}
	result.Workspaces, err = s.goalWorkspaces(ctx, goalID)
	if err != nil {
		return GoalStatus{}, err
	}
	result.Gates, err = s.Gates(ctx, goalID, false)
	if err != nil {
		return GoalStatus{}, err
	}
	result.Failures, err = s.goalFailures(ctx, goalID)
	if err != nil {
		return GoalStatus{}, err
	}
	result.Findings, err = s.GoalFindings(ctx, goalID)
	if err != nil {
		return GoalStatus{}, err
	}
	result.LatestTree = goal.FinalTree
	if result.LatestTree == "" {
		for index := len(result.Attempts) - 1; index >= 0; index-- {
			if result.Attempts[index].ResultTree != "" {
				result.LatestTree = result.Attempts[index].ResultTree
				break
			}
			if result.LatestTree == "" && result.Attempts[index].BaseTree != "" {
				result.LatestTree = result.Attempts[index].BaseTree
			}
		}
	}
	result.Validation, err = s.goalValidationSummary(ctx, goalID)
	if err != nil {
		return GoalStatus{}, err
	}
	result.LatestProgressHash, err = s.goalProgressHash(ctx, result)
	if err != nil {
		return GoalStatus{}, err
	}
	result.Activity, err = s.goalActivity(ctx, result)
	if err != nil {
		return GoalStatus{}, err
	}
	return result, nil
}

type evidenceProgress struct {
	ID          string               `json:"id"`
	State       domain.EvidenceState `json:"state"`
	TreeHash    string               `json:"tree_hash"`
	PayloadHash string               `json:"payload_hash"`
}

func (s *Store) goalProgressHash(ctx context.Context, status GoalStatus) (string, error) {
	work := make([]struct {
		ID             string           `json:"id"`
		PlanRevisionID string           `json:"plan_revision_id"`
		State          domain.WorkState `json:"state"`
	}, len(status.WorkItems))
	for index, item := range status.WorkItems {
		work[index] = struct {
			ID             string           `json:"id"`
			PlanRevisionID string           `json:"plan_revision_id"`
			State          domain.WorkState `json:"state"`
		}{item.ID, item.PlanRevisionID, item.State}
	}
	trees := make([]struct {
		ID   string `json:"attempt_id"`
		Tree string `json:"result_tree"`
	}, 0, len(status.Attempts))
	for _, attempt := range status.Attempts {
		if attempt.ResultTree != "" {
			trees = append(trees, struct {
				ID   string `json:"attempt_id"`
				Tree string `json:"result_tree"`
			}{attempt.ID, attempt.ResultTree})
		}
	}
	gates := make([]struct {
		ID       string              `json:"id"`
		State    domain.GateState    `json:"state"`
		Decision domain.GateDecision `json:"decision"`
	}, len(status.Gates))
	for index, gate := range status.Gates {
		gates[index] = struct {
			ID       string              `json:"id"`
			State    domain.GateState    `json:"state"`
			Decision domain.GateDecision `json:"decision"`
		}{gate.ID, gate.State, gate.Decision}
	}
	findings := make([]struct {
		ID    string              `json:"id"`
		State domain.FindingState `json:"state"`
	}, len(status.Findings))
	for index, finding := range status.Findings {
		findings[index] = struct {
			ID    string              `json:"id"`
			State domain.FindingState `json:"state"`
		}{finding.Finding.ID, finding.State}
	}
	evidence, err := s.goalEvidenceProgress(ctx, status.Goal.ID)
	if err != nil {
		return "", err
	}
	revisionHash := ""
	if status.GoalRevision != nil {
		revisionHash = status.GoalRevision.Hash
	}
	return canonical.Hash("goal-material-progress", "v1", struct {
		RevisionHash string             `json:"revision_hash"`
		LatestTree   string             `json:"latest_tree"`
		Work         any                `json:"work"`
		ResultTrees  any                `json:"result_trees"`
		Evidence     []evidenceProgress `json:"evidence"`
		Gates        any                `json:"gates"`
		Findings     any                `json:"findings"`
	}{revisionHash, status.LatestTree, work, trees, evidence, gates, findings})
}

func (s *Store) goalEvidenceProgress(ctx context.Context, goalID string) ([]evidenceProgress, error) {
	rows, err := s.db.QueryContext(ctx, `
WITH goal_subjects(id) AS (
  SELECT ?
  UNION
  SELECT work.id
  FROM work_items work
  JOIN plan_revisions plan ON plan.id = work.plan_revision_id
  JOIN goal_revisions revision ON revision.id = plan.goal_revision_id
  WHERE revision.goal_id = ?
), latest_evidence_state AS (
  SELECT changes.evidence_id, changes.state
  FROM evidence_state_changes changes
  WHERE changes.sequence = (
    SELECT MAX(candidate.sequence)
    FROM evidence_state_changes candidate
    WHERE candidate.evidence_id = changes.evidence_id
  )
)
SELECT records.id, latest.state, records.tree_hash, records.payload_hash
FROM evidence_records records
JOIN latest_evidence_state latest ON latest.evidence_id = records.id
WHERE records.subject_id IN (SELECT id FROM goal_subjects)
ORDER BY records.id`, goalID, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []evidenceProgress
	for rows.Next() {
		var item evidenceProgress
		if err := rows.Scan(&item.ID, &item.State, &item.TreeHash, &item.PayloadHash); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) goalValidationSummary(ctx context.Context, goalID string) (ValidationSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
WITH goal_subjects(id) AS (
  SELECT ?
  UNION
  SELECT work.id
  FROM work_items work
  JOIN plan_revisions plan ON plan.id = work.plan_revision_id
  JOIN goal_revisions revision ON revision.id = plan.goal_revision_id
  WHERE revision.goal_id = ?
), latest_evidence_state AS (
  SELECT changes.evidence_id, changes.state
  FROM evidence_state_changes changes
  WHERE changes.sequence = (
    SELECT MAX(candidate.sequence)
    FROM evidence_state_changes candidate
    WHERE candidate.evidence_id = changes.evidence_id
  )
)
SELECT state, COUNT(*)
FROM evidence_records records
JOIN latest_evidence_state latest ON latest.evidence_id = records.id
WHERE records.subject_id IN (SELECT id FROM goal_subjects)
GROUP BY state`, goalID, goalID)
	if err != nil {
		return ValidationSummary{}, err
	}
	defer rows.Close()
	var result ValidationSummary
	for rows.Next() {
		var state domain.EvidenceState
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return ValidationSummary{}, err
		}
		switch state {
		case domain.EvidenceCurrent:
			result.Current = count
		case domain.EvidenceStale:
			result.Stale = count
		case domain.EvidenceSuperseded:
			result.Superseded = count
		case domain.EvidenceInvalid:
			result.Invalid = count
		}
	}
	return result, rows.Err()
}

func (s *Store) goalAttempts(ctx context.Context, goalID string) ([]domain.Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT attempt.id FROM attempts attempt JOIN work_items work ON work.id = attempt.work_item_id JOIN plan_revisions plan ON plan.id = work.plan_revision_id JOIN goal_revisions revision ON revision.id = plan.goal_revision_id WHERE revision.goal_id = ? ORDER BY attempt.created_at, attempt.id`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Attempt
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, domain.Attempt{ID: id})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range result {
		attempt, err := s.Attempt(ctx, result[index].ID)
		if err != nil {
			return nil, err
		}
		result[index] = attempt
	}
	return result, nil
}

func (s *Store) goalLeases(ctx context.Context, goalID string) ([]domain.Lease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT lease.id, lease.work_item_id, lease.attempt_id, lease.holder, lease.generation, lease.state, lease.acquired_at, lease.heartbeat_at, lease.expires_at, lease.version FROM leases lease JOIN work_items work ON work.id = lease.work_item_id JOIN plan_revisions plan ON plan.id = work.plan_revision_id JOIN goal_revisions revision ON revision.id = plan.goal_revision_id WHERE revision.goal_id = ? ORDER BY lease.acquired_at, lease.id`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Lease
	for rows.Next() {
		lease, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, lease)
	}
	return result, rows.Err()
}

func (s *Store) goalWorkspaces(ctx context.Context, goalID string) ([]WorkspaceArtifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workspace.id FROM workspaces workspace JOIN attempts attempt ON attempt.id = workspace.attempt_id JOIN work_items work ON work.id = attempt.work_item_id JOIN plan_revisions plan ON plan.id = work.plan_revision_id JOIN goal_revisions revision ON revision.id = plan.goal_revision_id WHERE revision.goal_id = ? ORDER BY workspace.created_at, workspace.id`, goalID)
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
	result := make([]WorkspaceArtifact, 0, len(ids))
	for _, id := range ids {
		workspace, err := s.WorkspaceArtifact(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, workspace)
	}
	return result, nil
}

func (s *Store) goalFailures(ctx context.Context, goalID string) ([]FailureSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, failure_class, normalized_error, fingerprint, strategy, snapshot_hash, material_progress, repeat_count, created_at FROM failure_records WHERE goal_id = ? ORDER BY created_at DESC, id DESC LIMIT 20`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []FailureSummary
	for rows.Next() {
		var item FailureSummary
		var progress int
		var createdAt string
		if err := rows.Scan(&item.ID, &item.Class, &item.Error, &item.Fingerprint, &item.Strategy, &item.SnapshotHash, &progress, &item.RepeatCount, &createdAt); err != nil {
			return nil, err
		}
		item.MaterialProgress = progress == 1
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse failure created_at: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
