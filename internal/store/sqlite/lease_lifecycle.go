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

// ReleaseLease closes the current live generation after its worker has stopped writing.
func (s *Store) ReleaseLease(
	ctx context.Context,
	id string,
	generation, expectedVersion int64,
	event EventInput,
) (domain.Lease, error) {
	if id == "" || generation <= 0 || expectedVersion <= 0 {
		return domain.Lease{}, errors.New("invalid lease release")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return domain.Lease{}, err
	}
	var result domain.Lease
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		lease, err := readLease(ctx, tx, id)
		if err != nil {
			return err
		}
		if lease.Version != expectedVersion {
			return fmt.Errorf("lease %q: %w", id, basestore.ErrConflict)
		}
		if lease.Generation != generation || lease.State != domain.LeaseActive {
			return fmt.Errorf("lease %q generation %d: %w", id, generation, basestore.ErrStaleLease)
		}
		if !s.source.Now().UTC().Before(lease.ExpiresAt) {
			return fmt.Errorf("lease %q: %w", id, basestore.ErrExpired)
		}
		if err := domain.ValidateLeaseTransition(lease.State, domain.LeaseReleased); err != nil {
			return err
		}
		updated, err := tx.ExecContext(ctx, `
UPDATE leases
SET state = ?, version = version + 1
WHERE id = ? AND generation = ? AND version = ? AND state = ?`,
			domain.LeaseReleased,
			id,
			generation,
			expectedVersion,
			domain.LeaseActive,
		)
		if err != nil {
			return fmt.Errorf("release lease %q: %w", id, err)
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return fmt.Errorf("read lease release result: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("lease %q: %w", id, basestore.ErrConflict)
		}
		if err := s.appendEvent(ctx, tx, "lease", id, prepared); err != nil {
			return err
		}
		lease.State = domain.LeaseReleased
		lease.Version++
		result = lease
		return nil
	})
	if err != nil {
		return domain.Lease{}, err
	}
	return result, nil
}

// HeartbeatLease extends one still-live active generation using optimistic CAS.
func (s *Store) HeartbeatLease(
	ctx context.Context,
	id string,
	generation, expectedVersion int64,
	ttl time.Duration,
	event EventInput,
) (domain.Lease, error) {
	if id == "" || generation <= 0 || expectedVersion <= 0 || ttl <= 0 {
		return domain.Lease{}, errors.New("invalid lease heartbeat")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return domain.Lease{}, err
	}
	var result domain.Lease
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		lease, err := readLease(ctx, tx, id)
		if err != nil {
			return err
		}
		if lease.Version != expectedVersion {
			return fmt.Errorf("lease %q: %w", id, basestore.ErrConflict)
		}
		if lease.Generation != generation || lease.State != domain.LeaseActive {
			return fmt.Errorf("lease %q generation %d: %w", id, generation, basestore.ErrStaleLease)
		}
		now := s.source.Now().UTC()
		if !now.Before(lease.ExpiresAt) {
			return fmt.Errorf("lease %q: %w", id, basestore.ErrExpired)
		}
		updated, err := tx.ExecContext(ctx, `
UPDATE leases
SET heartbeat_at = ?, expires_at = ?, version = version + 1
WHERE id = ? AND generation = ? AND version = ? AND state = ?`,
			now.Format(time.RFC3339Nano),
			now.Add(ttl).Format(time.RFC3339Nano),
			id,
			generation,
			expectedVersion,
			domain.LeaseActive,
		)
		if err != nil {
			return fmt.Errorf("heartbeat lease %q: %w", id, err)
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return fmt.Errorf("read lease heartbeat result: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("lease %q: %w", id, basestore.ErrConflict)
		}
		if err := s.appendEvent(ctx, tx, "lease", id, prepared); err != nil {
			return err
		}
		lease.HeartbeatAt = now
		lease.ExpiresAt = now.Add(ttl)
		lease.Version++
		result = lease
		return nil
	})
	if err != nil {
		return domain.Lease{}, err
	}
	return result, nil
}

