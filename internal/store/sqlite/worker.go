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

type WorkerState string

const (
	WorkerRunning    WorkerState = "RUNNING"
	WorkerObserving  WorkerState = "OBSERVING"
	WorkerTerminated WorkerState = "TERMINATED"
	WorkerExited     WorkerState = "EXITED"
	WorkerLost       WorkerState = "LOST"
)

func (state WorkerState) terminal() bool {
	return state == WorkerTerminated || state == WorkerExited || state == WorkerLost
}

type WorkerProcess struct {
	AttemptID     string
	PID           int
	PGID          int
	StartIdentity string
	State         WorkerState
	Version       int64
	StartedAt     time.Time
	UpdatedAt     time.Time
}

func (s *Store) RecordWorker(ctx context.Context, worker WorkerProcess, event EventInput) (WorkerProcess, error) {
	if !validIdempotencyLabel(worker.AttemptID) || worker.PID <= 0 || worker.PGID <= 0 || !validIdempotencyLabel(worker.StartIdentity) || worker.State != WorkerRunning || worker.Version != 1 {
		return WorkerProcess{}, errors.New("invalid initial worker process")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return WorkerProcess{}, err
	}
	now := s.source.Now().UTC()
	worker.StartedAt, worker.UpdatedAt = now, now
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		if _, err := readAttempt(ctx, tx, worker.AttemptID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_processes(attempt_id, pid, pgid, start_identity, state, version, started_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, ?, ?)`, worker.AttemptID, worker.PID, worker.PGID, worker.StartIdentity, worker.State, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("insert worker for attempt %q: %w", worker.AttemptID, err)
		}
		return s.appendEvent(ctx, tx, "attempt", worker.AttemptID, prepared)
	})
	return worker, err
}

func (s *Store) RecoverableWorkers(ctx context.Context) ([]WorkerProcess, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT attempt_id, pid, pgid, start_identity, state, version, started_at, updated_at FROM worker_processes WHERE state IN (?, ?) ORDER BY attempt_id`, WorkerRunning, WorkerObserving)
	if err != nil {
		return nil, fmt.Errorf("list recoverable workers: %w", err)
	}
	defer rows.Close()
	var result []WorkerProcess
	for rows.Next() {
		worker, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, worker)
	}
	return result, rows.Err()
}

// MarkWorkerExited records the normal child-process observation without
// changing Attempt, Work, or Lease ownership; the engine performs those state
// transitions only after it has parsed the immutable result.
func (s *Store) MarkWorkerExited(ctx context.Context, attemptID string, expectedVersion int64, event EventInput) (WorkerProcess, error) {
	if !validIdempotencyLabel(attemptID) || expectedVersion <= 0 {
		return WorkerProcess{}, errors.New("invalid worker exit observation")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return WorkerProcess{}, err
	}
	var result WorkerProcess
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		worker, err := readWorker(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if worker.Version != expectedVersion || worker.State != WorkerRunning {
			return fmt.Errorf("worker %q: %w", attemptID, basestore.ErrConflict)
		}
		now := s.source.Now().UTC()
		updated, err := tx.ExecContext(ctx, `UPDATE worker_processes SET state = ?, version = version + 1, updated_at = ? WHERE attempt_id = ? AND version = ? AND state = ?`, WorkerExited, now.Format(time.RFC3339Nano), attemptID, expectedVersion, WorkerRunning)
		if err != nil {
			return err
		}
		affected, err := updated.RowsAffected()
		if err != nil || affected != 1 {
			return fmt.Errorf("worker %q exit CAS: %w", attemptID, basestore.ErrConflict)
		}
		if err := s.appendEvent(ctx, tx, "attempt", attemptID, prepared); err != nil {
			return err
		}
		worker.State = WorkerExited
		worker.Version++
		worker.UpdatedAt = now
		result = worker
		return nil
	})
	return result, err
}

