package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

const idempotencySchema = "xgoal.idempotency/v1"

// BeginIdempotentRequest creates a request record or returns the matching prior record.
func (s *Store) BeginIdempotentRequest(ctx context.Context, scope, key string, request any) (domain.IdempotencyRecord, bool, error) {
	if !validIdempotencyLabel(scope) || !validIdempotencyLabel(key) {
		return domain.IdempotencyRecord{}, false, errors.New("idempotency scope and key must be non-empty single-line values")
	}
	requestJSON, requestHash, err := canonicalValue("idempotency-request", idempotencySchema, request)
	if err != nil {
		return domain.IdempotencyRecord{}, false, fmt.Errorf("canonicalize idempotent request: %w", err)
	}
	var result domain.IdempotencyRecord
	created := false
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		existing, err := readIdempotencyRecord(ctx, tx, scope, key)
		if err == nil {
			if existing.RequestHash != requestHash {
				return fmt.Errorf("idempotency record %s/%s: %w", scope, key, basestore.ErrIdempotencyConflict)
			}
			result = existing
			return nil
		}
		if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		createdAt := s.source.Now().UTC()
		if _, err := tx.ExecContext(ctx, `
INSERT INTO idempotency_records(
    scope, key, request_json, request_hash, state, created_at
)
VALUES (?, ?, ?, ?, ?, ?)`,
			scope,
			key,
			requestJSON,
			requestHash,
			domain.IdempotencyInProgress,
			createdAt.Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert idempotency record %s/%s: %w", scope, key, err)
		}
		result = domain.IdempotencyRecord{
			Scope:       scope,
			Key:         key,
			RequestJSON: append([]byte(nil), requestJSON...),
			RequestHash: requestHash,
			State:       domain.IdempotencyInProgress,
			CreatedAt:   createdAt,
		}
		created = true
		return nil
	})
	if err != nil {
		return domain.IdempotencyRecord{}, false, err
	}
	return result, created, nil
}

// CompleteIdempotentRequest binds the final response to the original request hash.
func (s *Store) CompleteIdempotentRequest(
	ctx context.Context,
	scope, key, requestHash string,
	responseStatus int,
	response any,
) (domain.IdempotencyRecord, error) {
	if !validIdempotencyLabel(scope) || !validIdempotencyLabel(key) || len(requestHash) != 64 {
		return domain.IdempotencyRecord{}, errors.New("invalid idempotency completion identity")
	}
	if responseStatus < 100 || responseStatus > 599 {
		return domain.IdempotencyRecord{}, errors.New("idempotency response status must be between 100 and 599")
	}
	responseJSON, responseHash, err := canonicalValue("idempotency-response", idempotencySchema, response)
	if err != nil {
		return domain.IdempotencyRecord{}, fmt.Errorf("canonicalize idempotent response: %w", err)
	}
	var result domain.IdempotencyRecord
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		existing, err := readIdempotencyRecord(ctx, tx, scope, key)
		if err != nil {
			return err
		}
		if existing.RequestHash != requestHash {
			return fmt.Errorf("idempotency record %s/%s: %w", scope, key, basestore.ErrIdempotencyConflict)
		}
		if existing.State == domain.IdempotencyCompleted {
			if existing.ResponseStatus != responseStatus || existing.ResponseHash != responseHash {
				return fmt.Errorf("idempotency response %s/%s: %w", scope, key, basestore.ErrIdempotencyConflict)
			}
			result = existing
			return nil
		}
		completedAt := s.source.Now().UTC()
		updated, err := tx.ExecContext(ctx, `
UPDATE idempotency_records
SET state = ?, response_status = ?, response_json = ?, response_hash = ?, completed_at = ?
WHERE scope = ? AND key = ? AND state = ? AND request_hash = ?`,
			domain.IdempotencyCompleted,
			responseStatus,
			responseJSON,
			responseHash,
			completedAt.Format(time.RFC3339Nano),
			scope,
			key,
			domain.IdempotencyInProgress,
			requestHash,
		)
		if err != nil {
			return fmt.Errorf("complete idempotency record %s/%s: %w", scope, key, err)
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return fmt.Errorf("read idempotency completion result: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("idempotency record %s/%s: %w", scope, key, basestore.ErrIdempotencyConflict)
		}
		existing.State = domain.IdempotencyCompleted
		existing.ResponseStatus = responseStatus
		existing.ResponseJSON = append([]byte(nil), responseJSON...)
		existing.ResponseHash = responseHash
		existing.CompletedAt = completedAt
		result = existing
		return nil
	})
	if err != nil {
		return domain.IdempotencyRecord{}, err
	}
	return result, nil
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readIdempotencyRecord(ctx context.Context, queryer rowQueryer, scope, key string) (domain.IdempotencyRecord, error) {
	var record domain.IdempotencyRecord
	var createdAt string
	var completedAt sql.NullString
	var responseStatus sql.NullInt64
	var responseJSON []byte
	var responseHash sql.NullString
	err := queryer.QueryRowContext(ctx, `
SELECT scope, key, request_json, request_hash, state,
       response_status, response_json, response_hash, created_at, completed_at
FROM idempotency_records
WHERE scope = ? AND key = ?`, scope, key).Scan(
		&record.Scope,
		&record.Key,
		&record.RequestJSON,
		&record.RequestHash,
		&record.State,
		&responseStatus,
		&responseJSON,
		&responseHash,
		&createdAt,
		&completedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IdempotencyRecord{}, fmt.Errorf("idempotency record %s/%s: %w", scope, key, basestore.ErrNotFound)
	}
	if err != nil {
		return domain.IdempotencyRecord{}, fmt.Errorf("read idempotency record %s/%s: %w", scope, key, err)
	}
	if !record.State.Valid() {
		return domain.IdempotencyRecord{}, fmt.Errorf("idempotency record %s/%s has invalid state", scope, key)
	}
	record.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return domain.IdempotencyRecord{}, fmt.Errorf("parse idempotency record created_at: %w", err)
	}
	if responseStatus.Valid {
		record.ResponseStatus = int(responseStatus.Int64)
		record.ResponseJSON = append([]byte(nil), responseJSON...)
		record.ResponseHash = responseHash.String
	}
	if completedAt.Valid {
		record.CompletedAt, err = time.Parse(time.RFC3339Nano, completedAt.String)
		if err != nil {
			return domain.IdempotencyRecord{}, fmt.Errorf("parse idempotency record completed_at: %w", err)
		}
	}
	return record, nil
}

func canonicalValue(kind, schema string, value any) ([]byte, string, error) {
	encoded, err := canonical.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	hash, err := canonical.Hash(kind, schema, value)
	if err != nil {
		return nil, "", err
	}
	return encoded, hash, nil
}

func validIdempotencyLabel(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n")
}
