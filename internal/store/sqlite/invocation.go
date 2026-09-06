package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/invocation"
	"github.com/monshunter/xgoal/internal/redact"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func (s *Store) RegisterInvocation(ctx context.Context, in invocation.Input) (invocation.Record, error) {
	if err := in.Validate(); err != nil {
		return invocation.Record{}, err
	}
	in.Prompt = redact.String(in.Prompt)
	data, err := canonical.Marshal(in)
	if err != nil {
		return invocation.Record{}, err
	}
	hash, err := canonical.Hash("invocation-input", invocation.Version, in)
	if err != nil {
		return invocation.Record{}, err
	}
	observation := invocation.Observation{Status: "running", ObservedModel: "unknown"}
	encoded, _ := canonical.Marshal(observation)
	at := s.source.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `INSERT INTO invocations(id,goal_id,role,input_json,input_hash,observation_json,status,cursor,version,created_at,updated_at) VALUES(?,?,?,?,?,?,'running',0,1,?,?) ON CONFLICT(id) DO NOTHING`, in.ID, in.GoalID, in.Role, string(data), hash, string(encoded), at, at)
	if err != nil {
		return invocation.Record{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return invocation.Record{}, err
	}
	record, err := s.Invocation(ctx, in.ID)
	if err != nil {
		return record, err
	}
	if count == 0 && record.InputHash != hash {
		return invocation.Record{}, fmt.Errorf("%w: invocation ID already binds different inputs", basestore.ErrConflict)
	}
	return record, nil
}

func scanInvocation(row interface{ Scan(...any) error }) (invocation.Record, error) {
	var r invocation.Record
	var input, observation, created, updated, role, goal, status string
	var cursor int64
	err := row.Scan(&input, &r.InputHash, &observation, &r.Version, &created, &updated, &role, &goal, &status, &cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return r, basestore.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(input), &r.Input); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(observation), &r.Observation); err != nil {
		return r, err
	}
	hash, err := canonical.Hash("invocation-input", invocation.Version, r.Input)
	if err != nil || r.Input.Validate() != nil || hash != r.InputHash || r.Input.Role != role || r.Input.GoalID != goal || r.Observation.Status != status || r.Observation.Cursor != cursor {
		return r, errors.New("invocation observation identity corrupt")
	}
	r.ProtocolVersion = invocation.Version
	r.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return r, err
	}
	r.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return r, err
}

const invocationColumns = `input_json,input_hash,observation_json,version,created_at,updated_at,role,goal_id,status,cursor`

func (s *Store) Invocation(ctx context.Context, id string) (invocation.Record, error) {
	return scanInvocation(s.db.QueryRowContext(ctx, `SELECT `+invocationColumns+` FROM invocations WHERE id=?`, id))
}

func (s *Store) LatestInvocation(ctx context.Context, goalID string) (invocation.Record, error) {
	return scanInvocation(s.db.QueryRowContext(ctx, `SELECT `+invocationColumns+` FROM invocations WHERE goal_id=? ORDER BY rowid DESC LIMIT 1`, goalID))
}
func (s *Store) Invocations(ctx context.Context, goalID, role string) ([]invocation.Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+invocationColumns+` FROM invocations WHERE (?='' OR goal_id=?) AND (?='' OR role=?) ORDER BY rowid`, goalID, goalID, role, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]invocation.Record, 0)
	for rows.Next() {
		r, err := scanInvocation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
func (s *Store) UpdateInvocation(ctx context.Context, id string, version int64, o invocation.Observation) error {
	if o.Status != "running" && o.Status != "returned" && o.Status != "failed" && o.Status != "interrupted" {
		return errors.New("invalid invocation observation status")
	}
	if o.StderrCursor < 0 || o.StderrCursor > invocation.MaxEvents || o.StderrBytes < 0 || o.StderrBytes > invocation.MaxLogBytes || o.Cursor < 0 || o.Cursor > invocation.MaxEvents || o.Bytes < 0 || o.Bytes > invocation.MaxLogBytes {
		return errors.New("invalid invocation observation bounds")
	}
	o.Unavailable = redact.String(o.Unavailable)
	o.LogError = redact.String(o.LogError)
	o.Failure = redact.String(o.Failure)
	o.SessionID = redact.String(o.SessionID)
	o.ObservedModel = redact.String(o.ObservedModel)
	data, err := canonical.Marshal(o)
	if err != nil {
		return err
	}
	// Terminal labels cannot revive, and concurrent/late scans cannot regress the
	// persisted cursor. No execution state is mutated by this projection.
	result, err := s.db.ExecContext(ctx, `UPDATE invocations SET observation_json=?,status=?,cursor=?,version=version+1,updated_at=? WHERE id=? AND version=? AND cursor<=? AND CAST(json_extract(observation_json,'$.stderr_durable_cursor') AS INTEGER)<=? AND (status='running' OR status=?)`, string(data), o.Status, o.Cursor, s.source.Now().UTC().Format(time.RFC3339Nano), id, version, o.Cursor, o.StderrCursor, o.Status)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return basestore.ErrConflict
	}
	return nil
}

// RefreshInvocation scans files outside the control connection, then makes one
// small optimistic projection update. A missed update is retried on the next
// notification/read, never by blocking the Provider callback.
func (s *Store) RefreshInvocation(ctx context.Context, id string) (invocation.Record, error) {
	record, err := s.Invocation(ctx, id)
	if err != nil {
		return record, err
	}
	observed, scanErr := invocation.Inspect(ctx, s.info.ProjectDir, record.Input.ProviderDir, record.Observation)
	if ctx.Err() != nil {
		return record, ctx.Err()
	}
	if scanErr != nil {
		observed.Unavailable = scanErr.Error()
	}
	before, _ := canonical.Marshal(record.Observation)
	after, _ := canonical.Marshal(observed)
	if string(before) != string(after) {
		if err := s.UpdateInvocation(ctx, id, record.Version, observed); err != nil {
			return record, err
		}
		record.Version++
		record.Observation = observed
		record.UpdatedAt = s.source.Now().UTC()
	}
	return record, nil
}

func (s *Store) FinishInvocation(ctx context.Context, id, session, resultStatus string, cause error) error {
	for tries := 0; tries < 8; tries++ {
		err := s.finishInvocation(ctx, id, session, resultStatus, cause)
		if !errors.Is(err, basestore.ErrConflict) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return basestore.ErrConflict
}
func (s *Store) finishInvocation(ctx context.Context, id, session, resultStatus string, cause error) error {
	record, err := s.RefreshInvocation(ctx, id)
	if err != nil {
		return err
	}
	o := record.Observation
	o.Status = "returned"
	o.ResultStatus = resultStatus
	if session != "" {
		if o.SessionID != "" && o.SessionID != session {
			return errors.New("invocation returned a different session")
		}
		o.SessionID = session
	}
	if cause != nil {
		o.Status = "failed"
		o.Failure = cause.Error()
		o.Truncated = o.Truncated || strings.Contains(cause.Error(), "exceeded")
	}
	return s.UpdateInvocation(ctx, id, record.Version, o)
}