// ResolveWorkerRecovery terminates ownership and moves unfinished work to deterministic Reconcile.
func (s *Store) ResolveWorkerRecovery(ctx context.Context, attemptID string, expectedVersion int64, state WorkerState, reason string, event EventInput) (WorkerProcess, error) {
	if !validIdempotencyLabel(attemptID) || expectedVersion <= 0 || !state.terminal() || reason == "" {
		return WorkerProcess{}, errors.New("invalid worker recovery resolution")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return WorkerProcess{}, err
	}
	var result WorkerProcess
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		worker, err := readWorker(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if worker.Version != expectedVersion || worker.State.terminal() {
			return fmt.Errorf("worker %q: %w", attemptID, basestore.ErrConflict)
		}
		attempt, err := readAttempt(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		now := s.source.Now().UTC().Format(time.RFC3339Nano)
		updated, err := tx.ExecContext(ctx, `UPDATE worker_processes SET state = ?, version = version + 1, updated_at = ? WHERE attempt_id = ? AND version = ? AND state IN (?, ?)`, state, now, attemptID, expectedVersion, WorkerRunning, WorkerObserving)
		if err != nil {
			return fmt.Errorf("resolve worker %q: %w", attemptID, err)
		}
		affected, _ := updated.RowsAffected()
		if affected != 1 {
			return fmt.Errorf("worker %q: %w", attemptID, basestore.ErrConflict)
		}
		// A journaled promotion owns the remaining lifecycle transition. The
		// worker is stopped, but revoking its lease would make ref readback
		// impossible after a crash between Git CAS and SQLite observation.
		var pendingPromotion int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM promotions WHERE attempt_id=? AND state NOT IN ('OBSERVED','FAILED')`, attemptID).Scan(&pendingPromotion); err != nil {
			return err
		}
		if pendingPromotion > 0 {
			if err := s.appendEvent(ctx, tx, "attempt", attemptID, prepared); err != nil {
				return err
			}
			worker.State, worker.Version = state, worker.Version+1
			worker.UpdatedAt, _ = time.Parse(time.RFC3339Nano, now)
			result = worker
			return nil
		}
		if attempt.State.CanTransition(domain.AttemptInterrupted) {
			if _, err := tx.ExecContext(ctx, `UPDATE attempts SET state = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ?`, domain.AttemptInterrupted, now, attempt.ID, attempt.Version); err != nil {
				return fmt.Errorf("interrupt recovered attempt: %w", err)
			}
		}
		var leaseID string
		var leaseVersion int64
		if err := tx.QueryRowContext(ctx, `SELECT id, version FROM leases WHERE attempt_id = ? AND state = ?`, attemptID, domain.LeaseActive).Scan(&leaseID, &leaseVersion); err == nil {
			if _, err := tx.ExecContext(ctx, `UPDATE leases SET state = ?, version = version + 1 WHERE id = ? AND version = ?`, domain.LeaseRevoked, leaseID, leaseVersion); err != nil {
				return fmt.Errorf("revoke recovered worker lease: %w", err)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		work, err := readWorkItem(ctx, tx, attempt.WorkItemID)
		if err != nil {
			return err
		}
		if work.State.CanTransition(domain.WorkReconciling) {
			if _, err := tx.ExecContext(ctx, `UPDATE work_items SET state = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ?`, domain.WorkReconciling, now, work.ID, work.Version); err != nil {
				return fmt.Errorf("reconcile recovered work: %w", err)
			}
		}
		if err := s.appendEvent(ctx, tx, "attempt", attemptID, prepared); err != nil {
			return err
		}
		worker.State, worker.Version = state, worker.Version+1
		worker.UpdatedAt, _ = time.Parse(time.RFC3339Nano, now)
		result = worker
		return nil
	})
	return result, err
}

func readWorker(ctx context.Context, queryer rowQueryer, attemptID string) (WorkerProcess, error) {
	worker, err := scanWorker(queryer.QueryRowContext(ctx, `SELECT attempt_id, pid, pgid, start_identity, state, version, started_at, updated_at FROM worker_processes WHERE attempt_id = ?`, attemptID))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkerProcess{}, fmt.Errorf("worker %q: %w", attemptID, basestore.ErrNotFound)
	}
	return worker, err
}

type workerScanner interface{ Scan(...any) error }

func scanWorker(scanner workerScanner) (WorkerProcess, error) {
	var worker WorkerProcess
	var startedAt, updatedAt string
	if err := scanner.Scan(&worker.AttemptID, &worker.PID, &worker.PGID, &worker.StartIdentity, &worker.State, &worker.Version, &startedAt, &updatedAt); err != nil {
		return WorkerProcess{}, err
	}
	var err error
	worker.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return WorkerProcess{}, err
	}
	worker.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return WorkerProcess{}, err
	}
	return worker, nil
}
