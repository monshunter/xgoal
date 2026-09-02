package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
)

type migrationBackup struct {
	SourceVersion int
	TargetVersion int
	Filename      string
	SHA256        string
	CreatedAt     string
}

func createMigrationBackup(
	ctx context.Context,
	db *sql.DB,
	backupDir string,
	sourceVersion, targetVersion int,
	source clock.Clock,
) (*migrationBackup, error) {
	createdAt := source.Now().UTC()
	stem := fmt.Sprintf(
		"pre-migration-v%04d-to-v%04d-%s",
		sourceVersion,
		targetVersion,
		createdAt.Format("20060102T150405.000000000Z"),
	)
	finalPath, err := uniqueBackupPath(backupDir, stem)
	if err != nil {
		return nil, err
	}
	temporaryPath := finalPath + ".tmp"
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, temporaryPath); err != nil {
		return nil, fmt.Errorf("vacuum database into temporary backup: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return nil, fmt.Errorf("secure temporary backup: %w", err)
	}
	backupDB, err := openReadOnlyDatabase(ctx, temporaryPath)
	if err != nil {
		return nil, fmt.Errorf("open temporary backup read-only: %w", err)
	}
	if err := verifyDatabaseIntegrity(ctx, backupDB); err != nil {
		_ = backupDB.Close()
		return nil, fmt.Errorf("verify temporary backup: %w", err)
	}
	version, err := readSchemaVersion(ctx, backupDB)
	if err != nil {
		_ = backupDB.Close()
		return nil, fmt.Errorf("read temporary backup version: %w", err)
	}
	if version != sourceVersion {
		_ = backupDB.Close()
		return nil, fmt.Errorf("temporary backup schema version %d, want %d", version, sourceVersion)
	}
	if err := backupDB.Close(); err != nil {
		return nil, fmt.Errorf("close temporary backup: %w", err)
	}
	if err := syncFile(temporaryPath); err != nil {
		return nil, err
	}
	hash, err := hashFile(temporaryPath)
	if err != nil {
		return nil, err
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return nil, fmt.Errorf("publish migration backup: %w", err)
	}
	if err := syncDirectory(backupDir); err != nil {
		return nil, err
	}
	return &migrationBackup{
		SourceVersion: sourceVersion,
		TargetVersion: targetVersion,
		Filename:      filepath.Base(finalPath),
		SHA256:        hash,
		CreatedAt:     createdAt.Format(time.RFC3339Nano),
	}, nil
}

func uniqueBackupPath(backupDir, stem string) (string, error) {
	for sequence := 0; sequence < 10000; sequence++ {
		name := stem
		if sequence > 0 {
			name = fmt.Sprintf("%s-%04d", stem, sequence)
		}
		path := filepath.Join(backupDir, name+".db")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			if _, err := os.Lstat(path + ".tmp"); errors.Is(err, os.ErrNotExist) {
				return path, nil
			} else if err != nil {
				return "", fmt.Errorf("inspect temporary backup path: %w", err)
			}
		} else if err != nil {
			return "", fmt.Errorf("inspect backup path: %w", err)
		}
	}
	return "", errors.New("cannot allocate a unique migration backup path")
}

func recoverInterruptedBackups(backupDir string) error {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return fmt.Errorf("read migration backup directory: %w", err)
	}
	changed := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		source := filepath.Join(backupDir, entry.Name())
		target, err := uniqueIncompletePath(source)
		if err != nil {
			return err
		}
		if err := os.Rename(source, target); err != nil {
			return fmt.Errorf("preserve interrupted backup %s: %w", entry.Name(), err)
		}
		changed = true
	}
	if changed {
		return syncDirectory(backupDir)
	}
	return nil
}

func uniqueIncompletePath(source string) (string, error) {
	for sequence := 0; sequence < 10000; sequence++ {
		target := source + ".incomplete"
		if sequence > 0 {
			target = fmt.Sprintf("%s.incomplete-%04d", source, sequence)
		}
		if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
			return target, nil
		} else if err != nil {
			return "", fmt.Errorf("inspect incomplete backup path: %w", err)
		}
	}
	return "", errors.New("cannot allocate a unique incomplete backup path")
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file for sync %s: %w", path, err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync file %s: %w", path, err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync %s: %w", path, err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync directory %s: %w", path, err)
	}
	return nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open backup for hashing: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash backup: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
