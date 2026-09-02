package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/monshunter/xgoal/internal/clock"
	modernsqlite "modernc.org/sqlite"
)

const (
	databaseFilename      = "state.db"
	backupDirectoryName   = "backups"
	expectedSQLiteVersion = "3.53.3"
)

var (
	ErrMigrationDrift        = errors.New("sqlite migration history drift")
	ErrSchemaTooNew          = errors.New("sqlite schema is newer than this binary")
	ErrUnsafePath            = errors.New("unsafe sqlite store path")
	ErrUnsupportedFilesystem = errors.New("unsupported sqlite filesystem")
)

// RuntimeInfo is the connection and schema state verified during Open.
type RuntimeInfo struct {
	ProjectDir    string
	DatabasePath  string
	SQLiteVersion string
	JournalMode   string
	ForeignKeys   bool
	Synchronous   int
	BusyTimeout   int
	SchemaVersion int
}

// Store owns one project's single-connection SQLite database.
type Store struct {
	db     *sql.DB
	info   RuntimeInfo
	source clock.Clock

	closeMu sync.Mutex
	closed  bool
}

// Open opens or creates the SQLite store rooted at an absolute project state directory.
func Open(ctx context.Context, projectDir string, source clock.Clock) (*Store, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	return openWithMigrations(ctx, projectDir, source, migrations)
}

func openWithMigrations(ctx context.Context, projectDir string, source clock.Clock, migrations []Migration) (_ *Store, retErr error) {
	if err := validateMigrations(migrations); err != nil {
		return nil, err
	}
	if source == nil {
		source = clock.Real{}
	}

	databasePath, backupDir, preexisting, err := prepareProjectDirectory(projectDir)
	if err != nil {
		return nil, err
	}
	filesystem, err := detectFilesystem(projectDir)
	if err != nil {
		return nil, fmt.Errorf("detect project filesystem: %w", err)
	}
	if isKnownNetworkFilesystem(filesystem) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFilesystem, filesystem.describe())
	}
	if err := recoverInterruptedBackups(backupDir); err != nil {
		return nil, fmt.Errorf("recover interrupted migration backups: %w", err)
	}

	currentVersion := 0
	if preexisting {
		preflight, err := openLiveReadOnlyDatabase(ctx, databasePath)
		if err != nil {
			return nil, fmt.Errorf("open existing database for preflight: %w", err)
		}
		currentVersion, err = inspectExistingDatabase(ctx, preflight, migrations)
		closeErr := preflight.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close database preflight: %w", closeErr)
		}
	}

	db, err := openWritableDatabase(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if retErr != nil {
			_ = db.Close()
		}
	}()

	runtimeInfo, err := verifyRuntime(ctx, db, projectDir, databasePath)
	if err != nil {
		return nil, err
	}
	if !preexisting {
		currentVersion, err = readSchemaVersion(ctx, db)
		if err != nil {
			return nil, err
		}
	}

	var backup *migrationBackup
	latest := migrations[len(migrations)-1].Version
	if preexisting && currentVersion < latest {
		backup, err = createMigrationBackup(ctx, db, backupDir, currentVersion, latest, source)
		if err != nil {
			return nil, fmt.Errorf("create migration backup: %w", err)
		}
	}
	if err := applyMigrations(ctx, db, migrations, currentVersion, backup, source); err != nil {
		return nil, err
	}
	if _, err := inspectExistingDatabase(ctx, db, migrations); err != nil {
		return nil, err
	}
	runtimeInfo.SchemaVersion, err = readSchemaVersion(ctx, db)
	if err != nil {
		return nil, err
	}
	if err := secureDatabaseFiles(databasePath); err != nil {
		return nil, err
	}

	return &Store{db: db, info: runtimeInfo, source: source}, nil
}

// Info returns the settings and schema version verified by Open.
func (s *Store) Info() RuntimeInfo {
	return s.info
}

// Close stops use of the connection pool and preserves SQLite recovery files.
func (s *Store) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close sqlite database: %w", err)
	}
	return secureDatabaseFiles(s.info.DatabasePath)
}

