package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/gitrepo"
)

const markerVersion = "xgoal.workspace-marker/v1"

type Kind string

const (
	Attempt    Kind = "attempt"
	Validation Kind = "validation"
)

type Spec struct {
	ID         string
	AttemptID  string
	Kind       Kind
	BaseCommit string
	BaseTree   string
	ConfigHash string
}

type Snapshot struct {
	ID         string
	AttemptID  string
	Kind       Kind
	Path       string
	MarkerPath string
	CommonDir  string
	BaseCommit string
	BaseTree   string
	ConfigHash string
	MarkerHash string
	HeadCommit string
	HeadTree   string
	CreatedAt  time.Time
}

// ValidateMarkerBinding verifies the immutable marker identity represented by
// this snapshot without trusting the current worktree HEAD.
func (snapshot Snapshot) ValidateMarkerBinding() error {
	record := marker{
		ProtocolVersion: markerVersion,
		ID:              snapshot.ID,
		AttemptID:       snapshot.AttemptID,
		Kind:            snapshot.Kind,
		Path:            snapshot.Path,
		CommonDir:       snapshot.CommonDir,
		BaseCommit:      snapshot.BaseCommit,
		BaseTree:        snapshot.BaseTree,
		ConfigHash:      snapshot.ConfigHash,
		CreatedAt:       snapshot.CreatedAt,
		MarkerHash:      snapshot.MarkerHash,
	}
	if err := validateMarker(record, snapshot.ID, snapshot.CommonDir); err != nil {
		return err
	}
	if snapshot.MarkerPath != filepath.Join(filepath.Dir(snapshot.Path), "marker.json") {
		return errors.New("workspace snapshot marker path does not match its worktree")
	}
	expected, err := markerHash(record)
	if err != nil {
		return err
	}
	if expected != snapshot.MarkerHash {
		return errors.New("workspace snapshot marker hash mismatch")
	}
	return nil
}

type marker struct {
	ProtocolVersion string    `json:"protocol_version"`
	ID              string    `json:"id"`
	AttemptID       string    `json:"attempt_id"`
	Kind            Kind      `json:"kind"`
	Path            string    `json:"path"`
	CommonDir       string    `json:"common_dir"`
	BaseCommit      string    `json:"base_commit"`
	BaseTree        string    `json:"base_tree"`
	ConfigHash      string    `json:"config_hash"`
	CreatedAt       time.Time `json:"created_at"`
	MarkerHash      string    `json:"marker_hash"`
}

type markerIdentity struct {
	ProtocolVersion string    `json:"protocol_version"`
	ID              string    `json:"id"`
	AttemptID       string    `json:"attempt_id"`
	Kind            Kind      `json:"kind"`
	Path            string    `json:"path"`
	CommonDir       string    `json:"common_dir"`
	BaseCommit      string    `json:"base_commit"`
	BaseTree        string    `json:"base_tree"`
	ConfigHash      string    `json:"config_hash"`
	CreatedAt       time.Time `json:"created_at"`
}

type Manager struct {
	root       string
	repository *gitrepo.Repository
}

