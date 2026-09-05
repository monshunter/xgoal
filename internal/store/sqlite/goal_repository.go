package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

// EventInput contains trusted event metadata. Payload is canonicalized before persistence.
type EventInput struct {
	Type          string
	ActorType     string
	ActorID       string
	CorrelationID string
	Payload       any
}

type preparedEvent struct {
	input   EventInput
	payload []byte
}

// CreateGoal persists the initial aggregate state and its first event atomically.
func (s *Store) CreateGoal(ctx context.Context, goal domain.Goal, event EventInput) error {
	if goal.ID == "" || goal.State != domain.GoalDraft || goal.ActiveRevisionID != "" ||
		goal.FinalTree != "" || goal.FinalEvidenceSetID != "" || goal.FinalReportHash != "" || goal.Version != 1 {
		return errors.New("invalid goal")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return err
	}
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM goals WHERE id = ?`, goal.ID).Scan(&exists); err != nil {
			return fmt.Errorf("check goal existence: %w", err)
		}
		if exists != 0 {
			return fmt.Errorf("goal %q: %w", goal.ID, basestore.ErrAlreadyExists)
		}
		now := s.source.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO goals(
    id, state, active_revision_id, final_tree, final_evidence_set_id,
    final_report_hash, version, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			goal.ID,
			goal.State,
			goal.ActiveRevisionID,
			goal.FinalTree,
			goal.FinalEvidenceSetID,
			goal.FinalReportHash,
			goal.Version,
			now,
			now,
		); err != nil {
			return fmt.Errorf("insert goal %q: %w", goal.ID, err)
		}
		if s.info.SchemaVersion >= 8 {
			if _, err := tx.ExecContext(ctx, `UPDATE goals SET execution_model = 'current-directory' WHERE id = ?`, goal.ID); err != nil {
				return err
			}
		}
		return s.appendEvent(ctx, tx, "goal", goal.ID, prepared)
	})
}

// Goal returns the current persisted aggregate state.
func (s *Store) Goal(ctx context.Context, id string) (domain.Goal, error) {
	if id == "" {
		return domain.Goal{}, errors.New("goal id is empty")
	}
	return readGoal(ctx, s.db, id)
}

func readGoal(ctx context.Context, queryer rowQueryer, id string) (domain.Goal, error) {
	var goal domain.Goal
	err := queryer.QueryRowContext(ctx, `
SELECT id, state, active_revision_id, final_tree, final_evidence_set_id,
       final_report_hash, version
FROM goals
WHERE id = ?`, id).Scan(
		&goal.ID,
		&goal.State,
		&goal.ActiveRevisionID,
		&goal.FinalTree,
		&goal.FinalEvidenceSetID,
		&goal.FinalReportHash,
		&goal.Version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Goal{}, fmt.Errorf("goal %q: %w", id, basestore.ErrNotFound)
	}
	if err != nil {
		return domain.Goal{}, fmt.Errorf("read goal %q: %w", id, err)
	}
	if !goal.State.Valid() || goal.Version <= 0 {
		return domain.Goal{}, fmt.Errorf("goal %q contains invalid persisted state", id)
	}
	return goal, nil
}

// UpdateGoalState applies a valid state transition with optimistic CAS and one event.
func (s *Store) UpdateGoalState(
	ctx context.Context,
	id string,
	expectedVersion int64,
	state domain.GoalState,
	event EventInput,
) error {
	if id == "" || expectedVersion <= 0 || !state.Valid() {
		return errors.New("invalid goal state update")
	}
	if state == domain.GoalCompleted {
		return errors.New("COMPLETED may only be written through CompleteGoal")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return err
	}
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		var currentState domain.GoalState
		var currentVersion int64
		err := tx.QueryRowContext(ctx, `SELECT state, version FROM goals WHERE id = ?`, id).Scan(&currentState, &currentVersion)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("goal %q: %w", id, basestore.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("read goal %q for update: %w", id, err)
		}
		if currentVersion != expectedVersion {
			return fmt.Errorf("goal %q: %w", id, basestore.ErrConflict)
		}
		if err := domain.ValidateGoalTransition(currentState, state); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
UPDATE goals
SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`, state, s.source.Now().UTC().Format(time.RFC3339Nano), id, expectedVersion)
		if err != nil {
			return fmt.Errorf("update goal %q: %w", id, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read goal update result: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("goal %q: %w", id, basestore.ErrConflict)
		}
		return s.appendEvent(ctx, tx, "goal", id, prepared)
	})
}

// Events returns the immutable event stream for one aggregate in sequence order.
func (s *Store) Events(ctx context.Context, aggregateType, aggregateID string) ([]domain.Event, error) {
	if aggregateType == "" || aggregateID == "" {
		return nil, errors.New("aggregate type and id are required")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, aggregate_type, aggregate_id, sequence, event_type,
       actor_type, actor_id, correlation_id, payload_json, created_at
FROM events
WHERE aggregate_type = ? AND aggregate_id = ?
ORDER BY sequence`, aggregateType, aggregateID)
	if err != nil {
		return nil, fmt.Errorf("read events for %s/%s: %w", aggregateType, aggregateID, err)
	}
	defer rows.Close()

	var events []domain.Event
	for rows.Next() {
		var event domain.Event
		var createdAt string
		if err := rows.Scan(
			&event.ID,
			&event.AggregateType,
			&event.AggregateID,
			&event.Sequence,
			&event.EventType,
			&event.ActorType,
			&event.ActorID,
			&event.CorrelationID,
			&event.Payload,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse event %q created_at: %w", event.ID, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events for %s/%s: %w", aggregateType, aggregateID, err)
	}
	return events, nil
}

func prepareEvent(event EventInput) (preparedEvent, error) {
	if !validEventLabel(event.Type) || !validEventLabel(event.ActorType) {
		return preparedEvent{}, errors.New("event type and actor type must be non-empty single-line values")
	}
	payload, err := canonical.Marshal(event.Payload)
	if err != nil {
		return preparedEvent{}, fmt.Errorf("canonicalize event payload: %w", err)
	}
	return preparedEvent{input: event, payload: payload}, nil
}

func validEventLabel(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n")
}

func (s *Store) appendEvent(ctx context.Context, tx *sql.Tx, aggregateType, aggregateID string, event preparedEvent) error {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(sequence), 0) + 1
FROM events
WHERE aggregate_type = ? AND aggregate_id = ?`, aggregateType, aggregateID).Scan(&sequence); err != nil {
		return fmt.Errorf("allocate event sequence for %s/%s: %w", aggregateType, aggregateID, err)
	}
	createdAt := s.source.Now().UTC()
	id, err := newEventID(createdAt)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO events(
    id, aggregate_type, aggregate_id, sequence, event_type,
    actor_type, actor_id, correlation_id, payload_json, created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id,
		aggregateType,
		aggregateID,
		sequence,
		event.input.Type,
		event.input.ActorType,
		event.input.ActorID,
		event.input.CorrelationID,
		event.payload,
		createdAt.Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("append event %s for %s/%s: %w", event.input.Type, aggregateType, aggregateID, err)
	}
	return nil
}

func newEventID(now time.Time) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate event id: %w", err)
	}
	return fmt.Sprintf("event_%019d_%s", now.UnixNano(), hex.EncodeToString(random)), nil
}

func (s *Store) withTransaction(ctx context.Context, operation func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sqlite transaction: %w", err)
	}
	if err := operation(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("rollback sqlite transaction: %w", rollbackErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite transaction: %w", err)
	}
	return nil
}
