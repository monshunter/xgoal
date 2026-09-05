// Package workspace records immutable execution sessions in the project's current checkout.
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

const (
	legacyMarkerVersion       = "xgoal.workspace-marker/v1"
	markerVersion             = "xgoal.workspace-marker/v2"
	ExecutionCurrentDirectory = "current-directory"
	ExecutionLegacyWorktree   = "git-worktree"
)

var ErrLegacyWorkspace = errors.New("WORKSPACE_MIGRATION_REQUIRED: historical worktree sessions are read-only; retain their original files")

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
	ID             string
	AttemptID      string
	Kind           Kind
	Path           string
	MarkerPath     string
	CommonDir      string
	BaseCommit     string
	BaseTree       string
	ConfigHash     string
	MarkerHash     string
	HeadCommit     string
	HeadTree       string
	CreatedAt      time.Time
	ExecutionModel string
	Identity       gitrepo.CheckoutIdentity
	ExcludePaths   []string
	InputTree      string
}

type marker struct {
	ProtocolVersion string                    `json:"protocol_version"`
	ID              string                    `json:"id"`
	AttemptID       string                    `json:"attempt_id"`
	Kind            Kind                      `json:"kind"`
	Path            string                    `json:"path"`
	CommonDir       string                    `json:"common_dir"`
	BaseCommit      string                    `json:"base_commit"`
	BaseTree        string                    `json:"base_tree"`
	ConfigHash      string                    `json:"config_hash"`
	CreatedAt       time.Time                 `json:"created_at"`
	MarkerHash      string                    `json:"marker_hash"`
	ExecutionModel  string                    `json:"execution_model,omitempty"`
	MarkerPath      string                    `json:"marker_path,omitempty"`
	Identity        *gitrepo.CheckoutIdentity `json:"checkout_identity,omitempty"`
	ExcludePaths    []string                  `json:"exclude_paths,omitempty"`
	InputTree       string                    `json:"input_tree,omitempty"`
}

type markerIdentity struct {
	ProtocolVersion string                    `json:"protocol_version"`
	ID              string                    `json:"id"`
	AttemptID       string                    `json:"attempt_id"`
	Kind            Kind                      `json:"kind"`
	Path            string                    `json:"path"`
	CommonDir       string                    `json:"common_dir"`
	BaseCommit      string                    `json:"base_commit"`
	BaseTree        string                    `json:"base_tree"`
	ConfigHash      string                    `json:"config_hash"`
	CreatedAt       time.Time                 `json:"created_at"`
	ExecutionModel  string                    `json:"execution_model,omitempty"`
	MarkerPath      string                    `json:"marker_path,omitempty"`
	Identity        *gitrepo.CheckoutIdentity `json:"checkout_identity,omitempty"`
	ExcludePaths    []string                  `json:"exclude_paths,omitempty"`
	InputTree       string                    `json:"input_tree,omitempty"`
}

func (snapshot Snapshot) marker() marker {
	record := marker{ProtocolVersion: legacyMarkerVersion, ID: snapshot.ID, AttemptID: snapshot.AttemptID, Kind: snapshot.Kind, Path: snapshot.Path, CommonDir: snapshot.CommonDir, BaseCommit: snapshot.BaseCommit, BaseTree: snapshot.BaseTree, ConfigHash: snapshot.ConfigHash, CreatedAt: snapshot.CreatedAt, MarkerHash: snapshot.MarkerHash}
	if snapshot.ExecutionModel == ExecutionCurrentDirectory {
		identity := snapshot.Identity
		record.ProtocolVersion = markerVersion
		record.ExecutionModel = ExecutionCurrentDirectory
		record.MarkerPath = snapshot.MarkerPath
		record.Identity = &identity
		record.ExcludePaths = snapshot.ExcludePaths
		record.InputTree = snapshot.InputTree
	}
	return record
}

