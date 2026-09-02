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

const effectSchema = "xgoal.effect/v1"

// EffectRequest is the immutable identity and request body for an external side effect.
type EffectRequest struct {
	ID      string
	Key     string
	Type    string
	Request any
}

// RequestEffect journals a request before external execution or returns its exact replay.
func (s *Store) RequestEffect(ctx context.Context, request EffectRequest, event EventInput) (domain.Effect, bool, error) {
	if !validIdempotencyLabel(request.ID) || !validIdempotencyLabel(request.Key) || !validIdempotencyLabel(request.Type) {
		return domain.Effect{}, false, errors.New("effect id, key, and type must be non-empty single-line values")
	}
	requestJSON, requestHash, err := canonicalValue("effect-request", effectSchema, request.Request)
	if err != nil {
		return domain.Effect{}, false, fmt.Errorf("canonicalize effect request: %w", err)
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return domain.Effect{}, false, err
	}
	var result domain.Effect
	created := false
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		existing, err := readEffectByKey(ctx, tx, request.Key)
		if err == nil {
			if existing.Type != request.Type || existing.RequestHash != requestHash {
				return fmt.Errorf("effect key %q: %w", request.Key, basestore.ErrIdempotencyConflict)
			}
			result = existing
			return nil
		}
		if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		if _, err := readEffect(ctx, tx, request.ID); err == nil {
			return fmt.Errorf("effect %q: %w", request.ID, basestore.ErrAlreadyExists)
		} else if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		now := s.source.Now().UTC()
		if _, err := tx.ExecContext(ctx, `
INSERT INTO effects(
    id, effect_key, effect_type, state, request_json, request_hash,
    version, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			request.ID,
			request.Key,
			request.Type,
			domain.EffectRequested,
			requestJSON,
			requestHash,
			now.Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert effect %q: %w", request.ID, err)
		}
		if err := s.appendEvent(ctx, tx, "effect", request.ID, prepared); err != nil {
			return err
		}
		result = domain.Effect{
			ID:          request.ID,
			Key:         request.Key,
			Type:        request.Type,
			State:       domain.EffectRequested,
			RequestJSON: append([]byte(nil), requestJSON...),
			RequestHash: requestHash,
			Version:     1,
		}
		created = true
		return nil
	})
	if err != nil {
		return domain.Effect{}, false, err
	}
	return result, created, nil
}

// Effect returns one persisted external effect journal entry.
func (s *Store) Effect(ctx context.Context, id string) (domain.Effect, error) {
	if id == "" {
		return domain.Effect{}, errors.New("effect id is empty")
	}
	return readEffect(ctx, s.db, id)
}

// RecoverableEffects returns non-terminal external effects in stable order for startup read-back.
func (s *Store) RecoverableEffects(ctx context.Context) ([]domain.Effect, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, effect_key, effect_type, state, request_json, request_hash,
       observation_json, observation_hash, version
FROM effects
WHERE state IN (?, ?, ?, ?)
ORDER BY id`,
		domain.EffectRequested,
		domain.EffectExecuting,
		domain.EffectObserving,
		domain.EffectRecovering,
	)
	if err != nil {
		return nil, fmt.Errorf("read recoverable effects: %w", err)
	}
	defer rows.Close()
	var effects []domain.Effect
	for rows.Next() {
		effect, err := scanEffect(rows, "recovery id", "")
		if err != nil {
			return nil, err
		}
		effects = append(effects, effect)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recoverable effects: %w", err)
	}
	return effects, nil
}

// UpdateEffect advances an Effect with CAS; terminal outcomes require read-back observation.
func (s *Store) UpdateEffect(
	ctx context.Context,
	id string,
	expectedVersion int64,
	state domain.EffectState,
	observation any,
	event EventInput,
) (domain.Effect, error) {
	if id == "" || expectedVersion <= 0 || !state.Valid() {
		return domain.Effect{}, errors.New("invalid effect update")
	}
	terminal := state == domain.EffectSucceeded || state == domain.EffectFailed
	if terminal && observation == nil {
		return domain.Effect{}, errors.New("terminal effect update requires a read-back observation")
	}
	var observationJSON []byte
	var observationHash string
	var err error
	if observation != nil {
		observationJSON, observationHash, err = canonicalValue("effect-observation", effectSchema, observation)
		if err != nil {
			return domain.Effect{}, fmt.Errorf("canonicalize effect observation: %w", err)
		}
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return domain.Effect{}, err
	}
	var result domain.Effect
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		current, err := readEffect(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Version != expectedVersion {
			return fmt.Errorf("effect %q: %w", id, basestore.ErrConflict)
		}
		if err := domain.ValidateEffectTransition(current.State, state); err != nil {
			return err
		}
		if observation == nil {
			observationJSON = current.ObservationJSON
			observationHash = current.ObservationHash
		}
		updated, err := tx.ExecContext(ctx, `
UPDATE effects
SET state = ?, observation_json = ?, observation_hash = ?,
    version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`,
			state,
			nullableBytes(observationJSON),
			nullableString(observationHash),
			s.source.Now().UTC().Format(time.RFC3339Nano),
			id,
			expectedVersion,
		)
		if err != nil {
			return fmt.Errorf("update effect %q: %w", id, err)
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return fmt.Errorf("read effect update result: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("effect %q: %w", id, basestore.ErrConflict)
		}
		if err := s.appendEvent(ctx, tx, "effect", id, prepared); err != nil {
			return err
		}
		current.State = state
		current.ObservationJSON = append([]byte(nil), observationJSON...)
		current.ObservationHash = observationHash
		current.Version++
		result = current
		return nil
	})
	if err != nil {
		return domain.Effect{}, err
	}
	return result, nil
}

func readEffect(ctx context.Context, queryer rowQueryer, id string) (domain.Effect, error) {
	return scanEffect(queryer.QueryRowContext(ctx, `
SELECT id, effect_key, effect_type, state, request_json, request_hash,
       observation_json, observation_hash, version
FROM effects
WHERE id = ?`, id), "id", id)
}

func readEffectByKey(ctx context.Context, queryer rowQueryer, key string) (domain.Effect, error) {
	return scanEffect(queryer.QueryRowContext(ctx, `
SELECT id, effect_key, effect_type, state, request_json, request_hash,
       observation_json, observation_hash, version
FROM effects
WHERE effect_key = ?`, key), "key", key)
}

func scanEffect(row sqlRows, identityKind, identity string) (domain.Effect, error) {
	var effect domain.Effect
	var observationJSON []byte
	var observationHash sql.NullString
	err := row.Scan(
		&effect.ID,
		&effect.Key,
		&effect.Type,
		&effect.State,
		&effect.RequestJSON,
		&effect.RequestHash,
		&observationJSON,
		&observationHash,
		&effect.Version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Effect{}, fmt.Errorf("effect %s %q: %w", identityKind, identity, basestore.ErrNotFound)
	}
	if err != nil {
		return domain.Effect{}, fmt.Errorf("read effect %s %q: %w", identityKind, identity, err)
	}
	if !effect.State.Valid() || effect.Version <= 0 {
		return domain.Effect{}, fmt.Errorf("effect %q contains invalid persisted state", effect.ID)
	}
	if observationJSON != nil {
		effect.ObservationJSON = append([]byte(nil), observationJSON...)
		effect.ObservationHash = observationHash.String
	}
	return effect, nil
}

func nullableBytes(value []byte) any {
	if value == nil {
		return nil
	}
	return value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
