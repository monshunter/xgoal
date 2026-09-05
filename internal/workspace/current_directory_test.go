package workspace_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/workspace"
)

func TestCurrentDirectorySessionsShareCheckoutAndCleanupOnlyMetadata(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo")
	initializeRepository(t, root)
	repo, err := gitrepo.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	base, err := repo.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	before := runGit(t, root, "worktree", "list", "--porcelain")
	manager, err := workspace.NewManager(filepath.Join(root, ".xgoal"), repo)
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []workspace.Snapshot
	for _, kind := range []workspace.Kind{workspace.Attempt, workspace.Validation} {
		snapshot, err := manager.Create(ctx, workspace.Spec{ID: "ws_" + string(kind), AttemptID: "attempt", Kind: kind, BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: strings.Repeat("b", 64)})
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Path != repo.Root() {
			t.Fatalf("execution at %s, want %s", snapshot.Path, repo.Root())
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := os.WriteFile(filepath.Join(repo.Root(), "output.txt"), []byte("visible\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range snapshots {
		read, err := workspace.ReadMarkerSnapshot(snapshot.MarkerPath)
		if err != nil || read.Path != repo.Root() {
			t.Fatalf("read=%+v err=%v", read, err)
		}
		if err := manager.Cleanup(ctx, snapshot.ID); err != nil {
			t.Fatal(err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(repo.Root(), "output.txt")); err != nil || string(data) != "visible\n" {
		t.Fatalf("cleanup changed checkout: %s %v", data, err)
	}
	if after := runGit(t, root, "worktree", "list", "--porcelain"); after != before {
		t.Fatalf("worktree list changed: %s -> %s", before, after)
	}
}

func TestLegacyWorkspaceMarkerRemainsReadableAndCannotBeCleaned(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo")
	initializeRepository(t, root)
	repo, err := gitrepo.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	base, err := repo.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(t.TempDir(), "state")
	manager, err := workspace.NewManager(runtime, repo)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err = filepath.EvalSymlinks(runtime)
	if err != nil {
		t.Fatal(err)
	}
	container := filepath.Join(runtime, "workspaces", "attempts", "old")
	if err := os.Mkdir(container, 0700); err != nil {
		t.Fatal(err)
	}
	// This historical marker can be read even when its old code directory is absent.
	record := map[string]any{"protocol_version": "xgoal.workspace-marker/v1", "id": "old", "attempt_id": "attempt_old", "kind": "attempt", "path": filepath.Join(container, "tree"), "common_dir": repo.CommonDir(), "base_commit": base.Commit, "base_tree": base.Tree, "config_hash": strings.Repeat("a", 64), "created_at": time.Date(2026, 9, 2, 1, 0, 0, 0, time.UTC)}
	hash, err := canonical.Hash("workspace-marker", "xgoal.workspace-marker/v1", record)
	if err != nil {
		t.Fatal(err)
	}
	record["marker_hash"] = hash
	encoded, err := canonical.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(container, "marker.json")
	if err := os.WriteFile(marker, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspace.ReadMarkerSnapshot(marker)
	if err != nil || snapshot.MarkerHash != hash || snapshot.ExecutionModel != workspace.ExecutionLegacyWorktree {
		t.Fatalf("legacy snapshot=%+v %v", snapshot, err)
	}
	if _, err := manager.ReadBack(ctx, "old"); !errors.Is(err, workspace.ErrLegacyWorkspace) {
		t.Fatalf("legacy executed: %v", err)
	}
	if err := manager.Cleanup(ctx, "old"); !errors.Is(err, workspace.ErrLegacyWorkspace) {
		t.Fatalf("legacy cleanup=%v", err)
	}
	if after, err := os.ReadFile(marker); err != nil || !bytes.Equal(after, encoded) {
		t.Fatalf("legacy marker changed: %v", err)
	}
}
