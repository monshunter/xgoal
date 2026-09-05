package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/gitrepo"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/workspace"
)

const checkoutKey = "current_directory_checkout_v1"

var (
	ErrCheckoutConflict           = errors.New("CHECKOUT_WAITING: current files or Git identity are not the accepted checkout; preserve files and inspect the owning Goal")
	ErrCheckoutBusy               = errors.New("CHECKOUT_BUSY: an unfinished Work or Promotion owns the current directory")
	ErrExecutionMigrationRequired = errors.New("EXECUTION_MIGRATION_REQUIRED: historical worktree Goal is read-only; inspect its report or create a new Goal from a reviewed clean checkout")
)

type CheckoutControl struct {
	GoalID          string                   `json:"goal_id"`
	WorkID          string                   `json:"work_id"`
	Identity        gitrepo.CheckoutIdentity `json:"identity"`
	AcceptedCommit  string                   `json:"accepted_commit"`
	AcceptedTree    string                   `json:"accepted_tree"`
	ObservedTree    string                   `json:"observed_tree"`
	RetryAuthorized bool                     `json:"retry_authorized"`
}

func (s *Store) GoalExecutionModel(ctx context.Context, goalID string) (string, error) {
	var model string
	err := s.db.QueryRowContext(ctx, `SELECT execution_model FROM goals WHERE id=?`, goalID).Scan(&model)
	if errors.Is(err, sql.ErrNoRows) {
		return "", basestore.ErrNotFound
	}
	return model, err
}
func (s *Store) Checkout(ctx context.Context) (CheckoutControl, error) {
	return readCheckout(ctx, s.db)
}
func readCheckout(ctx context.Context, q rowQueryer) (CheckoutControl, error) {
	var encoded []byte
	if err := q.QueryRowContext(ctx, `SELECT value FROM store_metadata WHERE key=?`, checkoutKey).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CheckoutControl{}, basestore.ErrNotFound
		}
		return CheckoutControl{}, err
	}
	var record CheckoutControl
	if err := decodeCanonical(encoded, &record); err != nil {
		return record, err
	}
	if record.GoalID == "" || record.Identity.Validate() != nil || !artifactObjectID(record.AcceptedCommit) || !artifactObjectID(record.AcceptedTree) || !artifactObjectID(record.ObservedTree) {
		return record, errors.New("invalid persisted checkout identity")
	}
	return record, nil
}
func saveCheckout(ctx context.Context, tx *sql.Tx, record CheckoutControl) error {
	encoded, err := canonical.Marshal(record)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO store_metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, checkoutKey, encoded)
	return err
}
func checkoutIdle(ctx context.Context, tx *sql.Tx) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM leases WHERE state='ACTIVE') + (SELECT COUNT(*) FROM worker_processes WHERE state IN ('RUNNING','OBSERVING')) + (SELECT COUNT(*) FROM promotions p JOIN goals g ON g.id=p.goal_id WHERE g.execution_model='current-directory' AND p.state NOT IN ('OBSERVED','FAILED'))`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrCheckoutBusy
	}
	return nil
}

// AdmitCheckout runs under daemon ownership after a real Git/filesystem snapshot.
// The persisted record owns acceptance; the private ref alone is never authority.
func (s *Store) AdmitCheckout(ctx context.Context, goalID string, identity gitrepo.CheckoutIdentity, currentTree string) (CheckoutControl, error) {
	if identity.Validate() != nil || !artifactObjectID(currentTree) {
		return CheckoutControl{}, ErrCheckoutConflict
	}
	var result CheckoutControl
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		goal, err := readGoal(ctx, tx, goalID)
		if err != nil {
			return err
		}
		if goal.State == domain.GoalCompleted || goal.State == domain.GoalCancelled {
			return basestore.ErrConflict
		}
		var model string
		if err := tx.QueryRowContext(ctx, `SELECT execution_model FROM goals WHERE id=?`, goalID).Scan(&model); err != nil {
			return err
		}
		if model != workspace.ExecutionCurrentDirectory {
			return ErrExecutionMigrationRequired
		}
		previous, err := readCheckout(ctx, tx)
		if err == nil && previous.GoalID == goalID {
			if previous.Identity != identity {
				return ErrCheckoutConflict
			}
			expected := previous.AcceptedTree
			if previous.RetryAuthorized {
				expected = previous.ObservedTree
			} else if previous.WorkID != "" {
				return ErrCheckoutBusy
			}
			if expected != currentTree {
				return ErrCheckoutConflict
			}
			result = previous
			return nil
		}
		if err != nil && !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		if err := checkoutIdle(ctx, tx); err != nil {
			return err
		}
		result = CheckoutControl{GoalID: goalID, Identity: identity, AcceptedCommit: identity.HeadCommit, AcceptedTree: identity.HeadTree, ObservedTree: currentTree}
		clean := identity.IndexTree == identity.HeadTree && currentTree == identity.HeadTree
		if err == nil {
			priorGoal, err := readGoal(ctx, tx, previous.GoalID)
			if err != nil {
				return err
			}
			if priorGoal.State == domain.GoalRunning || priorGoal.State == domain.GoalVerifying || priorGoal.State == domain.GoalReady {
				return ErrCheckoutBusy
			}
			accepted := priorGoal.State == domain.GoalCompleted && previous.Identity == identity && previous.AcceptedTree == currentTree && previous.WorkID == ""
			if accepted {
				result.AcceptedCommit = previous.AcceptedCommit
				result.AcceptedTree = previous.AcceptedTree
			} else if !clean {
				return ErrCheckoutConflict
			}
		} else if !clean {
			return ErrCheckoutConflict
		}
		if err := saveCheckout(ctx, tx, result); err != nil {
			return err
		}
		event, err := prepareEvent(EventInput{Type: "CheckoutAdmitted", ActorType: "kernel", Payload: map[string]any{"tree": currentTree, "base_commit": result.AcceptedCommit}})
		if err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "goal", goalID, event)
	})
	return result, err
}
func (s *Store) ClaimCheckoutWork(ctx context.Context, goalID, workID string) error {
	return s.withTransaction(ctx, func(tx *sql.Tx) error { return claimCheckoutWork(ctx, tx, goalID, workID) })
}
func claimCheckoutWork(ctx context.Context, tx *sql.Tx, goalID, workID string) error {
	current, err := readCheckout(ctx, tx)
	if err != nil {
		return err
	}
	if current.GoalID != goalID || (current.WorkID != "" && (!current.RetryAuthorized || current.WorkID != workID)) {
		return ErrCheckoutBusy
	}
	work, err := readWorkItem(ctx, tx, workID)
	if err != nil {
		return err
	}
	owner, err := workGoalID(ctx, tx, work)
	if err != nil {
		return err
	}
	if owner != goalID {
		return ErrCheckoutConflict
	}
	if err := checkoutIdle(ctx, tx); err != nil {
		return err
	}
	current.WorkID = workID
	current.RetryAuthorized = false
	return saveCheckout(ctx, tx, current)
}

// ObserveCheckoutFailure only records a scene already checked for identity and scope by the Engine.
func (s *Store) ObserveCheckoutFailure(ctx context.Context, goalID, workID string, identity gitrepo.CheckoutIdentity, tree string) error {
	if identity.Validate() != nil || !artifactObjectID(tree) {
		return ErrCheckoutConflict
	}
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		current, err := readCheckout(ctx, tx)
		if err != nil {
			return err
		}
		if current.GoalID != goalID || current.WorkID != workID || current.Identity != identity {
			return ErrCheckoutConflict
		}
		current.ObservedTree = tree
		current.RetryAuthorized = false
		if err := saveCheckout(ctx, tx, current); err != nil {
			return err
		}
		event, err := prepareEvent(EventInput{Type: "CheckoutFailureObserved", ActorType: "kernel", Payload: map[string]any{"work_item_id": workID, "tree": tree}})
		if err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "goal", goalID, event)
	})
}

func workGoalID(ctx context.Context, q rowQueryer, work domain.WorkItem) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT revision.goal_id FROM plan_revisions plan JOIN goal_revisions revision ON revision.id=plan.goal_revision_id WHERE plan.id=?`, work.PlanRevisionID).Scan(&id)
	return id, err
}

