// Package exporter produces a local, non-executable audit snapshot. SQLite is
// still the authority; exported JSON and files are projections of that snapshot.
package exporter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/monshunter/xgoal/internal/invocation"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

const Version = "xgoal.audit-export/v1"
const maxTotalBytes int64 = 2 << 30
const maxFileBytes int64 = 128 << 20

type File struct {
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	SourceSHA256 string `json:"source_sha256,omitempty"`
	Size         int64  `json:"size"`
	Mode         uint32 `json:"mode"`
	Source       string `json:"source"`
}
type LogBoundary struct {
	ID     string `json:"invocation_id"`
	Stdout int64  `json:"stdout_durable_cursor"`
	Stderr int64  `json:"stderr_durable_cursor"`
	Status string `json:"status"`
}
type Owner struct {
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	State          string `json:"state,omitempty"`
	ArtifactStatus string `json:"artifact_status,omitempty"`
}
type Manifest struct {
	ProtocolVersion string                  `json:"protocol_version"`
	Status          string                  `json:"status"`
	GoalID          string                  `json:"focus_goal_id"`
	Scope           string                  `json:"scope"`
	RuntimeRoot     string                  `json:"source_runtime_root"`
	Boundary        sqlite.SnapshotBoundary `json:"database_boundary"`
	Logs            []LogBoundary           `json:"logs"`
	Owners          []Owner                 `json:"owners"`
	Files           []File                  `json:"files"`
	Exclusions      []string                `json:"exclusions"`
}
type Result struct {
	Status         string `json:"status"`
	Path           string `json:"path"`
	ManifestSHA256 string `json:"manifest_sha256"`
	Files          int    `json:"files"`
}
type IncompleteError struct {
	Path  string
	Cause error
}

func (e *IncompleteError) Error() string {
	return fmt.Sprintf("export incomplete at %s: %v", e.Path, e.Cause)
}
func (e *IncompleteError) Unwrap() error { return e.Cause }

// Export is called while holding the control service's cleanup lock. It never
// holds a live Store connection during artifact IO. Destination must not exist.
func Export(ctx context.Context, live *sqlite.Store, projectRoot, goalID, destination string) (result Result, retErr error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination || !invocation.Component(filepath.Base(destination)) {
		return result, errors.New("export output must be a clean absolute directory path")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return result, err
	}
	if parent != filepath.Dir(destination) {
		return result, errors.New("export parent must not contain symlinks")
	}
	for _, root := range []string{projectRoot, live.Info().ProjectDir} {
		if rel, e := filepath.Rel(root, destination); e != nil || rel == "." || filepath.IsLocal(rel) {
			return result, errors.New("export output must be outside the project checkout and state directory")
		}
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return result, errors.New("export output already exists or cannot be inspected")
	}
	temporary, err := os.MkdirTemp(parent, "."+filepath.Base(destination)+".incomplete-")
	if err != nil {
		return result, err
	}
	defer func() {
		if retErr != nil {
			retErr = &IncompleteError{Path: temporary, Cause: retErr}
		}
	}()
	e := &writer{ctx: ctx, source: live.Info().ProjectDir, root: temporary, files: map[string]File{}, refs: map[string]sqlite.SnapshotArtifact{}}
	if err := os.Mkdir(filepath.Join(temporary, "snapshot"), 0700); err != nil {
		return result, err
	}
	snap, boundary, err := live.ReadSnapshot(ctx, filepath.Join(temporary, "snapshot", "state.db"))
	if err != nil {
		return result, err
	}
	defer snap.Close()
	status, err := snap.GoalStatus(ctx, goalID)
	if err != nil {
		return result, err
	}
	references, err := snap.SnapshotArtifacts(ctx)
	if err != nil {
		return result, err
	}
	for _, a := range references {
		e.refs[a.Kind+":"+a.ID] = a
	}
	m := Manifest{ProtocolVersion: Version, Status: "complete", GoalID: goalID, Scope: "all project database rows and their sealed artifact references", RuntimeRoot: e.source, Boundary: boundary, Logs: []LogBoundary{}, Owners: []Owner{}, Exclusions: []string{
		"Project source and Git objects; source paths and tree hashes remain references, not copied inputs.",
		"Writable workspaces, private indexes, processes, sockets, locks, environment runtime data and unsealed outputs.",
		"Host credentials and native Provider session databases.",
		"Provider metadata/result files without a frozen file hash; frozen invocation inputs and observed results remain in SQLite.",
		"Legacy unindexed Provider streams and events newer than each frozen durable cursor; exported indexed logs are public, redacted projections.",
	}}
	for _, a := range references {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := e.artifact(a, &m); err != nil {
			return result, fmt.Errorf("%s %s: %w", a.Kind, a.ID, err)
		}
		m.Owners = append(m.Owners, Owner{Kind: a.Kind, ID: a.ID, State: a.State, ArtifactStatus: e.artifactStatus[a.Kind+":"+a.ID]})
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return result, err
	}
	if err := e.write("data/goal.json", data, 0600, "database projection", ""); err != nil {
		return result, err
	}
	if err := snap.Close(); err != nil {
		return result, err
	}
	db, info, err := invocation.ReadFile(temporary, "snapshot/state.db", 512<<20)
	if err != nil {
		return result, err
	}
	if err := e.record("snapshot/state.db", db, info.Mode(), "consistent SQLite snapshot", ""); err != nil {
		return result, err
	}
	for _, f := range e.files {
		m.Files = append(m.Files, f)
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return result, err
	}
	// The completion manifest is the last file, after all copied bytes validate.
	if err := e.write("manifest.json", encoded, 0600, "manifest", ""); err != nil {
		return result, err
	}
	if err := syncDirectories(temporary); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// No check-then-rename overwrite window, including an empty directory that
	// another process creates after our initial preflight.
	if err := renameExclusive(temporary, destination); err != nil {
		return result, err
	}
	temporary = destination
	if err := syncDir(parent); err != nil {
		return result, err
	}
	return Result{Status: "complete", Path: destination, ManifestSHA256: invocation.SHA256(encoded), Files: len(m.Files)}, nil
}

