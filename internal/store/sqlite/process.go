package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/supervisor"
)

func (s *Store) CheckExecutionAvailable(ctx context.Context) error {
	return s.withTransaction(ctx, func(tx *sql.Tx) error { return executionIdle(ctx, tx) })
}

func executionIdle(ctx context.Context, tx *sql.Tx) error {
	return processSlotAvailable(ctx, tx, supervisor.Owner{})
}

func processSlotAvailable(ctx context.Context, q rowQueryer, owner supervisor.Owner) error {
	attemptID, effectID := "", ""
	if owner.Kind == "attempt" {
		attemptID = owner.ID
	}
	if owner.Kind == "planning" {
		effectID = owner.ID
	}
	queries := []struct {
		table, statement string
		args             []any
	}{
		{"leases", `SELECT COUNT(*) FROM leases WHERE state='ACTIVE' AND attempt_id<>?`, []any{attemptID}},
		{"worker_processes", `SELECT COUNT(*) FROM worker_processes WHERE state IN ('RUNNING','OBSERVING','LOST') AND attempt_id<>?`, []any{attemptID}},
		{"process_invocations", `SELECT COUNT(*) FROM process_invocations WHERE state IN ('INTENT','REGISTERED','UNKNOWN') AND (state='UNKNOWN' OR owner_kind<>? OR owner_id<>? OR generation<>?)`, []any{owner.Kind, owner.ID, owner.Generation}},
		{"effects", `SELECT COUNT(*) FROM effects WHERE effect_type='planner' AND state IN ('EXECUTING','OBSERVING','RECOVERING') AND id<>?`, []any{effectID}},
	}
	var version int
	if err := q.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 8 {
		queries = append(queries, struct {
			table, statement string
			args             []any
		}{"promotions", `SELECT COUNT(*) FROM promotions p JOIN goals g ON g.id=p.goal_id WHERE g.execution_model='current-directory' AND p.state NOT IN ('OBSERVED','FAILED')`, nil})
	}
	for _, query := range queries {
		var exists int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, query.table).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		var count int
		if err := q.QueryRowContext(ctx, query.statement, query.args...).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%w: unresolved %s ownership", ErrCheckoutBusy, query.table)
		}
	}
	return nil
}

func (s *Store) BeginProcess(ctx context.Context, intent supervisor.ProcessIntent) error {
	if !validIdempotencyLabel(intent.ID) || intent.Owner.Validate() != nil {
		return errors.New("invalid process intent")
	}
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		existing, err := readProcess(ctx, tx, intent.ID)
		if err == nil {
			if existing.ProcessIntent == intent {
				return nil
			}
			return basestore.ErrConflict
		}
		if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		if err := validateProcessOwner(ctx, tx, intent.Owner); err != nil {
			return err
		}
		if err := processSlotAvailable(ctx, tx, intent.Owner); err != nil {
			return err
		}
		now := s.source.Now().UTC().Format(time.RFC3339Nano)
		_, err = tx.ExecContext(ctx, `INSERT INTO process_invocations(id,owner_kind,owner_id,goal_id,generation,state,created_at,updated_at) VALUES(?,?,?,?,?,'INTENT',?,?)`, intent.ID, intent.Owner.Kind, intent.Owner.ID, intent.Owner.GoalID, intent.Owner.Generation, now, now)
		return err
	})
}

