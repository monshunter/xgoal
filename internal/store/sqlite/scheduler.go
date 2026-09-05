package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

// RefreshReadyWork deterministically promotes dependency- and Gate-cleared PENDING Work.
func (s *Store) RefreshReadyWork(ctx context.Context, goalID string, event EventInput) ([]string, error) {
	if goalID == "" {
		return nil, errors.New("goal id is empty")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return nil, err
	}
	var ready []string
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		return s.refreshReadyWorkTx(ctx, tx, goalID, prepared, &ready)
	})
	if err != nil {
		return nil, err
	}
	return ready, nil
}

// NextReadyWork returns the stable next candidate only when the project execution slot is free.
func (s *Store) NextReadyWork(ctx context.Context, goalID string) (domain.WorkItem, error) {
	if goalID == "" {
		return domain.WorkItem{}, errors.New("goal id is empty")
	}
	var activeLeases int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM leases WHERE state = ?`, domain.LeaseActive).Scan(&activeLeases); err != nil {
		return domain.WorkItem{}, fmt.Errorf("check project execution slot: %w", err)
	}
	if activeLeases != 0 {
		return domain.WorkItem{}, fmt.Errorf("project execution slot is occupied: %w", basestore.ErrNotFound)
	}
	var workID string
	err := s.db.QueryRowContext(ctx, `
SELECT work_items.id
FROM work_items
JOIN plan_revisions plan ON plan.id = work_items.plan_revision_id
JOIN goal_revisions goal_revision ON goal_revision.id = plan.goal_revision_id
JOIN goals goal ON goal.id = goal_revision.goal_id
WHERE goal.id = ?
  AND goal.state = ?
  AND goal.active_revision_id = goal_revision.id
  AND plan.status = ?
  AND work_items.state = ?
  AND NOT EXISTS (
      SELECT 1
      FROM gates gate_record
      WHERE gate_record.goal_id = goal.id
        AND `+gateBlocksExecution+`
        AND gate_record.required = 1
        AND (gate_record.work_item_id IS NULL OR gate_record.work_item_id = work_items.id)
  )
ORDER BY work_items.id
LIMIT 1`, goalID, domain.GoalRunning, domain.PlanActive, domain.WorkReady, s.source.Now().UTC().Format(time.RFC3339Nano)).Scan(&workID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkItem{}, fmt.Errorf("ready work for goal %q: %w", goalID, basestore.ErrNotFound)
	}
	if err != nil {
		return domain.WorkItem{}, fmt.Errorf("read next work for goal %q: %w", goalID, err)
	}
	return readWorkItem(ctx, s.db, workID)
}

// RefreshReadyWork transaction body is shared by standalone operations and atomic planning publication.
func (s *Store) refreshReadyWorkTx(ctx context.Context, tx *sql.Tx, goalID string, prepared preparedEvent, ready *[]string) error {
	goal, err := readGoal(ctx, tx, goalID)
	if err != nil {
		return err
	}
	if goal.State != domain.GoalRunning {
		return fmt.Errorf("goal %q is not running: %w", goalID, basestore.ErrConflict)
	}
	rows, err := tx.QueryContext(ctx, `
SELECT work.id
FROM work_items work
JOIN plan_revisions plan ON plan.id = work.plan_revision_id
JOIN goal_revisions goal_revision ON goal_revision.id = plan.goal_revision_id
WHERE goal_revision.goal_id = ?
  AND goal_revision.id = ?
  AND plan.status = ?
  AND work.state = ?
  AND NOT EXISTS (
      SELECT 1
      FROM work_dependencies dependency
      JOIN work_items prerequisite ON prerequisite.id = dependency.from_id
      WHERE dependency.to_id = work.id
        AND dependency.dependency_type = ?
        AND prerequisite.state <> ?
  )
  AND NOT EXISTS (
      SELECT 1
      FROM gates gate_record
      WHERE gate_record.goal_id = ?
        AND `+gateBlocksExecution+`
        AND gate_record.required = 1
        AND (gate_record.work_item_id IS NULL OR gate_record.work_item_id = work.id)
  )
ORDER BY work.id`,
		goalID,
		goal.ActiveRevisionID,
		domain.PlanActive,
		domain.WorkPending,
		domain.DependencyHard,
		domain.WorkCompleted,
		goalID,
		s.source.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("find ready work for goal %q: %w", goalID, err)
	}
	var candidates []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan ready work candidate: %w", err)
		}
		candidates = append(candidates, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate ready work candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close ready work candidates: %w", err)
	}

	now := s.source.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range candidates {
		updated, err := tx.ExecContext(ctx, `
UPDATE work_items
SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND state = ?`, domain.WorkReady, now, id, domain.WorkPending)
		if err != nil {
			return fmt.Errorf("promote work item %q to ready: %w", id, err)
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return fmt.Errorf("read work ready result: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("work item %q: %w", id, basestore.ErrConflict)
		}
		if err := s.appendEvent(ctx, tx, "work", id, prepared); err != nil {
			return err
		}
		*ready = append(*ready, id)
	}
	return nil
}
