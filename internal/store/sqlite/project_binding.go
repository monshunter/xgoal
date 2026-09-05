package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/monshunter/xgoal/internal/clock"
	"golang.org/x/sys/unix"
)

var ErrProjectBinding = errors.New("sqlite project binding mismatch")

const projectBindingKey = "project_binding"

// ProjectBinding records the repository that owns a database. It is immutable;
// moving a repository or importing its history requires an explicit operation.
type ProjectBinding struct {
	ProjectID   string `json:"project_id"`
	CommonDir   string `json:"common_dir"`
	ProjectRoot string `json:"project_root"`
}

func (binding ProjectBinding) valid() bool {
	return validIdempotencyLabel(binding.ProjectID) && cleanBindingPath(binding.CommonDir) && cleanBindingPath(binding.ProjectRoot)
}

func cleanBindingPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\r\n\x00")
}

// CheckProjectBinding runs before Open can create or migrate SQLite. The caller
// must hold both repository and state ownership through this check and Open.
func CheckProjectBinding(ctx context.Context, stateDir string, binding ProjectBinding, allowLegacy bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !cleanBindingPath(stateDir) {
		return fmt.Errorf("%w: state directory must be clean and absolute", ErrUnsafePath)
	}
	if !binding.valid() {
		return fmt.Errorf("%w: invalid project identity", ErrProjectBinding)
	}
	if info, err := os.Lstat(stateDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: state is not a real directory", ErrUnsafePath)
		}
		if err := checkBindingFileOwner(stateDir); err != nil {
			return err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		return nil
	} else {
		return err
	}
	databasePath := filepath.Join(stateDir, databaseFilename)
	var existing bool
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: database or sidecar is not a regular file: %s", ErrUnsafePath, path)
		}
		if err := checkBindingFileOwner(path); err != nil {
			return err
		}
		if path == databasePath {
			existing = info.Size() > 0
		}
	}
	legacyDefault := allowLegacy && stateDir == filepath.Join(binding.ProjectRoot, ".xgoal")
	if !existing {
		if !legacyDefault {
			if err := checkEmptyUnboundState(stateDir); err != nil {
				return err
			}
		}
		return checkLegacyPlannerPaths(ctx, stateDir, binding)
	}
	db, err := openLiveReadOnlyDatabase(ctx, databasePath)
	if err != nil {
		return fmt.Errorf("preflight project database: %w", err)
	}
	defer db.Close()
	stored, err := readProjectBinding(ctx, db)
	if err == nil {
		if stored != binding {
			return fmt.Errorf("%w: database belongs to project %q at %s, requested %q at %s", ErrProjectBinding, stored.ProjectID, stored.CommonDir, binding.ProjectID, binding.CommonDir)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !legacyDefault {
		return fmt.Errorf("%w: nonempty external state has no project identity; preserve it and use its original repository/default state", ErrProjectBinding)
	}
	if err := checkLegacyWorkspacePaths(ctx, db, stateDir, binding); err != nil {
		return err
	}
	return checkLegacyPlannerPaths(ctx, stateDir, binding)
}

// OpenProject preserves Open's migration/backup guarantees and installs the
// binding with an insert-only transaction. The daemon owns the external locks.
func OpenProject(ctx context.Context, stateDir string, source clock.Clock, binding ProjectBinding, allowLegacy bool) (*Store, error) {
	if err := CheckProjectBinding(ctx, stateDir, binding, allowLegacy); err != nil {
		return nil, err
	}
	store, err := Open(ctx, stateDir, source)
	if err != nil {
		return nil, err
	}
	err = store.withTransaction(ctx, func(tx *sql.Tx) error {
		stored, err := readProjectBinding(ctx, tx)
		if err == nil {
			if stored != binding {
				return fmt.Errorf("%w: database identity changed during open", ErrProjectBinding)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		raw, err := json.Marshal(binding)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO store_metadata(key,value) VALUES (?,?)`, projectBindingKey, raw)
		return err
	})
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	return store, nil
}

func readProjectBinding(ctx context.Context, reader rowQueryer) (ProjectBinding, error) {
	var raw []byte
	if err := reader.QueryRowContext(ctx, `SELECT value FROM store_metadata WHERE key = ?`, projectBindingKey).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectBinding{}, err
		}
		return ProjectBinding{}, fmt.Errorf("%w: read database identity: %v", ErrProjectBinding, err)
	}
	var binding ProjectBinding
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return binding, fmt.Errorf("%w: invalid identity JSON", ErrProjectBinding)
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return binding, fmt.Errorf("%w: invalid or duplicate identity field", ErrProjectBinding)
		}
		seen[key] = true
		var target *string
		switch key {
		case "project_id":
			target = &binding.ProjectID
		case "common_dir":
			target = &binding.CommonDir
		case "project_root":
			target = &binding.ProjectRoot
		default:
			return binding, fmt.Errorf("%w: unknown identity field %q", ErrProjectBinding, key)
		}
		if err := decoder.Decode(target); err != nil {
			return binding, fmt.Errorf("%w: invalid identity field %q", ErrProjectBinding, key)
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return binding, fmt.Errorf("%w: incomplete identity JSON", ErrProjectBinding)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || !binding.valid() {
		return binding, fmt.Errorf("%w: invalid or trailing identity JSON", ErrProjectBinding)
	}
	return binding, nil
}

func checkBindingFileOwner(path string) error {
	var info unix.Stat_t
	if err := unix.Lstat(path, &info); err != nil {
		return err
	}
	if info.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("%w: state file belongs to another user: %s", ErrUnsafePath, path)
	}
	if info.Mode&unix.S_IFMT == unix.S_IFREG && info.Nlink != 1 {
		return fmt.Errorf("%w: state file has multiple hard links: %s", ErrUnsafePath, path)
	}
	return nil
}

func checkEmptyUnboundState(stateDir string) error {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == databaseFilename {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() && info.Size() == 0 {
				continue
			}
		}
		if entry.Name() == "run" && entry.IsDir() {
			locks, err := os.ReadDir(filepath.Join(stateDir, "run"))
			if err != nil {
				return err
			}
			valid := true
			for _, lock := range locks {
				if lock.Name() != "daemon.lock" {
					valid = false
				}
			}
			if valid {
				continue
			}
		}
		return fmt.Errorf("%w: nonempty external state has no database identity: %s", ErrProjectBinding, stateDir)
	}
	return nil
}

func checkLegacyWorkspacePaths(ctx context.Context, db *sql.DB, stateDir string, binding ProjectBinding) error {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workspaces'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT id,kind,path,common_dir FROM workspaces`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, path, common string
		if err := rows.Scan(&id, &kind, &path, &common); err != nil {
			return err
		}
		directory := "attempts"
		if kind == "VALIDATION" {
			directory = "validation"
		}
		if common != binding.CommonDir || path != filepath.Join(stateDir, "workspaces", directory, id, "tree") {
			return fmt.Errorf("%w: legacy workspace %q belongs to %s at %s; retain the original state paths", ErrProjectBinding, id, common, path)
		}
	}
	return rows.Err()
}

func checkLegacyPlannerPaths(ctx context.Context, stateDir string, binding ProjectBinding) error {
	root := filepath.Join(stateDir, "planner")
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: linked legacy Planner artifact: %s", ErrProjectBinding, path)
		}
		if entry.IsDir() || entry.Name() != "packet.json" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 8<<20 {
			return fmt.Errorf("%w: invalid legacy Planner packet", ErrProjectBinding)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var packet struct {
			ProjectRoot string `json:"project_root"`
		}
		if err := json.Unmarshal(raw, &packet); err != nil || packet.ProjectRoot != binding.ProjectRoot {
			return fmt.Errorf("%w: legacy Planner packet has another or invalid project root: %s", ErrProjectBinding, path)
		}
		return nil
	})
}