func validateProcessOwner(ctx context.Context, q rowQueryer, owner supervisor.Owner) error {
	if owner.Kind == "probe" {
		return nil
	}
	var count int
	var err error
	if owner.Kind == "planning" {
		err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM effects e JOIN goals g ON g.planning_effect_id=e.id WHERE e.id=? AND g.id=? AND g.planning_generation=? AND g.state='DRAFT' AND g.active_revision_id='' AND g.planning_paused=0 AND e.effect_type='planner' AND e.state='EXECUTING'`, owner.ID, owner.GoalID, owner.Generation).Scan(&count)
	} else {
		err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts a JOIN leases l ON l.attempt_id=a.id JOIN work_items w ON w.id=a.work_item_id JOIN plan_revisions p ON p.id=w.plan_revision_id JOIN goal_revisions r ON r.id=p.goal_revision_id JOIN goals g ON g.id=r.goal_id WHERE a.id=? AND g.id=? AND l.generation=? AND g.active_revision_id=r.id AND p.status='ACTIVE' AND g.state IN ('RUNNING','VERIFYING') AND a.state IN ('CREATED','PREPARING','STARTING','RUNNING','COLLECTING','VALIDATING','REVIEWING','PROMOTING','SUCCEEDED')`, owner.ID, owner.GoalID, owner.Generation).Scan(&count)
	}
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("stale process owner: %w", basestore.ErrConflict)
	}
	return nil
}

func (s *Store) RegisterProcess(ctx context.Context, id string, identity supervisor.ProcessIdentity) error {
	if !validIdempotencyLabel(id) || identity.PID <= 0 || identity.PID != identity.PGID || !validIdempotencyLabel(identity.StartID) {
		return errors.New("invalid process identity")
	}
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		record, err := readProcess(ctx, tx, id)
		if err != nil {
			return err
		}
		if record.State == supervisor.ProcessRegistered && record.Identity == identity {
			return nil
		}
		if record.State != supervisor.ProcessIntentState {
			return basestore.ErrConflict
		}
		if err := validateProcessOwner(ctx, tx, record.Owner); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE process_invocations SET pid=?,pgid=?,start_identity=?,state='REGISTERED',updated_at=? WHERE id=? AND state='INTENT'`, identity.PID, identity.PGID, identity.StartID, s.source.Now().UTC().Format(time.RFC3339Nano), id)
		return err
	})
}

func (s *Store) FinishProcess(ctx context.Context, id string, state supervisor.ProcessState, reason string) error {
	if !validIdempotencyLabel(id) || (state != supervisor.ProcessExited && state != supervisor.ProcessTerminated && state != supervisor.ProcessUnknown) || reason == "" || len(reason) > 4096 {
		return errors.New("invalid process observation")
	}
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		record, err := readProcess(ctx, tx, id)
		if err != nil {
			return err
		}
		if record.State == state && record.Reason == reason {
			return nil
		}
		if record.State == supervisor.ProcessExited || record.State == supervisor.ProcessTerminated {
			return basestore.ErrConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE process_invocations SET state=?,reason=?,updated_at=? WHERE id=?`, state, reason, s.source.Now().UTC().Format(time.RFC3339Nano), id)
		return err
	})
}

func (s *Store) RecoverableProcesses(ctx context.Context) ([]supervisor.ProcessRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,owner_kind,owner_id,goal_id,generation,COALESCE(pid,0),COALESCE(pgid,0),COALESCE(start_identity,''),state,reason FROM process_invocations WHERE state IN ('INTENT','REGISTERED','UNKNOWN') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []supervisor.ProcessRecord
	for rows.Next() {
		record, err := scanProcess(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func readProcess(ctx context.Context, q rowQueryer, id string) (supervisor.ProcessRecord, error) {
	return scanProcess(q.QueryRowContext(ctx, `SELECT id,owner_kind,owner_id,goal_id,generation,COALESCE(pid,0),COALESCE(pgid,0),COALESCE(start_identity,''),state,reason FROM process_invocations WHERE id=?`, id))
}
func scanProcess(scanner interface{ Scan(...any) error }) (supervisor.ProcessRecord, error) {
	var record supervisor.ProcessRecord
	err := scanner.Scan(&record.ID, &record.Owner.Kind, &record.Owner.ID, &record.Owner.GoalID, &record.Owner.Generation, &record.Identity.PID, &record.Identity.PGID, &record.Identity.StartID, &record.State, &record.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		err = basestore.ErrNotFound
	}
	return record, err
}

var _ supervisor.Journal = (*Store)(nil)