func NewManager(runtimeRoot string, repository *gitrepo.Repository) (*Manager, error) {
	if repository == nil || !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot || strings.ContainsAny(runtimeRoot, "\r\n\x00") {
		return nil, errors.New("runtime root must be a clean absolute path and repository is required")
	}
	if err := ensureDirectory(runtimeRoot); err != nil {
		return nil, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(runtimeRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime root: %w", err)
	}
	runtimeRoot = resolvedRoot
	workspaceRoot := filepath.Join(runtimeRoot, "workspaces")
	for _, directory := range []string{workspaceRoot, filepath.Join(workspaceRoot, "attempts"), filepath.Join(workspaceRoot, "validation")} {
		if err := ensureDirectory(directory); err != nil {
			return nil, err
		}
	}
	return &Manager{root: runtimeRoot, repository: repository}, nil
}

func (manager *Manager) Create(ctx context.Context, spec Spec) (Snapshot, error) {
	if !validComponent(spec.ID) || !validComponent(spec.AttemptID) || (spec.Kind != Attempt && spec.Kind != Validation) ||
		spec.BaseCommit == "" || spec.BaseTree == "" || !validHash(spec.ConfigHash) {
		return Snapshot{}, errors.New("invalid workspace spec")
	}
	container, worktreePath, markerPath := manager.paths(spec.Kind, spec.ID)
	if _, err := os.Lstat(container); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return Snapshot{}, fmt.Errorf("workspace %q already exists", spec.ID)
		}
		return Snapshot{}, fmt.Errorf("inspect workspace container: %w", err)
	}
	if err := os.Mkdir(container, 0o700); err != nil {
		return Snapshot{}, fmt.Errorf("create workspace container: %w", err)
	}
	created := false
	defer func() {
		if !created {
			_ = os.Remove(markerPath)
			_ = os.Remove(container)
		}
	}()
	info, err := manager.repository.AddDetachedWorktree(ctx, worktreePath, spec.BaseCommit)
	if err != nil {
		return Snapshot{}, err
	}
	removeWorktree := true
	defer func() {
		if removeWorktree {
			_ = manager.repository.RemoveWorktree(context.Background(), worktreePath)
		}
	}()
	if info.HeadCommit != spec.BaseCommit || info.HeadTree != spec.BaseTree {
		return Snapshot{}, fmt.Errorf("workspace base mismatch: commit/tree %s/%s, want %s/%s", info.HeadCommit, info.HeadTree, spec.BaseCommit, spec.BaseTree)
	}
	now := time.Now().UTC()
	record := marker{
		ProtocolVersion: markerVersion,
		ID:              spec.ID, AttemptID: spec.AttemptID, Kind: spec.Kind,
		Path: info.Path, CommonDir: info.CommonDir,
		BaseCommit: spec.BaseCommit, BaseTree: spec.BaseTree, ConfigHash: spec.ConfigHash,
		CreatedAt: now,
	}
	record.MarkerHash, err = markerHash(record)
	if err != nil {
		return Snapshot{}, err
	}
	encoded, err := canonical.Marshal(record)
	if err != nil {
		return Snapshot{}, fmt.Errorf("encode workspace marker: %w", err)
	}
	if err := writeAtomic(markerPath, encoded); err != nil {
		return Snapshot{}, err
	}
	created = true
	removeWorktree = false
	return snapshotFrom(record, info), nil
}

func (manager *Manager) ReadBack(ctx context.Context, id string) (Snapshot, error) {
	record, err := manager.readMarker(id)
	if err != nil {
		return Snapshot{}, err
	}
	info, err := manager.repository.InspectWorktree(ctx, record.Path)
	if err != nil {
		return Snapshot{}, err
	}
	if info.CommonDir != record.CommonDir {
		return Snapshot{}, errors.New("workspace common directory changed")
	}
	return snapshotFrom(record, info), nil
}

