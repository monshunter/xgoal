// Package project identifies the Git repository that owns one xgoal runtime.
// Resolution is read-only; ownership and binding are explicit write boundaries.
package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	ErrAlreadyRunning        = errors.New("xgoal project already has an active owner")
	ErrBindingMismatch       = errors.New("xgoal project binding mismatch")
	ErrAmbiguousState        = errors.New("multiple historical xgoal state databases belong to this Git repository")
	ErrLinkedWorktree        = errors.New("linked worktree entry is unsupported")
	ErrUnsupportedRepository = errors.New("unsupported Git repository entry")
)

type Paths struct {
	ProjectRoot        string `json:"project_root"`
	WorktreeRoot       string `json:"worktree_root"`
	CommonDir          string `json:"common_dir"`
	ProjectID          string `json:"project_id"`
	RepositoryIdentity string `json:"repository_identity"`
	StateDir           string `json:"state_dir"`
	RunDir             string `json:"run_dir"`
	SocketPath         string `json:"socket_path"`
	LegacyState        bool   `json:"legacy_state"`
}

type binding struct {
	ProjectID   string `json:"project_id"`
	ProjectRoot string `json:"project_root"`
	StateDir    string `json:"state_dir"`
}

// Resolve applies flags, environment, shared binding, then defaults. It never
// creates directories, changes Git config, opens SQLite, or starts a provider.
func Resolve(ctx context.Context, directory, stateDir, socketPath string) (Paths, error) {
	if directory == "" {
		directory = os.Getenv("XGOAL_PROJECT")
	}
	if directory == "" {
		directory = "."
	}
	working, err := canonicalPath(directory)
	if err != nil {
		return Paths{}, err
	}
	root, err := git(ctx, working, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return Paths{}, fmt.Errorf("resolve trusted Git project: %w", err)
	}
	root, err = canonicalPath(root)
	if err != nil {
		return Paths{}, err
	}
	common, err := git(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Paths{}, err
	}
	common, err = canonicalPath(common)
	if err != nil {
		return Paths{}, err
	}
	gitDirectory, err := git(ctx, root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return Paths{}, err
	}
	gitDirectory, err = canonicalPath(gitDirectory)
	if err != nil {
		return Paths{}, err
	}
	superproject, err := git(ctx, root, "rev-parse", "--show-superproject-working-tree")
	if err != nil {
		return Paths{}, err
	}
	if superproject != "" {
		return Paths{}, fmt.Errorf("%w: submodule checkout %s belongs to %s; use a standalone repository", ErrUnsupportedRepository, root, superproject)
	}
	listing, err := git(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return Paths{}, err
	}
	var roots []string
	for _, field := range strings.Split(listing, "\x00") {
		if !strings.HasPrefix(field, "worktree ") {
			continue
		}
		candidate, err := canonicalPath(strings.TrimPrefix(field, "worktree "))
		if err != nil {
			return Paths{}, err
		}
		roots = append(roots, candidate)
	}
	if len(roots) == 0 {
		return Paths{}, errors.New("Git repository has no worktree")
	}
	if root != roots[0] || gitDirectory != common {
		return Paths{}, fmt.Errorf("%w: %s; run xgoal from the main Git working directory %s", ErrLinkedWorktree, root, roots[0])
	}
	identityHash := sha256.Sum256([]byte(common))
	identity := hex.EncodeToString(identityHash[:])
	id, err := configValue(ctx, common, "xgoal.projectID")
	if err != nil {
		return Paths{}, err
	}
	stored, err := readBinding(ctx, common)
	if err != nil {
		return Paths{}, err
	}
	if stored != nil {
		if stored.ProjectRoot != root {
			return Paths{}, fmt.Errorf("%w: saved execution root %s is not the current main Git working directory %s; preserve legacy state at %s and resolve its migration before starting", ErrBindingMismatch, stored.ProjectRoot, root, stored.StateDir)
		}
		if id != "" && id != stored.ProjectID {
			return Paths{}, fmt.Errorf("%w: Git projectID differs from state binding", ErrBindingMismatch)
		}
		id = stored.ProjectID
	}
	if id == "" {
		id = "project_" + identity[:32]
	}
	if !validID(id) {
		return Paths{}, errors.New("invalid xgoal.projectID in Git config")
	}
	projectRoot := root
	defaultState := filepath.Join(projectRoot, ".xgoal")
	var histories []string
	for _, candidate := range roots {
		path := filepath.Join(candidate, ".xgoal")
		if err := rejectSymlink(path); err != nil {
			return Paths{}, err
		}
		if info, err := os.Lstat(filepath.Join(path, "state.db")); err == nil {
			if !info.Mode().IsRegular() {
				return Paths{}, fmt.Errorf("state database is not a regular file: %s", filepath.Join(path, "state.db"))
			}
			if info.Size() == 0 {
				continue
			}
			histories = append(histories, path)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Paths{}, err
		}
	}
	if len(histories) > 1 {
		return Paths{}, fmt.Errorf("%w: %s; stop all daemons and reconcile these paths without merging databases", ErrAmbiguousState, strings.Join(histories, ", "))
	}
	if len(histories) == 1 && histories[0] != defaultState {
		return Paths{}, fmt.Errorf("%w: legacy state exists in linked worktree at %s; preserve it and resolve its migration before starting from %s", ErrBindingMismatch, histories[0], root)
	}
	if stored != nil {
		canonicalRoot, rootErr := canonicalPath(stored.ProjectRoot)
		canonicalState, stateErr := canonicalPath(stored.StateDir)
		if rootErr != nil || stateErr != nil || canonicalRoot != stored.ProjectRoot || canonicalState != stored.StateDir {
			return Paths{}, fmt.Errorf("%w: saved project/state path moved or changed; preserve the old state and restore its registered location", ErrBindingMismatch)
		}
		defaultState = stored.StateDir
		if len(histories) == 1 && histories[0] != defaultState {
			return Paths{}, fmt.Errorf("%w: registered %s conflicts with %s", ErrAmbiguousState, defaultState, histories[0])
		}
	}
	if stateDir == "" {
		stateDir = os.Getenv("XGOAL_STATE_DIR")
	}
	if stateDir == "" {
		stateDir = defaultState
	}
	if !filepath.IsAbs(stateDir) {
		stateDir = filepath.Join(projectRoot, stateDir)
	}
	if err := rejectSymlink(stateDir); err != nil {
		return Paths{}, err
	}
	stateDir, err = canonicalPath(stateDir)
	if err != nil {
		return Paths{}, err
	}
	if stored != nil && stateDir != stored.StateDir {
		return Paths{}, fmt.Errorf("%w: project uses %s, requested %s; a state override cannot create another runtime", ErrBindingMismatch, stored.StateDir, stateDir)
	}
	if stored == nil && len(histories) == 1 && stateDir != histories[0] {
		return Paths{}, fmt.Errorf("%w: historical state exists at %s, requested %s", ErrBindingMismatch, histories[0], stateDir)
	}
	runtimeRoot := os.Getenv("XGOAL_RUNTIME_DIR")
	if runtimeRoot == "" {
		runtimeRoot = filepath.Join("/tmp", fmt.Sprintf("xgoal-%d", os.Getuid()))
	}
	if !filepath.IsAbs(runtimeRoot) {
		return Paths{}, errors.New("XGOAL_RUNTIME_DIR must be absolute")
	}
	runtimeRoot, err = canonicalPath(runtimeRoot)
	if err != nil {
		return Paths{}, err
	}
	runDir := filepath.Join(runtimeRoot, identity[:24])
	if socketPath == "" {
		socketPath = os.Getenv("XGOAL_SOCKET")
	}
	if socketPath == "" {
		socketPath = filepath.Join(runDir, "rpc.sock")
	}
	if !filepath.IsAbs(socketPath) {
		socketPath = filepath.Join(projectRoot, socketPath)
	}
	if err := rejectSymlink(socketPath); err != nil {
		return Paths{}, err
	}
	socketPath, err = canonicalPath(socketPath)
	if err != nil {
		return Paths{}, err
	}
	if len([]byte(socketPath)) > 100 {
		return Paths{}, fmt.Errorf("Unix socket path exceeds portable 100-byte limit: %s; choose a shorter --socket or XGOAL_RUNTIME_DIR", socketPath)
	}
	return Paths{ProjectRoot: projectRoot, WorktreeRoot: root, CommonDir: common, ProjectID: id, RepositoryIdentity: identity, StateDir: stateDir, RunDir: runDir, SocketPath: socketPath, LegacyState: stateDir == filepath.Join(projectRoot, ".xgoal")}, nil
}