// ValidateMarkerBinding validates historical or current identity without executing Git.
func (snapshot Snapshot) ValidateMarkerBinding() error {
	record := snapshot.marker()
	if snapshot.ExecutionModel != "" && snapshot.ExecutionModel != ExecutionLegacyWorktree && snapshot.ExecutionModel != ExecutionCurrentDirectory {
		return errors.New("unknown workspace execution model")
	}
	if err := validateMarker(record, snapshot.ID, snapshot.CommonDir); err != nil {
		return err
	}
	if snapshot.MarkerPath != boundMarkerPath(record) {
		return errors.New("workspace snapshot marker path mismatch")
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

type Manager struct {
	root       string
	repository *gitrepo.Repository
}

func NewManager(runtimeRoot string, repository *gitrepo.Repository) (*Manager, error) {
	if repository == nil || !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot || strings.ContainsAny(runtimeRoot, "\r\n\x00") {
		return nil, errors.New("runtime root must be clean absolute and repository is required")
	}
	if err := ensureDirectory(runtimeRoot); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(runtimeRoot)
	if err != nil {
		return nil, err
	}
	for _, directory := range []string{filepath.Join(resolved, "workspaces"), filepath.Join(resolved, "workspaces", "attempts"), filepath.Join(resolved, "workspaces", "validation")} {
		if err := ensureDirectory(directory); err != nil {
			return nil, err
		}
	}
	return &Manager{root: resolved, repository: repository}, nil
}

func (manager *Manager) Create(ctx context.Context, spec Spec) (Snapshot, error) {
	if !validComponent(spec.ID) || !validComponent(spec.AttemptID) || (spec.Kind != Attempt && spec.Kind != Validation) || spec.BaseCommit == "" || spec.BaseTree == "" || !validHash(spec.ConfigHash) {
		return Snapshot{}, errors.New("invalid workspace spec")
	}
	base, err := manager.repository.ResolveRevision(ctx, spec.BaseCommit)
	if err != nil {
		return Snapshot{}, err
	}
	if base.Commit != spec.BaseCommit || base.Tree != spec.BaseTree {
		return Snapshot{}, errors.New("workspace base commit/tree mismatch")
	}
	captured, err := manager.repository.SnapshotTree(ctx, gitrepo.SnapshotSpec{BaseTree: spec.BaseTree, ExcludePaths: []string{manager.root}, MaxFileBytes: 64 << 20})
	if err != nil {
		return Snapshot{}, err
	}
	container, _, markerPath := manager.paths(spec.Kind, spec.ID)
	if err := os.Mkdir(container, 0700); err != nil {
		return Snapshot{}, fmt.Errorf("create workspace metadata: %w", err)
	}
	created := false
	defer func() {
		if !created {
			_ = os.Remove(markerPath)
			_ = os.Remove(container)
		}
	}()
	record := marker{ProtocolVersion: markerVersion, ID: spec.ID, AttemptID: spec.AttemptID, Kind: spec.Kind, Path: manager.repository.Root(), CommonDir: manager.repository.CommonDir(), BaseCommit: spec.BaseCommit, BaseTree: spec.BaseTree, ConfigHash: spec.ConfigHash, CreatedAt: time.Now().UTC(), ExecutionModel: ExecutionCurrentDirectory, MarkerPath: markerPath, Identity: &captured.Identity, ExcludePaths: []string{manager.root}, InputTree: captured.Tree}
	record.MarkerHash, err = markerHash(record)
	if err != nil {
		return Snapshot{}, err
	}
	encoded, err := canonical.Marshal(record)
	if err != nil {
		return Snapshot{}, err
	}
	if err := writeAtomic(markerPath, encoded); err != nil {
		return Snapshot{}, err
	}
	created = true
	return snapshotFrom(record), nil
}

func (manager *Manager) ReadBack(ctx context.Context, id string) (Snapshot, error) {
	record, err := manager.readMarker(id)
	if err != nil {
		return Snapshot{}, err
	}
	if record.ExecutionModel != ExecutionCurrentDirectory {
		return Snapshot{}, ErrLegacyWorkspace
	}
	if record.Path != manager.repository.Root() {
		return Snapshot{}, errors.New("workspace execution root changed")
	}
	if err := manager.repository.CheckCheckoutIdentity(ctx, *record.Identity); err != nil {
		return Snapshot{}, err
	}
	return snapshotFrom(record), nil
}

// ReadMarkerSnapshot preserves v1 historical hashes without requiring the old code directory.
func ReadMarkerSnapshot(markerPath string) (Snapshot, error) {
	if !filepath.IsAbs(markerPath) || filepath.Clean(markerPath) != markerPath {
		return Snapshot{}, errors.New("workspace marker path must be clean and absolute")
	}
	info, err := os.Lstat(markerPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1<<20 {
		return Snapshot{}, errors.New("workspace marker is missing, linked, oversized or has unsafe permissions")
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
	if boundMarkerPath(record) != markerPath {
		return Snapshot{}, errors.New("workspace marker path does not match identity")
	}
	return snapshotFrom(record), nil
}

func (manager *Manager) Cleanup(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := manager.readMarker(id)
	if err != nil {
		return err
	}
	if record.ExecutionModel != ExecutionCurrentDirectory {
		return ErrLegacyWorkspace
	}
	container, _, markerPath := manager.paths(record.Kind, id)
	if record.Path != manager.repository.Root() || boundMarkerPath(record) != markerPath {
		return errors.New("workspace cleanup identity mismatch")
	}
	// Metadata-only removal. Unknown files make os.Remove fail; no recursive deletion.
	entries, err := os.ReadDir(container)
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Name() != "marker.json" {
		return errors.New("workspace metadata contains unexpected files; preserve it")
	}
	if err := os.Remove(markerPath); err != nil {
		return err
	}
	if err := os.Remove(container); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(container))
}

func (manager *Manager) readMarker(id string) (marker, error) {
	if !validComponent(id) {
		return marker{}, errors.New("invalid workspace id")
	}
	var paths []string
	for _, kind := range []Kind{Attempt, Validation} {
		_, _, path := manager.paths(kind, id)
		if _, err := os.Lstat(path); err == nil {
			paths = append(paths, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return marker{}, err
		}
	}
	if len(paths) != 1 {
		return marker{}, fmt.Errorf("workspace %q has %d markers, want 1", id, len(paths))
	}
	snapshot, err := ReadMarkerSnapshot(paths[0])
	if err != nil {
		return marker{}, err
	}
	if snapshot.CommonDir != manager.repository.CommonDir() {
		return marker{}, errors.New("workspace common directory changed")
	}
	record := snapshot.marker()
	_, _, expected := manager.paths(record.Kind, id)
	if paths[0] != expected {
		return marker{}, errors.New("workspace kind or id does not match metadata container")
	}
	return record, nil
}

func (manager *Manager) paths(kind Kind, id string) (container, executionPath, markerPath string) {
	directory := "attempts"
	if kind == Validation {
		directory = "validation"
	}
	container = filepath.Join(manager.root, "workspaces", directory, id)
	return container, manager.repository.Root(), filepath.Join(container, "marker.json")
}
func boundMarkerPath(record marker) string {
	if record.ProtocolVersion == markerVersion {
		return record.MarkerPath
	}
	return filepath.Join(filepath.Dir(record.Path), "marker.json")
}
func validateMarker(record marker, id, commonDir string) error {
	if (record.ProtocolVersion != markerVersion && record.ProtocolVersion != legacyMarkerVersion) || record.ID != id || !validComponent(record.ID) || !validComponent(record.AttemptID) || (record.Kind != Attempt && record.Kind != Validation) || record.CommonDir != commonDir || !filepath.IsAbs(record.Path) || filepath.Clean(record.Path) != record.Path || record.BaseCommit == "" || record.BaseTree == "" || !validHash(record.ConfigHash) || record.CreatedAt.IsZero() || !validHash(record.MarkerHash) {
		return errors.New("workspace marker violates its contract")
	}
	if record.ProtocolVersion == legacyMarkerVersion {
		if record.ExecutionModel != "" || record.Identity != nil || record.MarkerPath != "" || len(record.ExcludePaths) != 0 || record.InputTree != "" {
			return errors.New("legacy workspace marker contains new execution fields")
		}
		return nil
	}
	if record.ExecutionModel != ExecutionCurrentDirectory || record.Identity == nil || record.Identity.Validate() != nil || record.Identity.Root != record.Path || record.Identity.CommonDir != commonDir || record.Identity.HeadCommit == "" || record.Identity.HeadTree == "" || !validHash(record.Identity.IndexHash) || !validHash(record.Identity.GitConfigHash) || record.InputTree == "" || !filepath.IsAbs(record.MarkerPath) || filepath.Clean(record.MarkerPath) != record.MarkerPath {
		return errors.New("current-directory workspace identity is invalid")
	}
	for _, excluded := range record.ExcludePaths {
		if !filepath.IsAbs(excluded) || filepath.Clean(excluded) != excluded {
			return errors.New("workspace exclusion must be canonical absolute")
		}
	}
	return nil
}
func markerHash(record marker) (string, error) {
	return canonical.Hash("workspace-marker", record.ProtocolVersion, markerIdentity{ProtocolVersion: record.ProtocolVersion, ID: record.ID, AttemptID: record.AttemptID, Kind: record.Kind, Path: record.Path, CommonDir: record.CommonDir, BaseCommit: record.BaseCommit, BaseTree: record.BaseTree, ConfigHash: record.ConfigHash, CreatedAt: record.CreatedAt, ExecutionModel: record.ExecutionModel, MarkerPath: record.MarkerPath, Identity: record.Identity, ExcludePaths: record.ExcludePaths, InputTree: record.InputTree})
}
func snapshotFrom(record marker) Snapshot {
	result := Snapshot{ID: record.ID, AttemptID: record.AttemptID, Kind: record.Kind, Path: record.Path, MarkerPath: boundMarkerPath(record), CommonDir: record.CommonDir, BaseCommit: record.BaseCommit, BaseTree: record.BaseTree, ConfigHash: record.ConfigHash, MarkerHash: record.MarkerHash, CreatedAt: record.CreatedAt, ExecutionModel: ExecutionLegacyWorktree}
	if record.ProtocolVersion == markerVersion {
		result.ExecutionModel = ExecutionCurrentDirectory
		result.Identity = *record.Identity
		result.ExcludePaths = append([]string(nil), record.ExcludePaths...)
		result.InputTree = record.InputTree
		result.HeadCommit = record.Identity.HeadCommit
		result.HeadTree = record.Identity.HeadTree
	}
	return result
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
