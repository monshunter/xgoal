package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

// ReconcileLegacyExecution runs after old process recovery. Historical files,
// evidence, refs and unfinished effects are retained, never executed as current-directory work.
func (s *Store) ReconcileLegacyExecution(ctx context.Context) error {
	// Only recorded terminal process observations justify releasing old leases.
	// Unknown ownership remains visible and blocks new execution for inspection.
	if err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT lease.id FROM leases lease JOIN worker_processes worker ON worker.attempt_id=lease.attempt_id JOIN work_items work ON work.id=lease.work_item_id JOIN plan_revisions plan ON plan.id=work.plan_revision_id JOIN goal_revisions revision ON revision.id=plan.goal_revision_id JOIN goals goal ON goal.id=revision.goal_id WHERE goal.execution_model='git-worktree' AND lease.state='ACTIVE' AND worker.state IN ('EXITED','TERMINATED')`)
		if err != nil {
			return err
		}
		var leaseIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			leaseIDs = append(leaseIDs, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range leaseIDs {
			if _, err := tx.ExecContext(ctx, `UPDATE leases SET state='REVOKED',version=version+1 WHERE id=? AND state='ACTIVE'`, id); err != nil {
				return err
			}
			event, err := prepareEvent(EventInput{Type: "LegacyLeaseRetired", ActorType: "kernel", Payload: map[string]any{"reason": "legacy execution disabled; recorded worker stopped"}})
			if err != nil {
				return err
			}
			if err := s.appendEvent(ctx, tx, "lease", id, event); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM goals WHERE execution_model='git-worktree' AND state NOT IN ('COMPLETED','CANCELLED') ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		event := EventInput{Type: "ExecutionMigrationRequired", ActorType: "kernel", Payload: map[string]any{"execution_model": "git-worktree", "reason": "retain historical files; create a new Goal from a reviewed clean checkout"}}
		gateID := "execution_migration_" + id
		if _, err := s.Gate(ctx, gateID); errors.Is(err, basestore.ErrNotFound) {
			_, err = s.CreateGate(ctx, GateDraft{ID: gateID, GoalID: id, ReasonCode: "execution_migration_required", Facts: map[string]any{"execution_model": "git-worktree", "files_preserved": true}, Unknowns: []string{"whether the historical result should be reviewed and committed by its owner"}, Options: []string{"inspect historical report and files", "cancel historical goal and create a new goal"}, Recommendation: "Preserve history; review files and create a new Goal from a clean main checkout", Action: domain.ActionReadFile, Scope: []string{"goal:" + id}, ExpiresAt: s.source.Now().Add(365 * 24 * time.Hour), MaxUses: 1, Revocable: true, Required: true}, event)
			if err != nil && !errors.Is(err, basestore.ErrAlreadyExists) {
				return err
			}
		} else if err != nil {
			return err
		}
		goal, err := s.Goal(ctx, id)
		if err != nil {
			return err
		}
		if goal.State.CanTransition(domain.GoalWaiting) {
			if err := s.UpdateGoalState(ctx, id, goal.Version, domain.GoalWaiting, event); err != nil {
				return err
			}
		}
	}
	return nil
}
