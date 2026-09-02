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

// CancelWork atomically makes a Work Item terminal and revokes any active
// execution ownership. Historical Attempts and Leases remain immutable rows;
// only their lifecycle states advance to the cancellation boundary.
func (s *Store) CancelWork(ctx context.Context, id string, expectedVersion int64, event EventInput) (domain.WorkItem, error) {
	if !validIdempotencyLabel(id) || expectedVersion <= 0 {
		return domain.WorkItem{}, errors.New("invalid work cancellation")
	}
	workEvent, err := prepareEvent(event)
	if err != nil {
		return domain.WorkItem{}, err
	}
	attemptEvent, err := prepareEvent(EventInput{
		Type:          "AttemptInterrupted",
		ActorType:     event.ActorType,
		ActorID:       event.ActorID,
		CorrelationID: event.CorrelationID,
		Payload:       map[string]any{"work_item_id": id, "reason": "work cancelled"},
	})
	if err != nil {
		return domain.WorkItem{}, err
	}
	leaseEvent, err := prepareEvent(EventInput{
		Type:          "LeaseRevoked",
		ActorType:     event.ActorType,
		ActorID:       event.ActorID,
		CorrelationID: event.CorrelationID,
		Payload:       map[string]any{"work_item_id": id, "reason": "work cancelled"},
	})
	if err != nil {
		return domain.WorkItem{}, err
	}
	goalEvent, err := prepareEvent(EventInput{
		Type:          "GoalWaiting",
		ActorType:     event.ActorType,
		ActorID:       event.ActorID,
		CorrelationID: event.CorrelationID,
		Payload:       map[string]any{"work_item_id": id, "reason": "required work cancelled"},
	})
	if err != nil {
		return domain.WorkItem{}, err
	}

	var result domain.WorkItem
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		work, err := readWorkItem(ctx, tx, id)
		if err != nil {
			return err
		}
		if work.Version != expectedVersion {
			return fmt.Errorf("work item %q: %w", id, basestore.ErrConflict)
		}
		if err := domain.ValidateWorkTransition(work.State, domain.WorkCancelled); err != nil {
			return fmt.Errorf("work item %q cannot be cancelled: %v: %w", id, err, basestore.ErrConflict)
		}

		lease, leaseErr := scanLease(tx.QueryRowContext(ctx, `
SELECT id, work_item_id, attempt_id, holder, generation, state,
       acquired_at, heartbeat_at, expires_at, version
FROM leases
WHERE work_item_id = ? AND state = ?`, id, domain.LeaseActive))
		if leaseErr != nil && !errors.Is(leaseErr, sql.ErrNoRows) {
			return fmt.Errorf("read active lease for work %q: %w", id, leaseErr)
		}
		now := s.source.Now().UTC()
		if leaseErr == nil {
			attempt, err := readAttempt(ctx, tx, lease.AttemptID)
			if err != nil {
				return err
			}
			if attempt.State.CanTransition(domain.AttemptInterrupted) {
				updated, err := tx.ExecContext(ctx, `
UPDATE attempts
SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`, domain.AttemptInterrupted, now.Format(time.RFC3339Nano), attempt.ID, attempt.Version)
				if err != nil {
					return fmt.Errorf("interrupt attempt %q: %w", attempt.ID, err)
				}
				affected, err := updated.RowsAffected()
				if err != nil || affected != 1 {
					return fmt.Errorf("interrupt attempt %q: %w", attempt.ID, basestore.ErrConflict)
				}
				if err := s.appendEvent(ctx, tx, "attempt", attempt.ID, attemptEvent); err != nil {
					return err
				}
			}
			updated, err := tx.ExecContext(ctx, `
UPDATE leases
SET state = ?, version = version + 1
WHERE id = ? AND generation = ? AND version = ? AND state = ?`,
				domain.LeaseRevoked, lease.ID, lease.Generation, lease.Version, domain.LeaseActive)
			if err != nil {
				return fmt.Errorf("revoke lease %q: %w", lease.ID, err)
			}
			affected, err := updated.RowsAffected()
			if err != nil || affected != 1 {
				return fmt.Errorf("revoke lease %q: %w", lease.ID, basestore.ErrConflict)
			}
			if err := s.appendEvent(ctx, tx, "lease", lease.ID, leaseEvent); err != nil {
				return err
			}
		}

		updated, err := tx.ExecContext(ctx, `
UPDATE work_items
SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`, domain.WorkCancelled, now.Format(time.RFC3339Nano), id, expectedVersion)
		if err != nil {
			return fmt.Errorf("cancel work item %q: %w", id, err)
		}
		affected, err := updated.RowsAffected()
		if err != nil || affected != 1 {
			return fmt.Errorf("cancel work item %q: %w", id, basestore.ErrConflict)
		}
		if err := s.appendEvent(ctx, tx, "work", id, workEvent); err != nil {
			return err
		}
		if work.Required {
			var goalID string
			if err := tx.QueryRowContext(ctx, `
SELECT goal_revision.goal_id
FROM plan_revisions plan
JOIN goal_revisions goal_revision ON goal_revision.id = plan.goal_revision_id
WHERE plan.id = ?`, work.PlanRevisionID).Scan(&goalID); err != nil {
				return fmt.Errorf("resolve Goal for cancelled work %q: %w", id, err)
			}
			goal, err := readGoal(ctx, tx, goalID)
			if err != nil {
				return err
			}
			if goal.State == domain.GoalRunning || goal.State == domain.GoalVerifying {
				updated, err := tx.ExecContext(ctx, `
UPDATE goals
SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`, domain.GoalWaiting, now.Format(time.RFC3339Nano), goal.ID, goal.Version)
				if err != nil {
					return fmt.Errorf("wait Goal %q after required work cancellation: %w", goal.ID, err)
				}
				affected, err := updated.RowsAffected()
				if err != nil || affected != 1 {
					return fmt.Errorf("wait Goal %q after required work cancellation: %w", goal.ID, basestore.ErrConflict)
				}
				if err := s.appendEvent(ctx, tx, "goal", goal.ID, goalEvent); err != nil {
					return err
				}
			}
		}
		work.State = domain.WorkCancelled
		work.Version++
		result = work
		return nil
	})
	return result, err
}
