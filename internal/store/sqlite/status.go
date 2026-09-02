package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/monshunter/xgoal/internal/budget"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/reconcile"
)

type FailureSummary struct {
	ID               string                 `json:"id"`
	Class            reconcile.FailureClass `json:"class"`
	Fingerprint      string                 `json:"fingerprint"`
	Strategy         string                 `json:"strategy"`
	SnapshotHash     string                 `json:"snapshot_hash"`
	MaterialProgress bool                   `json:"material_progress"`
	RepeatCount      int64                  `json:"repeat_count"`
	CreatedAt        time.Time              `json:"created_at"`
}

type GoalStatus struct {
	Goal               domain.Goal                 `json:"goal"`
	WorkItems          []domain.WorkItem           `json:"work_items"`
	Attempts           []domain.Attempt            `json:"attempts"`
	Leases             []domain.Lease              `json:"leases"`
	Workspaces         []WorkspaceArtifact         `json:"workspaces"`
	Gates              []domain.Gate               `json:"gates"`
	Budgets            []BudgetSnapshot            `json:"budgets"`
	Failures           []FailureSummary            `json:"failures"`
	LatestProgressHash string                      `json:"latest_material_progress_hash,omitempty"`
	Authority          map[string]domain.Authority `json:"authority"`
}

// GoalStatus projects current persisted facts without an LLM-generated summary.
func (s *Store) GoalStatus(ctx context.Context, goalID string) (GoalStatus, error) {
	goal, err := s.Goal(ctx, goalID)
	if err != nil {
		return GoalStatus{}, err
	}
	result := GoalStatus{Goal: goal, Authority: map[string]domain.Authority{
		"goal": domain.AuthorityFact, "work_items": domain.AuthorityFact, "attempts": domain.AuthorityFact,
		"leases": domain.AuthorityFact, "workspaces": domain.AuthorityFact, "gates": domain.AuthorityDecision,
		"budgets": domain.AuthorityFact, "failures": domain.AuthorityFact,
	}}
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
	result.Budgets, err = s.goalBudgets(ctx, goalID)
	if err != nil {
		return GoalStatus{}, err
	}
	result.Failures, err = s.goalFailures(ctx, goalID)
	if err != nil {
		return GoalStatus{}, err
	}
	if len(result.Failures) > 0 {
		result.LatestProgressHash = result.Failures[0].SnapshotHash
	}
	return result, nil
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

func (s *Store) goalBudgets(ctx context.Context, goalID string) ([]BudgetSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.goal_id, l.work_item_id, l.dimension, l.soft_limit, l.hard_limit, u.known, u.consumed, u.version FROM budget_limits l JOIN budget_usage u USING(goal_id, work_item_id, dimension) WHERE l.goal_id = ? ORDER BY l.work_item_id, l.dimension`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []BudgetSnapshot
	for rows.Next() {
		var item BudgetSnapshot
		var known int
		if err := rows.Scan(&item.GoalID, &item.WorkItemID, &item.Limit.Dimension, &item.Limit.Soft, &item.Limit.Hard, &known, &item.Usage.Consumed, &item.Version); err != nil {
			return nil, err
		}
		item.Usage = budget.Usage{Dimension: item.Limit.Dimension, Known: known == 1, Consumed: item.Usage.Consumed}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) goalFailures(ctx context.Context, goalID string) ([]FailureSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, failure_class, fingerprint, strategy, snapshot_hash, material_progress, repeat_count, created_at FROM failure_records WHERE goal_id = ? ORDER BY created_at DESC, id DESC LIMIT 20`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []FailureSummary
	for rows.Next() {
		var item FailureSummary
		var progress int
		var createdAt string
		if err := rows.Scan(&item.ID, &item.Class, &item.Fingerprint, &item.Strategy, &item.SnapshotHash, &progress, &item.RepeatCount, &createdAt); err != nil {
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
