package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
)

type EventBoundary struct {
	AggregateType string `json:"aggregate_type"`
	AggregateID   string `json:"aggregate_id"`
	Sequence      int64  `json:"sequence"`
	EventID       string `json:"event_id"`
}

type SnapshotBoundary struct {
	StartedAt     time.Time       `json:"started_at"`
	CompletedAt   time.Time       `json:"completed_at"`
	SchemaVersion int             `json:"schema_version"`
	EventCount    int64           `json:"event_count"`
	Events        []EventBoundary `json:"events"`
}

// ReadSnapshot uses its own WAL-aware read-only connection. The returned Store
// queries only the immutable copy and must be closed by the caller. ProjectDir
// still identifies source artifacts; this is an audit snapshot, not an executor.
func (s *Store) ReadSnapshot(ctx context.Context, destination string) (_ *Store, boundary SnapshotBoundary, retErr error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return nil, boundary, ErrUnsafePath
	}
	parent := filepath.Dir(destination)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return nil, boundary, ErrUnsafePath
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, boundary, ErrUnsafePath
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, boundary, err
	}
	if err := file.Close(); err != nil {
		return nil, boundary, err
	}
	// Do not use openReadOnlyDatabase: immutable=1 would ignore live WAL pages.
	live, err := openDatabase(ctx, sqliteFileDSN(s.info.DatabasePath, map[string]string{"mode": "ro", "_dqs": "false", "_defensive": "true"}))
	if err != nil {
		return nil, boundary, err
	}
	boundary.StartedAt = s.source.Now().UTC()
	_, copyErr := live.ExecContext(ctx, `VACUUM INTO ?`, destination)
	if err := errors.Join(copyErr, live.Close()); err != nil {
		return nil, boundary, fmt.Errorf("read-only live snapshot unavailable: %w", err)
	}
	boundary.CompletedAt = s.source.Now().UTC()
	if err := syncFile(destination); err != nil {
		return nil, boundary, err
	}
	db, err := openReadOnlyDatabase(ctx, destination)
	if err != nil {
		return nil, boundary, err
	}
	defer func() {
		if retErr != nil {
			db.Close()
		}
	}()
	if err := verifyDatabaseIntegrity(ctx, db); err != nil {
		return nil, boundary, err
	}
	boundary.SchemaVersion, err = readSchemaVersion(ctx, db)
	if err != nil || boundary.SchemaVersion != s.info.SchemaVersion {
		return nil, boundary, fmt.Errorf("snapshot schema differs from running Store: %w", errors.Join(err, ErrMigrationDrift))
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&boundary.EventCount); err != nil {
		return nil, boundary, err
	}
	rows, err := db.QueryContext(ctx, `SELECT e.aggregate_type,e.aggregate_id,e.sequence,e.id FROM events e JOIN (SELECT aggregate_type,aggregate_id,MAX(sequence) AS sequence FROM events GROUP BY aggregate_type,aggregate_id) b USING (aggregate_type,aggregate_id,sequence) ORDER BY e.aggregate_type,e.aggregate_id`)
	if err != nil {
		return nil, boundary, err
	}
	boundary.Events = []EventBoundary{}
	for rows.Next() {
		var event EventBoundary
		if err := rows.Scan(&event.AggregateType, &event.AggregateID, &event.Sequence, &event.EventID); err != nil {
			rows.Close()
			return nil, boundary, err
		}
		boundary.Events = append(boundary.Events, event)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, boundary, err
	}
	infoCopy := s.info
	infoCopy.DatabasePath = destination
	return &Store{db: db, info: infoCopy, source: clock.NewFake(boundary.CompletedAt)}, boundary, nil
}