func readBinding(ctx context.Context, common string) (*binding, error) {
	raw, err := configValue(ctx, common, "xgoal.stateBinding")
	if err != nil || raw == "" {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	fields := make(map[string]string)
	token, decodeErr := decoder.Token()
	if decodeErr != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("%w: invalid Git xgoal.stateBinding", ErrBindingMismatch)
	}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || name != "project_id" && name != "project_root" && name != "state_dir" {
			return nil, fmt.Errorf("%w: invalid binding field", ErrBindingMismatch)
		}
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("%w: duplicate binding field %s", ErrBindingMismatch, name)
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%w: invalid binding value", ErrBindingMismatch)
		}
		fields[name] = value
	}
	end, endErr := decoder.Token()
	_, trailingErr := decoder.Token()
	value := binding{ProjectID: fields["project_id"], ProjectRoot: fields["project_root"], StateDir: fields["state_dir"]}
	if endErr != nil || end != json.Delim('}') || trailingErr != io.EOF || !validID(value.ProjectID) || !validStoredPath(value.ProjectRoot) || !validStoredPath(value.StateDir) {
		return nil, fmt.Errorf("%w: invalid Git xgoal.stateBinding", ErrBindingMismatch)
	}
	return &value, nil
}

func configValue(ctx context.Context, common, key string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "config", "--file", filepath.Join(common, "config"), "--null", "--get-all", key)
	cmd.Env = GitEnvironment()
	output, err := cmd.Output()
	var status *exec.ExitError
	if errors.As(err, &status) && status.ExitCode() == 1 {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read Git %s: %w", key, err)
	}
	values := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	if len(values) != 1 {
		return "", fmt.Errorf("%w: multiple Git %s values", ErrBindingMismatch, key)
	}
	return values[0], nil
}

func validStoredPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\r\n\x00")
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("sensitive path must not be a symlink: %s", path)
	}
	return nil
}

// canonicalPath resolves existing parents while allowing a missing final path.
func canonicalPath(value string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("path is empty or contains forbidden characters")
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return "", err
	}
	resolved, err = canonicalPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(abs)), nil
}

func validID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "/\\\r\n\x00")
}

func git(ctx context.Context, directory string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	cmd.Env = GitEnvironment()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s: %w", args[0], strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

// GitEnvironment preserves user configuration and credentials while removing
// inherited repository selectors. An explicit project must not be redirected
// by a parent Git hook, IDE, or another repository's process environment.
func GitEnvironment() []string {
	var result []string
	selectors := map[string]bool{"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_COMMON_DIR": true, "GIT_INDEX_FILE": true, "GIT_NAMESPACE": true, "GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !selectors[key] {
			result = append(result, entry)
		}
	}
	return result
}
