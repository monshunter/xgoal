package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const createMigrationHistorySQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY CHECK (version > 0),
    name TEXT NOT NULL,
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
    applied_at TEXT NOT NULL
) STRICT;`

// Migration is one immutable, monotonically numbered schema transition.
type Migration struct {
	Version int
	Name    string
	SQL     string
	SHA256  string
}

func newMigration(version int, name, statement string) Migration {
	sum := sha256.Sum256([]byte(statement))
	return Migration{
		Version: version,
		Name:    name,
		SQL:     statement,
		SHA256:  hex.EncodeToString(sum[:]),
	}
}

func loadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		base := strings.TrimSuffix(entry.Name(), ".sql")
		versionText, name, found := strings.Cut(base, "_")
		if !found || name == "" {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.Atoi(versionText)
		if err != nil {
			return nil, fmt.Errorf("parse migration version %q: %w", entry.Name(), err)
		}
		content, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		migrations = append(migrations, newMigration(version, name, string(content)))
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	if err := validateMigrations(migrations); err != nil {
		return nil, err
	}
	return migrations, nil
}

func validateMigrations(migrations []Migration) error {
	if len(migrations) == 0 {
		return errors.New("sqlite migrations are empty")
	}
	for index, migration := range migrations {
		wantVersion := index + 1
		if migration.Version != wantVersion {
			return fmt.Errorf("sqlite migration version %d is not contiguous, want %d", migration.Version, wantVersion)
		}
		if migration.Name == "" || strings.TrimSpace(migration.SQL) == "" {
			return fmt.Errorf("sqlite migration %d has an empty name or statement", migration.Version)
		}
		sum := sha256.Sum256([]byte(migration.SQL))
		if migration.SHA256 != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("sqlite migration %d has an invalid in-memory hash", migration.Version)
		}
	}
	return nil
}

func inspectExistingDatabase(ctx context.Context, db *sql.DB, migrations []Migration) (int, error) {
	if err := verifyDatabaseIntegrity(ctx, db); err != nil {
		return 0, err
	}
	version, err := readSchemaVersion(ctx, db)
	if err != nil {
		return 0, err
	}
	latest := migrations[len(migrations)-1].Version
	if version > latest {
		return 0, fmt.Errorf("%w: database=%d binary=%d", ErrSchemaTooNew, version, latest)
	}
	if err := verifyMigrationHistory(ctx, db, migrations, version); err != nil {
		return 0, err
	}
	return version, nil
}

func readSchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if version < 0 {
		return 0, fmt.Errorf("%w: negative user_version %d", ErrMigrationDrift, version)
	}
	return version, nil
}

func verifyMigrationHistory(ctx context.Context, db *sql.DB, migrations []Migration, schemaVersion int) error {
	var tableCount int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM sqlite_master
WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&tableCount); err != nil {
		return fmt.Errorf("inspect migration history table: %w", err)
	}
	if tableCount == 0 {
		if schemaVersion == 0 {
			return nil
		}
		return fmt.Errorf("%w: schema version %d has no migration history", ErrMigrationDrift, schemaVersion)
	}

	rows, err := db.QueryContext(ctx, `SELECT version, name, sha256, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("read migration history: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var version int
		var name, hash, appliedAt string
		if err := rows.Scan(&version, &name, &hash, &appliedAt); err != nil {
			return fmt.Errorf("scan migration history: %w", err)
		}
		count++
		if version != count || version > len(migrations) {
			return fmt.Errorf("%w: unexpected migration version %d", ErrMigrationDrift, version)
		}
		expected := migrations[version-1]
		if name != expected.Name || hash != expected.SHA256 || appliedAt == "" {
			return fmt.Errorf("%w: migration %d metadata mismatch", ErrMigrationDrift, version)
		}
		if _, err := time.Parse(time.RFC3339Nano, appliedAt); err != nil {
			return fmt.Errorf("%w: migration %d has invalid applied_at", ErrMigrationDrift, version)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate migration history: %w", err)
	}
	if count != schemaVersion {
		return fmt.Errorf("%w: user_version=%d history_rows=%d", ErrMigrationDrift, schemaVersion, count)
	}
	return nil
}

func applyMigrations(
	ctx context.Context,
	db *sql.DB,
	migrations []Migration,
	currentVersion int,
	backup *migrationBackup,
	source clock.Clock,
) error {
	latest := migrations[len(migrations)-1].Version
	if currentVersion == latest {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin immediate migration transaction: %w", err)
	}
	rollback := func(cause error) error {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(cause, fmt.Errorf("rollback migration transaction: %w", rollbackErr))
		}
		return cause
	}

	if _, err := tx.ExecContext(ctx, createMigrationHistorySQL); err != nil {
		return rollback(fmt.Errorf("create migration history: %w", err))
	}
	for _, migration := range migrations[currentVersion:] {
		if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
			return rollback(fmt.Errorf("apply migration %04d_%s: %w", migration.Version, migration.Name, err))
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO schema_migrations(version, name, sha256, applied_at)
VALUES (?, ?, ?, ?)`, migration.Version, migration.Name, migration.SHA256, source.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return rollback(fmt.Errorf("record migration %d: %w", migration.Version, err))
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, migration.Version)); err != nil {
			return rollback(fmt.Errorf("set schema version %d: %w", migration.Version, err))
		}
	}
	if backup != nil {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO migration_backups(source_version, target_version, filename, sha256, created_at)
VALUES (?, ?, ?, ?, ?)`, backup.SourceVersion, backup.TargetVersion, backup.Filename, backup.SHA256, backup.CreatedAt); err != nil {
			return rollback(fmt.Errorf("record migration backup: %w", err))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration transaction: %w", err)
	}
	return nil
}

func verifyDatabaseIntegrity(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return fmt.Errorf("run sqlite quick_check: %w", err)
	}
	defer rows.Close()
	resultCount := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("scan sqlite quick_check: %w", err)
		}
		resultCount++
		if result != "ok" {
			return fmt.Errorf("sqlite quick_check failed: %s", result)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sqlite quick_check: %w", err)
	}
	if resultCount != 1 {
		return fmt.Errorf("sqlite quick_check returned %d rows", resultCount)
	}

	foreignRows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("run sqlite foreign_key_check: %w", err)
	}
	defer foreignRows.Close()
	if foreignRows.Next() {
		return errors.New("sqlite foreign_key_check found a violation")
	}
	if err := foreignRows.Err(); err != nil {
		return fmt.Errorf("iterate sqlite foreign_key_check: %w", err)
	}
	return nil
}