// ReadMarkerSnapshot validates one immutable workspace marker without requiring
// a live Git worktree. Callers can separately inspect the worktree when the
// recorded state is active.
func ReadMarkerSnapshot(markerPath string) (Snapshot, error) {
	if !filepath.IsAbs(markerPath) || filepath.Clean(markerPath) != markerPath {
		return Snapshot{}, errors.New("workspace marker path must be a clean absolute path")
	}
	info, err := os.Lstat(markerPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() > 1<<20 {
		return Snapshot{}, errors.New("workspace marker is missing, linked, oversized, or has unsafe permissions")
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return Snapshot{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record marker
	if err := decoder.Decode(&record); err != nil {
		return Snapshot{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Snapshot{}, errors.New("workspace marker contains trailing data")
	}
	if err := validateMarker(record, record.ID, record.CommonDir); err != nil {
		return Snapshot{}, err
	}
	expected, err := markerHash(record)
	if err != nil || expected != record.MarkerHash {
		return Snapshot{}, errors.New("workspace marker hash mismatch")
	}
	snapshot := snapshotFrom(record, gitrepo.WorktreeInfo{})
	if snapshot.MarkerPath != markerPath {
		return Snapshot{}, errors.New("workspace marker path does not match its bound worktree")
	}
	return snapshot, nil
}

func (manager *Manager) Cleanup(ctx context.Context, id string) error {
	record, err := manager.readMarker(id)
	if err != nil {
		return err
	}
	_, expectedPath, markerPath := manager.paths(record.Kind, id)
	if record.Path != expectedPath {
		return errors.New("workspace marker path is outside its bound container")
	}
	if err := manager.repository.RemoveWorktree(ctx, expectedPath); err != nil {
		return err
	}
	if err := os.Remove(markerPath); err != nil {
		return fmt.Errorf("remove workspace marker: %w", err)
	}
	container := filepath.Dir(markerPath)
	if err := os.Remove(container); err != nil {
		return fmt.Errorf("remove empty workspace container: %w", err)
	}
	return syncDirectory(filepath.Dir(container))
}

func (manager *Manager) readMarker(id string) (marker, error) {
	if !validComponent(id) {
		return marker{}, errors.New("invalid workspace id")
	}
	var found []string
	for _, kind := range []Kind{Attempt, Validation} {
		_, _, markerPath := manager.paths(kind, id)
		if _, err := os.Lstat(markerPath); err == nil {
			found = append(found, markerPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return marker{}, fmt.Errorf("inspect workspace marker: %w", err)
		}
	}
	if len(found) != 1 {
		return marker{}, fmt.Errorf("workspace %q has %d markers, want 1", id, len(found))
	}
	info, err := os.Lstat(found[0])
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return marker{}, errors.New("workspace marker is missing, linked, or has unsafe permissions")
	}
	data, err := os.ReadFile(found[0])
	if err != nil {
		return marker{}, fmt.Errorf("read workspace marker: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record marker
	if err := decoder.Decode(&record); err != nil {
		return marker{}, fmt.Errorf("decode workspace marker: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return marker{}, errors.New("workspace marker contains multiple JSON values")
		}
		return marker{}, fmt.Errorf("decode workspace marker trailing data: %w", err)
	}
	if err := validateMarker(record, id, manager.repository.CommonDir()); err != nil {
		return marker{}, err
	}
	expectedHash, err := markerHash(record)
	if err != nil {
		return marker{}, err
	}
	if record.MarkerHash != expectedHash {
		return marker{}, errors.New("workspace marker hash mismatch")
	}
	return record, nil
}

func (manager *Manager) paths(kind Kind, id string) (container, worktreePath, markerPath string) {
	directory := "attempts"
	if kind == Validation {
		directory = "validation"
	}
	container = filepath.Join(manager.root, "workspaces", directory, id)
	return container, filepath.Join(container, "tree"), filepath.Join(container, "marker.json")
}

func validateMarker(record marker, id, commonDir string) error {
	if record.ProtocolVersion != markerVersion || record.ID != id || !validComponent(record.ID) || !validComponent(record.AttemptID) ||
		(record.Kind != Attempt && record.Kind != Validation) || record.CommonDir != commonDir || !filepath.IsAbs(record.Path) ||
		record.BaseCommit == "" || record.BaseTree == "" || !validHash(record.ConfigHash) || record.CreatedAt.IsZero() || !validHash(record.MarkerHash) {
		return errors.New("workspace marker violates its contract")
	}
	return nil
}

func markerHash(record marker) (string, error) {
	return canonical.Hash("workspace-marker", markerVersion, markerIdentity{
		ProtocolVersion: record.ProtocolVersion,
		ID:              record.ID, AttemptID: record.AttemptID, Kind: record.Kind,
		Path: record.Path, CommonDir: record.CommonDir,
		BaseCommit: record.BaseCommit, BaseTree: record.BaseTree, ConfigHash: record.ConfigHash,
		CreatedAt: record.CreatedAt,
	})
}

func snapshotFrom(record marker, info gitrepo.WorktreeInfo) Snapshot {
	return Snapshot{
		ID: record.ID, AttemptID: record.AttemptID, Kind: record.Kind,
		Path: record.Path, MarkerPath: filepath.Join(filepath.Dir(record.Path), "marker.json"), CommonDir: record.CommonDir,
		BaseCommit: record.BaseCommit, BaseTree: record.BaseTree, ConfigHash: record.ConfigHash, MarkerHash: record.MarkerHash,
		HeadCommit: info.HeadCommit, HeadTree: info.HeadTree, CreatedAt: record.CreatedAt,
	}
}

func ensureDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create private workspace directory: %w", err)
		}
		info, err = os.Lstat(directory)
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("workspace directory is missing or unsafe")
	}
	return os.Chmod(directory, 0o700)
}

func writeAtomic(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".marker-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary workspace marker: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := true
	defer func() {
		_ = temporary.Close()
		if keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write workspace marker: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync workspace marker: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close workspace marker: %w", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("workspace marker target already exists or cannot be inspected")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish workspace marker: %w", err)
	}
	keep = false
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func validComponent(value string) bool {
	if value == "" || value == "." || value == ".." || !utf8.ValidString(value) || strings.ContainsAny(value, "/\\\r\n\x00") {
		return false
	}
	return true
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
