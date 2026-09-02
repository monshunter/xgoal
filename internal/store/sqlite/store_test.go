package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
)

func TestOpenCreatesPrivateWALStoreAndReopens(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, projectDir, clock.NewFake(time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	info := store.Info()
	if info.DatabasePath != filepath.Join(projectDir, "state.db") {
		t.Fatalf("DatabasePath = %q", info.DatabasePath)
	}
	if info.SQLiteVersion == "" {
		t.Fatal("SQLiteVersion is empty")
	}
	if info.JournalMode != "wal" || !info.ForeignKeys || info.Synchronous != 2 || info.BusyTimeout != 5000 {
		t.Fatalf("unexpected runtime info: %+v", info)
	}
	if info.SchemaVersion != len(migrations) {
		t.Fatalf("SchemaVersion = %d, want %d", info.SchemaVersion, len(migrations))
	}

	assertPermissions(t, projectDir, 0o700)
	assertPermissions(t, filepath.Join(projectDir, "backups"), 0o700)
	assertPermissions(t, info.DatabasePath, 0o600)

	var migrationCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("count schema migrations: %v", err)
	}
	if migrationCount != len(migrations) {
		t.Fatalf("migration count = %d, want %d", migrationCount, len(migrations))
	}
	var removedTables int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('budget_limits', 'budget_usage')`).Scan(&removedTables); err != nil {
		t.Fatalf("inspect removed accounting tables: %v", err)
	}
	if removedTables != 0 {
		t.Fatalf("removed accounting tables still exist: %d", removedTables)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, projectDir, clock.Real{})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if reopened.Info().SchemaVersion != len(migrations) {
		t.Fatalf("reopened SchemaVersion = %d, want %d", reopened.Info().SchemaVersion, len(migrations))
	}
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("count reopened migrations: %v", err)
	}
	if migrationCount != len(migrations) {
		t.Fatalf("reopened migration count = %d, want %d", migrationCount, len(migrations))
	}
}

func TestOpenRejectsMigrationDriftAndFutureSchema(t *testing.T) {
	t.Parallel()

	t.Run("hash drift", func(t *testing.T) {
		ctx := context.Background()
		projectDir := filepath.Join(t.TempDir(), "project")
		store, err := Open(ctx, projectDir, clock.Real{})
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE schema_migrations SET sha256 = ? WHERE version = 1`, strings.Repeat("0", 64)); err != nil {
			t.Fatalf("tamper migration hash: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}

		_, err = Open(ctx, projectDir, clock.Real{})
		if !errors.Is(err, ErrMigrationDrift) {
			t.Fatalf("reopen error = %v, want ErrMigrationDrift", err)
		}
	})

	t.Run("future schema", func(t *testing.T) {
		ctx := context.Background()
		projectDir := filepath.Join(t.TempDir(), "project")
		store, err := Open(ctx, projectDir, clock.Real{})
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		if _, err := store.db.ExecContext(ctx, `PRAGMA user_version = 99`); err != nil {
			t.Fatalf("set future schema: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}

		_, err = Open(ctx, projectDir, clock.Real{})
		if !errors.Is(err, ErrSchemaTooNew) {
			t.Fatalf("reopen error = %v, want ErrSchemaTooNew", err)
		}
	})
}

func TestUpgradeCreatesValidatedBackupOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	source := clock.NewFake(time.Date(2026, 9, 2, 9, 30, 0, 123, time.UTC))
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}

	store, err := openWithMigrations(ctx, projectDir, source, migrations)
	if err != nil {
		t.Fatalf("initial open error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("initial Close() error = %v", err)
	}

	upgradeVersion := len(migrations) + 1
	upgraded := append(append([]Migration(nil), migrations...), newMigration(upgradeVersion, "upgrade_marker", `
CREATE TABLE upgrade_marker (
    id INTEGER PRIMARY KEY,
    value TEXT NOT NULL
);`))
	store, err = openWithMigrations(ctx, projectDir, source, upgraded)
	if err != nil {
		t.Fatalf("upgrade open error = %v", err)
	}
	if store.Info().SchemaVersion != upgradeVersion {
		t.Fatalf("upgraded SchemaVersion = %d, want %d", store.Info().SchemaVersion, upgradeVersion)
	}
	var backupRecords int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM migration_backups`).Scan(&backupRecords); err != nil {
		t.Fatalf("count backup records: %v", err)
	}
	if backupRecords != 1 {
		t.Fatalf("backup record count = %d, want 1", backupRecords)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("upgrade Close() error = %v", err)
	}

	backups := completedBackups(t, filepath.Join(projectDir, "backups"))
	if len(backups) != 1 {
		t.Fatalf("completed backups = %v, want one", backups)
	}
	assertPermissions(t, backups[0], 0o600)
	backupDB, err := openReadOnlyDatabase(ctx, backups[0])
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backupDB.Close()
	var version int
	if err := backupDB.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read backup version: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("backup user_version = %d, want %d", version, len(migrations))
	}
	var quickCheck string
	if err := backupDB.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&quickCheck); err != nil {
		t.Fatalf("backup quick_check: %v", err)
	}
	if quickCheck != "ok" {
		t.Fatalf("backup quick_check = %q", quickCheck)
	}

	reopened, err := openWithMigrations(ctx, projectDir, source, upgraded)
	if err != nil {
		t.Fatalf("reopen upgraded store: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("reopened Close() error = %v", err)
	}
	if got := len(completedBackups(t, filepath.Join(projectDir, "backups"))); got != 1 {
		t.Fatalf("completed backups after reopen = %d, want 1", got)
	}
}

func TestM2MigrationUpgradesM1StoreAndPreservesStateWithBackup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	source := clock.NewFake(time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC))
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 2 {
		t.Fatal("M2 migration is missing")
	}
	m1, err := openWithMigrations(ctx, projectDir, source, migrations[:1])
	if err != nil {
		t.Fatal(err)
	}
	goal := domain.Goal{ID: "goal_before_m2", State: domain.GoalDraft, Version: 1}
	if err := m1.CreateGoal(ctx, goal, EventInput{Type: "GoalCreated", ActorType: "user", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := m1.Close(); err != nil {
		t.Fatal(err)
	}

	m2, err := Open(ctx, projectDir, source)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Info().SchemaVersion != len(migrations) {
		t.Fatalf("M2 schema version = %d, want %d", m2.Info().SchemaVersion, len(migrations))
	}
	if persisted, err := m2.Goal(ctx, goal.ID); err != nil || persisted.State != domain.GoalDraft {
		t.Fatalf("goal after M2 migration = %+v, %v", persisted, err)
	}
	for _, table := range []string{"workspaces", "patch_bundles", "environment_snapshots", "validator_definitions", "validator_registrations", "validator_runs", "promotions"} {
		var count int
		if err := m2.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("M2 table %s count = %d, %v", table, count, err)
		}
	}
	if err := m2.Close(); err != nil {
		t.Fatal(err)
	}
	backups := completedBackups(t, filepath.Join(projectDir, "backups"))
	if len(backups) != 1 {
		t.Fatalf("M1 to M2 backups = %v, want one", backups)
	}
	backup, err := openReadOnlyDatabase(ctx, backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var version int
	if err := backup.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("M2 backup schema version = %d, %v", version, err)
	}
	var goalCount int
	if err := backup.QueryRowContext(ctx, `SELECT COUNT(*) FROM goals WHERE id = ?`, goal.ID).Scan(&goalCount); err != nil || goalCount != 1 {
		t.Fatalf("M2 backup goal count = %d, %v", goalCount, err)
	}
}

func TestMigrationFailureRollsBackAndPreservesBackup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	store, err := openWithMigrations(ctx, projectDir, clock.Real{}, migrations)
	if err != nil {
		t.Fatalf("initial open error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("initial Close() error = %v", err)
	}

	broken := append(append([]Migration(nil), migrations...), newMigration(len(migrations)+1, "broken", `CREATE TABLE broken (`))
	if _, err := openWithMigrations(ctx, projectDir, clock.Real{}, broken); err == nil {
		t.Fatal("broken migration unexpectedly succeeded")
	}
	if got := len(completedBackups(t, filepath.Join(projectDir, "backups"))); got != 1 {
		t.Fatalf("completed backups after failure = %d, want 1", got)
	}

	reopened, err := openWithMigrations(ctx, projectDir, clock.Real{}, migrations)
	if err != nil {
		t.Fatalf("reopen after failed migration: %v", err)
	}
	defer reopened.Close()
	if reopened.Info().SchemaVersion != len(migrations) {
		t.Fatalf("schema version after failed migration = %d, want %d", reopened.Info().SchemaVersion, len(migrations))
	}
	var brokenTables int
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'broken'`).Scan(&brokenTables); err != nil {
		t.Fatalf("query broken table: %v", err)
	}
	if brokenTables != 0 {
		t.Fatalf("broken table count = %d, want 0", brokenTables)
	}
}