// ExpiredLeases reports active leases whose TTL has elapsed without changing ownership.
func (s *Store) ExpiredLeases(ctx context.Context) ([]domain.Lease, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, work_item_id, attempt_id, holder, generation, state,
       acquired_at, heartbeat_at, expires_at, version
FROM leases
WHERE state = ?
ORDER BY id`, domain.LeaseActive)
	if err != nil {
		return nil, fmt.Errorf("read expired leases: %w", err)
	}
	defer rows.Close()
	var leases []domain.Lease
	now := s.source.Now().UTC()
	for rows.Next() {
		lease, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		if !now.Before(lease.ExpiresAt) {
			leases = append(leases, lease)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired leases: %w", err)
	}
	return leases, nil
}

// ResolveExpiredLease closes ownership only after external read-back confirms the worker stopped.
func (s *Store) ResolveExpiredLease(
	ctx context.Context,
	id string,
	generation, expectedVersion int64,
	workerStopped bool,
	event EventInput,
) (domain.Lease, error) {
	if id == "" || generation <= 0 || expectedVersion <= 0 {
		return domain.Lease{}, errors.New("invalid expired lease resolution")
	}
	if !workerStopped {
		return domain.Lease{}, errors.New("expired lease cannot be resolved until external read-back confirms the worker stopped")
	}
	preparedLeaseEvent, err := prepareEvent(event)
	if err != nil {
		return domain.Lease{}, err
	}
	preparedAttemptEvent, err := prepareEvent(EventInput{
		Type:          "AttemptInterrupted",
		ActorType:     event.ActorType,
		ActorID:       event.ActorID,
		CorrelationID: event.CorrelationID,
		Payload:       map[string]any{"lease_id": id, "generation": generation},
	})
	if err != nil {
		return domain.Lease{}, err
	}
	preparedWorkEvent, err := prepareEvent(EventInput{
		Type:          "WorkReconciling",
		ActorType:     event.ActorType,
		ActorID:       event.ActorID,
		CorrelationID: event.CorrelationID,
		Payload:       map[string]any{"lease_id": id, "worker_stopped": true},
	})
	if err != nil {
		return domain.Lease{}, err
	}
	var result domain.Lease
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		lease, err := readLease(ctx, tx, id)
		if err != nil {
			return err
		}
		if lease.Version != expectedVersion {
			return fmt.Errorf("lease %q: %w", id, basestore.ErrConflict)
		}
		if lease.Generation != generation || lease.State != domain.LeaseActive {
			return fmt.Errorf("lease %q generation %d: %w", id, generation, basestore.ErrStaleLease)
		}
		now := s.source.Now().UTC()
		if now.Before(lease.ExpiresAt) {
			return fmt.Errorf("lease %q has not expired", id)
		}
		attempt, err := readAttempt(ctx, tx, lease.AttemptID)
		if err != nil {
			return err
		}
		work, err := readWorkItem(ctx, tx, lease.WorkItemID)
		if err != nil {
			return err
		}
		if err := domain.ValidateLeaseTransition(lease.State, domain.LeaseExpired); err != nil {
			return err
		}
		if err := domain.ValidateAttemptTransition(attempt.State, domain.AttemptInterrupted); err != nil {
			return err
		}
		if err := domain.ValidateWorkTransition(work.State, domain.WorkReconciling); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE leases
SET state = ?, version = version + 1
WHERE id = ? AND generation = ? AND version = ? AND state = ?`,
			domain.LeaseExpired,
			id,
			generation,
			expectedVersion,
			domain.LeaseActive,
		); err != nil {
			return fmt.Errorf("expire lease %q: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE attempts
SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`,
			domain.AttemptInterrupted,
			now.Format(time.RFC3339Nano),
			attempt.ID,
			attempt.Version,
		); err != nil {
			return fmt.Errorf("interrupt attempt %q: %w", attempt.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE work_items
SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`,
			domain.WorkReconciling,
			now.Format(time.RFC3339Nano),
			work.ID,
			work.Version,
		); err != nil {
			return fmt.Errorf("reconcile work item %q: %w", work.ID, err)
		}
		if err := s.appendEvent(ctx, tx, "lease", lease.ID, preparedLeaseEvent); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "attempt", attempt.ID, preparedAttemptEvent); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "work", work.ID, preparedWorkEvent); err != nil {
			return err
		}
		lease.State = domain.LeaseExpired
		lease.Version++
		result = lease
		return nil
	})
	if err != nil {
		return domain.Lease{}, err
	}
	return result, nil
}

// UpdateAttemptStateWithLease rejects writes from expired, replaced, or mismatched workers.
func (s *Store) UpdateAttemptStateWithLease(
	ctx context.Context,
	attemptID string,
	expectedAttemptVersion int64,
	leaseID string,
	generation int64,
	state domain.AttemptState,
	event EventInput,
) error {
	if attemptID == "" || expectedAttemptVersion <= 0 || leaseID == "" || generation <= 0 || !state.Valid() {
		return errors.New("invalid lease-bound attempt update")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return err
	}
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		lease, err := readLease(ctx, tx, leaseID)
		if err != nil {
			return err
		}
		if lease.Generation != generation || lease.AttemptID != attemptID || lease.State != domain.LeaseActive {
			return fmt.Errorf("lease %q generation %d: %w", leaseID, generation, basestore.ErrStaleLease)
		}
		if !s.source.Now().UTC().Before(lease.ExpiresAt) {
			return fmt.Errorf("lease %q: %w", leaseID, basestore.ErrExpired)
		}
		attempt, err := readAttempt(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if attempt.Version != expectedAttemptVersion {
			return fmt.Errorf("attempt %q: %w", attemptID, basestore.ErrConflict)
		}
		if err := domain.ValidateAttemptTransition(attempt.State, state); err != nil {
			return err
		}
		updated, err := tx.ExecContext(ctx, `
UPDATE attempts
SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`, state, s.source.Now().UTC().Format(time.RFC3339Nano), attemptID, expectedAttemptVersion)
		if err != nil {
			return fmt.Errorf("update lease-bound attempt %q: %w", attemptID, err)
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return fmt.Errorf("read lease-bound attempt update result: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("attempt %q: %w", attemptID, basestore.ErrConflict)
		}
		return s.appendEvent(ctx, tx, "attempt", attemptID, prepared)
	})
}

func scanLease(row sqlRows) (domain.Lease, error) {
	var lease domain.Lease
	var acquiredAt, heartbeatAt, expiresAt string
	if err := row.Scan(
		&lease.ID,
		&lease.WorkItemID,
		&lease.AttemptID,
		&lease.Holder,
		&lease.Generation,
		&lease.State,
		&acquiredAt,
		&heartbeatAt,
		&expiresAt,
		&lease.Version,
	); err != nil {
		return domain.Lease{}, err
	}
	if !lease.State.Valid() || lease.Generation <= 0 || lease.Version <= 0 {
		return domain.Lease{}, fmt.Errorf("lease %q contains invalid persisted state", lease.ID)
	}
	var err error
	lease.AcquiredAt, err = parseStoredTime("lease acquired_at", acquiredAt)
	if err != nil {
		return domain.Lease{}, err
	}
	lease.HeartbeatAt, err = parseStoredTime("lease heartbeat_at", heartbeatAt)
	if err != nil {
		return domain.Lease{}, err
	}
	lease.ExpiresAt, err = parseStoredTime("lease expires_at", expiresAt)
	if err != nil {
		return domain.Lease{}, err
	}
	return lease, nil
}