func (s *Store) RetryCheckoutWork(ctx context.Context, workID string, expectedVersion int64, identity gitrepo.CheckoutIdentity, tree string, event EventInput) (domain.WorkItem, error) {
	prepared, err := prepareEvent(event)
	if err != nil {
		return domain.WorkItem{}, err
	}
	if identity.Validate() != nil || !artifactObjectID(tree) {
		return domain.WorkItem{}, ErrCheckoutConflict
	}
	var result domain.WorkItem
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		work, err := readWorkItem(ctx, tx, workID)
		if err != nil {
			return err
		}
		if work.Version != expectedVersion || (work.State != domain.WorkReady && !work.State.CanTransition(domain.WorkReady)) {
			return basestore.ErrConflict
		}
		goalID, err := workGoalID(ctx, tx, work)
		if err != nil {
			return err
		}
		goal, err := readGoal(ctx, tx, goalID)
		if err != nil {
			return err
		}
		if goal.State == domain.GoalCancelled || goal.State == domain.GoalCompleted {
			return basestore.ErrConflict
		}
		plan, err := readPlanRevision(ctx, tx, work.PlanRevisionID)
		if err != nil {
			return err
		}
		if plan.Status != domain.PlanActive || plan.GoalRevisionID != goal.ActiveRevisionID {
			return fmt.Errorf("retry work is outside the active plan: %w", basestore.ErrConflict)
		}
		current, err := readCheckout(ctx, tx)
		if err != nil {
			return err
		}
		if current.GoalID != goalID || (current.WorkID != workID && (current.WorkID != "" || current.AcceptedTree != tree)) || current.Identity != identity || current.ObservedTree != tree {
			return ErrCheckoutConflict
		}
		if err := checkoutIdle(ctx, tx); err != nil {
			return err
		}
		now := s.source.Now().UTC().Format(time.RFC3339Nano)
		// Explicit retry resolves only the checkout retry decision, never another permission Gate.
		rows, err := tx.QueryContext(ctx, `SELECT id FROM gates WHERE goal_id=? AND work_item_id=? AND reason_code='checkout_retry_required' AND action='EXEC_COMMAND' AND state='OPEN'`, goalID, workID)
		if err != nil {
			return err
		}
		var gateIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			gateIDs = append(gateIDs, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range gateIDs {
			_, err = tx.ExecContext(ctx, `UPDATE gates SET state='APPROVED',decision='ALLOW',decided_by='local-user',decision_reason='explicit checkout work retry',decided_at=?,used=max_uses,version=version+1,updated_at=? WHERE id=? AND state='OPEN'`, now, now, id)
			if err != nil {
				return err
			}
			gateEvent, err := prepareEvent(EventInput{Type: "CheckoutRetryDecided", ActorType: "human", Payload: map[string]any{"work_item_id": workID, "reason": event.Payload}})
			if err != nil {
				return err
			}
			if err := s.appendEvent(ctx, tx, "gate", id, gateEvent); err != nil {
				return err
			}
		}
		var blocking int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gates WHERE goal_id=? AND required=1 AND state='OPEN'`, goalID).Scan(&blocking); err != nil {
			return err
		}
		if blocking != 0 {
			return fmt.Errorf("other required gates must be resolved before retry: %w", basestore.ErrAuthorizationDenied)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE work_items SET state='READY',version=version+1,updated_at=? WHERE id=? AND version=?`, now, workID, expectedVersion); err != nil {
			return err
		}
		if goal.State == domain.GoalWaiting {
			if _, err := tx.ExecContext(ctx, `UPDATE goals SET state='RUNNING',version=version+1,updated_at=? WHERE id=? AND version=?`, now, goalID, goal.Version); err != nil {
				return err
			}
			if err := s.appendEvent(ctx, tx, "goal", goalID, prepared); err != nil {
				return err
			}
		}
		current.WorkID = workID
		current.RetryAuthorized = true
		if err := saveCheckout(ctx, tx, current); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "work", workID, prepared); err != nil {
			return err
		}
		work.State = domain.WorkReady
		work.Version++
		result = work
		return nil
	})
	return result, err
}