func TestOpenPreservesInterruptedBackupAndRejectsUnsafePaths(t *testing.T) {
	t.Parallel()

	t.Run("interrupted backup", func(t *testing.T) {
		ctx := context.Background()
		projectDir := filepath.Join(t.TempDir(), "project")
		migrations, err := loadMigrations()
		if err != nil {
			t.Fatalf("loadMigrations() error = %v", err)
		}
		store, err := openWithMigrations(ctx, projectDir, clock.Real{}, migrations)
		if err != nil {
			t.Fatalf("initial open error = %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("initial Close() error = %v", err)
		}
		interrupted := filepath.Join(projectDir, "backups", "abandoned.tmp")
		if err := os.WriteFile(interrupted, []byte("partial"), 0o600); err != nil {
			t.Fatalf("write interrupted backup: %v", err)
		}

		upgraded := append(append([]Migration(nil), migrations...), newMigration(len(migrations)+1, "upgrade_marker", `CREATE TABLE upgrade_marker (id INTEGER PRIMARY KEY);`))
		store, err = openWithMigrations(ctx, projectDir, clock.Real{}, upgraded)
		if err != nil {
			t.Fatalf("upgrade open error = %v", err)
		}
		defer store.Close()
		if _, err := os.Stat(interrupted); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("interrupted temp still exists, stat error = %v", err)
		}
		matches, err := filepath.Glob(interrupted + ".incomplete*")
		if err != nil {
			t.Fatalf("glob incomplete backups: %v", err)
		}
		if len(matches) != 1 {
			t.Fatalf("incomplete backups = %v, want one", matches)
		}
	})

	t.Run("relative", func(t *testing.T) {
		_, err := Open(context.Background(), "relative/project", clock.Real{})
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Open(relative) error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		_, err := Open(context.Background(), link, clock.Real{})
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Open(symlink) error = %v, want ErrUnsafePath", err)
		}
	})
}

func TestKnownNetworkFilesystemClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info filesystemInfo
		want bool
	}{
		{name: "apfs", info: filesystemInfo{Name: "apfs"}, want: false},
		{name: "ext4", info: filesystemInfo{Name: "ext4", Magic: 0xef53}, want: false},
		{name: "nfs name", info: filesystemInfo{Name: "nfs"}, want: true},
		{name: "smb name", info: filesystemInfo{Name: "smbfs"}, want: true},
		{name: "nfs magic", info: filesystemInfo{Magic: 0x6969}, want: true},
		{name: "cifs magic", info: filesystemInfo{Magic: 0xff534d42}, want: true},
		{name: "ceph magic", info: filesystemInfo{Magic: 0x00c36400}, want: true},
		{name: "9p magic", info: filesystemInfo{Magic: 0x01021997}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isKnownNetworkFilesystem(tt.info); got != tt.want {
				t.Fatalf("isKnownNetworkFilesystem(%+v) = %t, want %t", tt.info, got, tt.want)
			}
		})
	}
}

func assertPermissions(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("permissions for %s = %04o, want %04o", path, got, want)
	}
}

func completedBackups(t *testing.T, backupDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("read backup dir: %v", err)
	}
	var backups []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".db") {
			backups = append(backups, filepath.Join(backupDir, entry.Name()))
		}
	}
	return backups
}