type writer struct {
	ctx            context.Context
	source, root   string
	files          map[string]File
	refs           map[string]sqlite.SnapshotArtifact
	artifactStatus map[string]string
	total          int64
}

func (e *writer) relative(path string) (string, error) {
	if filepath.IsAbs(path) {
		var err error
		path, err = filepath.Rel(e.source, path)
		if err != nil {
			return "", err
		}
	}
	if !invocation.Relative(path) {
		return "", errors.New("artifact escapes runtime root")
	}
	return path, nil
}
func (e *writer) copy(path, hash string, limit int64) ([]byte, error) {
	rel, err := e.relative(path)
	if err != nil {
		return nil, err
	}
	destination := filepath.Join("files", rel)
	if f, ok := e.files[destination]; ok {
		data, _, err := invocation.ReadFile(e.root, destination, limit)
		if err != nil || (hash != "" && f.SHA256 != hash) {
			return nil, errors.New("conflicting artifact references")
		}
		return data, nil
	}
	if err := e.ctx.Err(); err != nil {
		return nil, err
	}
	data, info, err := invocation.ReadFile(e.source, rel, limit)
	if err != nil {
		return nil, err
	}
	if hash != "" && invocation.SHA256(data) != hash {
		return nil, errors.New("artifact byte hash mismatch")
	}
	if err := e.write(destination, data, info.Mode().Perm(), "runtime:"+rel, ""); err != nil {
		return nil, err
	}
	return data, nil
}
func (e *writer) write(path string, data []byte, mode os.FileMode, source, sourceHash string) error {
	if err := e.record(path, data, mode, source, sourceHash); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(e.root, path)), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(e.root, path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	return errors.Join(writeErr, f.Sync(), f.Close())
}
func (e *writer) record(path string, data []byte, mode os.FileMode, source, sourceHash string) error {
	if !invocation.Relative(path) || mode.Perm()&0077 != 0 {
		return errors.New("unsafe export file")
	}
	if _, exists := e.files[path]; exists {
		return errors.New("duplicate export file")
	}
	e.total += int64(len(data))
	if len(e.files) >= 100000 || e.total > maxTotalBytes {
		return errors.New("export size or file count limit exceeded")
	}
	e.files[path] = File{Path: path, SHA256: invocation.SHA256(data), SourceSHA256: sourceHash, Size: int64(len(data)), Mode: uint32(mode.Perm()), Source: source}
	return nil
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func syncDirectories(root string) error {
	var dirs []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := syncDir(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}