func prepareProjectDirectory(projectDir string) (databasePath, backupDir string, preexisting bool, err error) {
	if projectDir == "" || !filepath.IsAbs(projectDir) || filepath.Clean(projectDir) != projectDir {
		return "", "", false, fmt.Errorf("%w: project directory must be a clean absolute path", ErrUnsafePath)
	}
	if err := ensurePrivateDirectory(projectDir); err != nil {
		return "", "", false, err
	}
	backupDir = filepath.Join(projectDir, backupDirectoryName)
	if err := ensurePrivateDirectory(backupDir); err != nil {
		return "", "", false, err
	}
	databasePath = filepath.Join(projectDir, databaseFilename)
	info, err := os.Lstat(databasePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return databasePath, backupDir, false, nil
		}
		return "", "", false, fmt.Errorf("inspect database path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", false, fmt.Errorf("%w: database path is not a regular file", ErrUnsafePath)
	}
	return databasePath, backupDir, info.Size() > 0, nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect directory %s: %w", path, err)
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create private directory %s: %w", path, err)
		}
		info, err = os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect created directory %s: %w", path, err)
		}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s is not a real directory", ErrUnsafePath, path)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure directory %s: %w", path, err)
	}
	return nil
}

func openWritableDatabase(ctx context.Context, databasePath string) (*sql.DB, error) {
	dsn := sqliteFileDSN(databasePath, map[string]string{
		"mode":          "rwc",
		"_busy_timeout": "5000",
		"_foreign_keys": "1",
		"_journal_mode": "WAL",
		"_synchronous":  "FULL",
		"_txlock":       "immediate",
		"_dqs":          "false",
		"_defensive":    "true",
	})
	db, err := openDatabase(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open writable sqlite database: %w", err)
	}
	if err := os.Chmod(databasePath, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure database file: %w", err)
	}
	return db, nil
}

func openLiveReadOnlyDatabase(ctx context.Context, databasePath string) (*sql.DB, error) {
	return openDatabase(ctx, sqliteFileDSN(databasePath, map[string]string{
		"mode":        "ro",
		"_query_only": "1",
		"_dqs":        "false",
		"_defensive":  "true",
	}))
}

func openReadOnlyDatabase(ctx context.Context, databasePath string) (*sql.DB, error) {
	return openDatabase(ctx, sqliteFileDSN(databasePath, map[string]string{
		"mode":        "ro",
		"immutable":   "1",
		"_query_only": "1",
		"_dqs":        "false",
		"_defensive":  "true",
	}))
}

func openDatabase(ctx context.Context, dsn string) (*sql.DB, error) {
	connector, err := modernsqlite.NewConnector(dsn)
	if err != nil {
		return nil, fmt.Errorf("create sqlite connector: %w", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}
	return db, nil
}

func sqliteFileDSN(path string, parameters map[string]string) string {
	u := url.URL{Scheme: "file", Path: path}
	query := u.Query()
	for key, value := range parameters {
		query.Set(key, value)
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func verifyRuntime(ctx context.Context, db *sql.DB, projectDir, databasePath string) (RuntimeInfo, error) {
	info := RuntimeInfo{ProjectDir: projectDir, DatabasePath: databasePath}
	var foreignKeys int
	if err := db.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&info.SQLiteVersion); err != nil {
		return RuntimeInfo{}, fmt.Errorf("read sqlite version: %w", err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&info.JournalMode); err != nil {
		return RuntimeInfo{}, fmt.Errorf("read journal_mode: %w", err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return RuntimeInfo{}, fmt.Errorf("read foreign_keys: %w", err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&info.Synchronous); err != nil {
		return RuntimeInfo{}, fmt.Errorf("read synchronous: %w", err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&info.BusyTimeout); err != nil {
		return RuntimeInfo{}, fmt.Errorf("read busy_timeout: %w", err)
	}
	info.JournalMode = strings.ToLower(info.JournalMode)
	info.ForeignKeys = foreignKeys == 1
	if info.SQLiteVersion != expectedSQLiteVersion {
		return RuntimeInfo{}, fmt.Errorf("unexpected sqlite version %q, want %q", info.SQLiteVersion, expectedSQLiteVersion)
	}
	if info.JournalMode != "wal" || !info.ForeignKeys || info.Synchronous != 2 || info.BusyTimeout != 5000 {
		return RuntimeInfo{}, fmt.Errorf("sqlite runtime contract mismatch: %+v", info)
	}
	if err := verifyDatabaseIntegrity(ctx, db); err != nil {
		return RuntimeInfo{}, err
	}
	return info, nil
}

func secureDatabaseFiles(databasePath string) error {
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("secure sqlite file %s: %w", path, err)
		}
	}
	return nil
}
