package patch_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/patch"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/scope"
	"github.com/monshunter/xgoal/internal/workspace"
)

func TestReplayAppliesBundleToLatestIntegrationWithoutMovingRefs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repositoryPath := filepath.Join(t.TempDir(), "repo")
	initializePatchRepository(t, repositoryPath)
	repository, err := gitrepo.Open(ctx, repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	base, err := repository.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.EnsureIntegrationBranch(ctx, "xgoal/goal_replay/integration", base.Commit); err != nil {
		t.Fatal(err)
	}
	manager, err := workspace.NewManager(filepath.Join(t.TempDir(), "runtime"), repository)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := manager.Create(ctx, workspace.Spec{
		ID: "attempt_workspace", AttemptID: "attempt_replay", Kind: workspace.Attempt,
		BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Cleanup(ctx, attempt.ID)
	mustWrite(t, filepath.Join(attempt.Path, "tracked.txt"), []byte("attempt\n"), 0o600)
	if err := os.Mkdir(filepath.Join(attempt.Path, "internal"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(attempt.Path, "internal", "new.txt"), []byte("new\n"), 0o600)
	captured, err := patch.Capture(ctx, repository, patch.CaptureSpec{
		AttemptID: "attempt_replay", WorktreePath: attempt.Path,
		BaseCommit: base.Commit, BaseTree: base.Tree, MaxFileBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	mustWrite(t, filepath.Join(repositoryPath, "unrelated.txt"), []byte("integration\n"), 0o600)
	runGit(t, repositoryPath, "add", "unrelated.txt")
	runGit(t, repositoryPath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "integration advance")
	integration, err := repository.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryPath, "update-ref", "refs/heads/xgoal/goal_replay/integration", integration.Commit, base.Commit)
	userHeadBefore := strings.TrimSpace(runGit(t, repositoryPath, "rev-parse", "HEAD"))
	userStatusBefore := runGit(t, repositoryPath, "status", "--porcelain=v1")

	validation, err := manager.Create(ctx, workspace.Spec{
		ID: "validation_workspace", AttemptID: "attempt_replay", Kind: workspace.Validation,
		BaseCommit: integration.Commit, BaseTree: integration.Tree, ConfigHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Cleanup(ctx, validation.ID)
	policy, err := scope.NewPolicy([]string{"/**"}, []string{"/.git/**"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := patch.Replay(ctx, repository, patch.ReplaySpec{
		WorktreePath: validation.Path, IntegrationCommit: integration.Commit, IntegrationTree: integration.Tree,
		Captured: captured, Policy: policy, MaxFileBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if result.CandidateTree == integration.Tree || result.IntegrationCommit != integration.Commit {
		t.Fatalf("Replay() result = %+v", result)
	}
	for filename, want := range map[string]string{
		"tracked.txt": "attempt\n", "internal/new.txt": "new\n", "unrelated.txt": "integration\n",
	} {
		content, err := os.ReadFile(filepath.Join(validation.Path, filepath.FromSlash(filename)))
		if err != nil || string(content) != want {
			t.Fatalf("validation file %s = %q, %v", filename, content, err)
		}
	}
	if got := strings.TrimSpace(runGit(t, validation.Path, "rev-parse", "HEAD")); got != integration.Commit {
		t.Fatalf("validation HEAD moved to %s", got)
	}
	if got := strings.TrimSpace(runGit(t, repositoryPath, "rev-parse", "refs/heads/xgoal/goal_replay/integration")); got != integration.Commit {
		t.Fatalf("integration ref moved to %s", got)
	}
	if got := strings.TrimSpace(runGit(t, repositoryPath, "rev-parse", "HEAD")); got != userHeadBefore || runGit(t, repositoryPath, "status", "--porcelain=v1") != userStatusBefore {
		t.Fatal("replay changed the user checkout")
	}
}

func TestReplayRejectsConflictAndScopeViolationBeforeMutation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name              string
		changeIntegration bool
		writeScope        []string
		wantError         error
	}{
		{name: "conflict", changeIntegration: true, writeScope: []string{"/**"}, wantError: patch.ErrStaleOrConflict},
		{name: "scope", writeScope: []string{"/internal/**"}, wantError: scope.ErrViolation},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			repositoryPath := filepath.Join(t.TempDir(), "repo")
			initializePatchRepository(t, repositoryPath)
			repository, err := gitrepo.Open(ctx, repositoryPath)
			if err != nil {
				t.Fatal(err)
			}
			base, err := repository.ResolveRevision(ctx, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			manager, err := workspace.NewManager(filepath.Join(t.TempDir(), "runtime"), repository)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := manager.Create(ctx, workspace.Spec{
				ID: "attempt_workspace", AttemptID: "attempt_replay", Kind: workspace.Attempt,
				BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: strings.Repeat("b", 64),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Cleanup(ctx, attempt.ID)
			mustWrite(t, filepath.Join(attempt.Path, "tracked.txt"), []byte("attempt\n"), 0o600)
			captured, err := patch.Capture(ctx, repository, patch.CaptureSpec{
				AttemptID: "attempt_replay", WorktreePath: attempt.Path,
				BaseCommit: base.Commit, BaseTree: base.Tree, MaxFileBytes: 1 << 20,
			})
			if err != nil {
				t.Fatal(err)
			}
			integration := base
			if test.changeIntegration {
				mustWrite(t, filepath.Join(repositoryPath, "tracked.txt"), []byte("integration\n"), 0o600)
				runGit(t, repositoryPath, "add", "tracked.txt")
				runGit(t, repositoryPath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "conflict")
				integration, err = repository.ResolveRevision(ctx, "HEAD")
				if err != nil {
					t.Fatal(err)
				}
			}
			validation, err := manager.Create(ctx, workspace.Spec{
				ID: "validation_workspace", AttemptID: "attempt_replay", Kind: workspace.Validation,
				BaseCommit: integration.Commit, BaseTree: integration.Tree, ConfigHash: strings.Repeat("b", 64),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Cleanup(ctx, validation.ID)
			before := runGit(t, validation.Path, "status", "--porcelain=v1")
			policy, err := scope.NewPolicy(test.writeScope, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = patch.Replay(ctx, repository, patch.ReplaySpec{
				WorktreePath: validation.Path, IntegrationCommit: integration.Commit, IntegrationTree: integration.Tree,
				Captured: captured, Policy: policy, MaxFileBytes: 1 << 20,
			})
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Replay() error = %v, want %v", err, test.wantError)
			}
			if after := runGit(t, validation.Path, "status", "--porcelain=v1"); after != before {
				t.Fatalf("failed replay mutated validation worktree: %q", after)
			}
		})
	}
}

func TestReplayRejectsSymlinkParentBeforeMutation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repositoryPath := filepath.Join(t.TempDir(), "repo")
	initializePatchRepository(t, repositoryPath)
	if err := os.Mkdir(filepath.Join(repositoryPath, "actual"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(repositoryPath, "actual", "base.txt"), []byte("base\n"), 0o600)
	if err := os.Symlink("actual", filepath.Join(repositoryPath, "linkdir")); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryPath, "add", "actual/base.txt", "linkdir")
	runGit(t, repositoryPath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "symlink parent")
	repository, err := gitrepo.Open(ctx, repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	integration, err := repository.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("forbidden\n")
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	bundle, err := (protocol.PatchBundle{
		ProtocolVersion: protocol.PatchBundleVersion,
		AttemptID:       "attempt_unsafe",
		BaseCommit:      integration.Commit,
		BaseTree:        integration.Tree,
		Entries: []protocol.PatchEntry{{
			Path: "linkdir/new.txt", Kind: protocol.PatchAdded, ModeAfter: "100644",
			ContentHashAfter: hash, ObjectRef: "objects/sha256/" + hash,
		}},
		Objects: []protocol.PatchObject{{Ref: "objects/sha256/" + hash, Hash: hash, Length: int64(len(content))}},
	}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	manager, err := workspace.NewManager(filepath.Join(t.TempDir(), "runtime"), repository)
	if err != nil {
		t.Fatal(err)
	}
	validation, err := manager.Create(ctx, workspace.Spec{
		ID: "validation_workspace", AttemptID: "attempt_unsafe", Kind: workspace.Validation,
		BaseCommit: integration.Commit, BaseTree: integration.Tree, ConfigHash: strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Cleanup(ctx, validation.ID)
	policy, err := scope.NewPolicy([]string{"/**"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = patch.Replay(ctx, repository, patch.ReplaySpec{
		WorktreePath: validation.Path, IntegrationCommit: integration.Commit, IntegrationTree: integration.Tree,
		Captured: patch.Captured{Bundle: bundle, Objects: map[string][]byte{hash: content}}, Policy: policy, MaxFileBytes: 1 << 20,
	})
	if !errors.Is(err, patch.ErrUnsafeReplay) {
		t.Fatalf("Replay() error = %v, want ErrUnsafeReplay", err)
	}
	if _, err := os.Lstat(filepath.Join(repositoryPath, "actual", "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe replay wrote through symlink: %v", err)
	}
	if status := runGit(t, validation.Path, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("unsafe replay mutated validation worktree: %q", status)
	}
}

func TestReplaySupportsFileDirectoryTopologyChanges(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		prepareBase  func(*testing.T, string)
		change       func(*testing.T, string)
		assertResult func(*testing.T, string)
	}{
		{
			name: "file_to_directory",
			prepareBase: func(t *testing.T, root string) {
				mustWrite(t, filepath.Join(root, "shape"), []byte("file\n"), 0o600)
			},
			change: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "shape")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, "shape"), 0o700); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, filepath.Join(root, "shape", "child.txt"), []byte("child\n"), 0o600)
			},
			assertResult: func(t *testing.T, root string) {
				content, err := os.ReadFile(filepath.Join(root, "shape", "child.txt"))
				if err != nil || string(content) != "child\n" {
					t.Fatalf("file-to-directory result = %q, %v", content, err)
				}
			},
		},
		{
			name: "directory_to_file",
			prepareBase: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, "shape"), 0o700); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, filepath.Join(root, "shape", "child.txt"), []byte("child\n"), 0o600)
			},
			change: func(t *testing.T, root string) {
				if err := os.RemoveAll(filepath.Join(root, "shape")); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, filepath.Join(root, "shape"), []byte("file\n"), 0o600)
			},
			assertResult: func(t *testing.T, root string) {
				content, err := os.ReadFile(filepath.Join(root, "shape"))
				if err != nil || string(content) != "file\n" {
					t.Fatalf("directory-to-file result = %q, %v", content, err)
				}
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			repositoryPath := filepath.Join(t.TempDir(), "repo")
			initializePatchRepository(t, repositoryPath)
			test.prepareBase(t, repositoryPath)
			runGit(t, repositoryPath, "add", "-A")
			runGit(t, repositoryPath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "topology base")
			repository, err := gitrepo.Open(ctx, repositoryPath)
			if err != nil {
				t.Fatal(err)
			}
			base, err := repository.ResolveRevision(ctx, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			manager, err := workspace.NewManager(filepath.Join(t.TempDir(), "runtime"), repository)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := manager.Create(ctx, workspace.Spec{
				ID: "attempt_topology", AttemptID: "attempt_topology", Kind: workspace.Attempt,
				BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: strings.Repeat("d", 64),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Cleanup(ctx, attempt.ID)
			test.change(t, attempt.Path)
			captured, err := patch.Capture(ctx, repository, patch.CaptureSpec{
				AttemptID: "attempt_topology", WorktreePath: attempt.Path,
				BaseCommit: base.Commit, BaseTree: base.Tree, MaxFileBytes: 1 << 20,
			})
			if err != nil {
				t.Fatal(err)
			}
			validation, err := manager.Create(ctx, workspace.Spec{
				ID: "validation_topology", AttemptID: "attempt_topology", Kind: workspace.Validation,
				BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: strings.Repeat("d", 64),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Cleanup(ctx, validation.ID)
			policy, err := scope.NewPolicy([]string{"/**"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := patch.Replay(ctx, repository, patch.ReplaySpec{
				WorktreePath: validation.Path, IntegrationCommit: base.Commit, IntegrationTree: base.Tree,
				Captured: captured, Policy: policy, MaxFileBytes: 1 << 20,
			}); err != nil {
				t.Fatalf("Replay() topology error = %v", err)
			}
			test.assertResult(t, validation.Path)
		})
	}
}
