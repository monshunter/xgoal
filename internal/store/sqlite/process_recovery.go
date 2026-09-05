package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
)

// ReconcileProcessAttempts runs at startup after every registered process has
// been inspected. Pending Promotions retain their lease for Git CAS readback.
func (s *Store) ReconcileProcessAttempts(ctx context.Context) error {
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT l.id FROM leases l JOIN attempts a ON a.id=l.attempt_id JOIN work_items w ON w.id=l.work_item_id JOIN plan_revisions p ON p.id=w.plan_revision_id JOIN goal_revisions r ON r.id=p.goal_revision_id JOIN goals g ON g.id=r.goal_id WHERE g.execution_model='current-directory' AND (l.state='ACTIVE' OR (w.state IN ('CLAIMED','RUNNING','RECONCILING') AND NOT EXISTS (SELECT 1 FROM leases newer WHERE newer.work_item_id=l.work_item_id AND newer.generation>l.generation))) ORDER BY l.id`)
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
			lease, err := readLease(ctx, tx, id)
			if err != nil {
				return err
			}
			var pending int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM promotions WHERE attempt_id=? AND state NOT IN ('OBSERVED','FAILED')`, lease.AttemptID).Scan(&pending); err != nil {
				return err
			}
			if pending != 0 {
				continue
			}
			attempt, err := readAttempt(ctx, tx, lease.AttemptID)
			if err != nil {
				return err
			}
			work, err := readWorkItem(ctx, tx, lease.WorkItemID)
			if err != nil {
				return err
			}
			goalID, err := workGoalID(ctx, tx, work)
			if err != nil {
				return err
			}
			goal, err := readGoal(ctx, tx, goalID)
			if err != nil {
				return err
			}
			var journalVersion, unconfirmed int
			if err := tx.QueryRowContext(ctx, `SELECT process_journal_version FROM attempts WHERE id=?`, attempt.ID).Scan(&journalVersion); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM process_invocations WHERE owner_kind='attempt' AND owner_id=? AND state IN ('INTENT','REGISTERED','UNKNOWN')`, attempt.ID).Scan(&unconfirmed); err != nil {
				return err
			}
			confirmed := journalVersion == 1 && unconfirmed == 0
			now := s.source.Now().UTC().Format(time.RFC3339Nano)
			changed := false
			if confirmed {
				if _, err := tx.ExecContext(ctx, `UPDATE leases SET state='REVOKED',version=version+1 WHERE id=? AND state='ACTIVE'`, id); err != nil {
					return err
				}
				changed = lease.State == domain.LeaseActive
				if attempt.State.CanTransition(domain.AttemptInterrupted) {
					if _, err := tx.ExecContext(ctx, `UPDATE attempts SET state='INTERRUPTED',version=version+1,updated_at=? WHERE id=?`, now, attempt.ID); err != nil {
						return err
					}
					changed = true
				}
				if work.State.CanTransition(domain.WorkReconciling) {
					if _, err := tx.ExecContext(ctx, `UPDATE work_items SET state='RECONCILING',version=version+1,updated_at=? WHERE id=?`, now, work.ID); err != nil {
						return err
					}
					changed = true
				}
			}
			if goal.State.CanTransition(domain.GoalWaiting) {
				if _, err := tx.ExecContext(ctx, `UPDATE goals SET state='WAITING',version=version+1,updated_at=? WHERE id=?`, now, goal.ID); err != nil {
					return err
				}
			}
			if err := s.executionRecoveryGateTx(ctx, tx, goal, work, attempt, confirmed); err != nil {
				return err
			}
			if confirmed && changed {
				if err := s.planningEvent(ctx, tx, "attempt", attempt.ID, "ExecutionInterrupted", map[string]any{"lease_id": lease.ID, "processes_stopped": true, "files_preserved": true}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *Store) executionRecoveryGateTx(ctx context.Context, tx *sql.Tx, goal domain.Goal, work domain.WorkItem, attempt domain.Attempt, confirmed bool) error {
	if goal.State == domain.GoalCancelled || goal.State == domain.GoalCompleted {
		return nil
	}
	id := "execution_recovery_" + attempt.ID
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM gates WHERE id=?`, id).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		if confirmed {
			result, err := tx.ExecContext(ctx, `UPDATE gates SET reason_code='checkout_retry_required',facts_json=CAST(json_set(facts_json,'$.processes_stopped',json('true')) AS BLOB),recommendation='Process termination is now confirmed. Review preserved files and restore the last recorded checkout scene before work retry, or cancel.',version=version+1,updated_at=? WHERE id=? AND state='OPEN' AND reason_code='execution_process_unconfirmed'`, s.source.Now().UTC().Format(time.RFC3339Nano), id)
			if err != nil {
				return err
			}
			if changed, err := result.RowsAffected(); err != nil {
				return err
			} else if changed != 0 {
				return s.planningEvent(ctx, tx, "goal", goal.ID, "ExecutionRecoveryConfirmed", map[string]any{"gate_id": id, "processes_stopped": true})
			}
		}
		return nil
	}
	code := "execution_process_unconfirmed"
	recommendation := "Process ownership cannot be confirmed. Inspect the recorded processes before any new execution. Files and lease are preserved."
	if confirmed {
		code = "checkout_retry_required"
		recommendation = "The previous daemon stopped. Review preserved files; restore the last recorded checkout scene before work retry, or cancel and create a new Goal from a reviewed clean checkout."
	}
	facts, err := canonical.Marshal(map[string]any{"owner": "execution-recovery", "attempt_id": attempt.ID, "processes_stopped": confirmed, "files_preserved": true})
	if err != nil {
		return err
	}
	scope, err := canonical.Marshal([]string{"work:" + work.ID})
	if err != nil {
		return err
	}
	now := s.source.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO gates(id,goal_id,work_item_id,attempt_id,reason_code,state,facts_json,unknowns_json,options_json,recommendation,action,scope_json,expires_at,max_uses,used,revocable,required,version,created_at,updated_at) VALUES (?,?,?,?,?,'OPEN',?,?,?,?,'EXEC_COMMAND',?,?,1,0,1,1,1,?,?)`, id, goal.ID, work.ID, attempt.ID, code, facts, []byte(`[]`), []byte(`["inspect","restore recorded scene and retry","cancel"]`), recommendation, scope, now.Add(24*time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return s.planningEvent(ctx, tx, "goal", goal.ID, "ExecutionRecoveryWaiting", map[string]any{"gate_id": id, "work_item_id": work.ID, "processes_stopped": confirmed})
}

func (s *Store) ExecutionRecoveryBlocker(ctx context.Context) (string, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM process_invocations WHERE state='UNKNOWN'`).Scan(&count); err != nil {
		return "", err
	}
	if count != 0 {
		return "PROCESS_UNCONFIRMED: inspect persisted process ownership before new execution", nil
	}
	return "", nil
}
