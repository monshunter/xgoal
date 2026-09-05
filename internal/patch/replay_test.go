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
	"time"

	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/patch"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/scope"
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
	if _, _, err := repository.EnsureIntegrationRef(ctx, "refs/xgoal/goals/goal_replay/integration", base.Commit); err != nil {
		t.Fatal(err)
	}
	attempt := currentPatchCheckout(t, repository)
	mustWrite(t, filepath.Join(attempt.Path, "tracked.txt"), []byte("attempt\n"), 0o600)
	if err := os.Mkdir(filepath.Join(attempt.Path, "internal"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(attempt.Path, "internal", "new.txt"), []byte("new\n"), 0o600)
	captured, err := patch.Capture(ctx, repository, patch.CaptureSpec{
		AttemptID: "attempt_replay", ExecutionPath: attempt.Path, Identity: attempt.Identity,
		BaseCommit: base.Commit, BaseTree: base.Tree, MaxFileBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	mustWrite(t, filepath.Join(repositoryPath, "unrelated.txt"), []byte("integration\n"), 0o600)
	integration := privatePatchRevision(t, repository, base, map[string][]byte{"unrelated.txt": []byte("integration\n")})
	runGit(t, repositoryPath, "update-ref", "refs/xgoal/goals/goal_replay/integration", integration.Commit, base.Commit)
	userHeadBefore := strings.TrimSpace(runGit(t, repositoryPath, "rev-parse", "HEAD"))
	userStatusBefore := runGit(t, repositoryPath, "status", "--porcelain=v1")

	validation := currentPatchCheckout(t, repository)
	policy, err := scope.NewPolicy([]string{"/**"}, []string{"/.git/**"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := patch.Replay(ctx, repository, patch.ReplaySpec{
		ExecutionPath: validation.Path, Identity: validation.Identity, IntegrationCommit: integration.Commit, IntegrationTree: integration.Tree,
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
	if got := strings.TrimSpace(runGit(t, validation.Path, "rev-parse", "HEAD")); got != base.Commit {
		t.Fatalf("user HEAD moved to %s", got)
	}
	if got := strings.TrimSpace(runGit(t, repositoryPath, "rev-parse", "refs/xgoal/goals/goal_replay/integration")); got != integration.Commit {
		t.Fatalf("integration ref moved to %s", got)
	}
	if got := strings.TrimSpace(runGit(t, repositoryPath, "rev-parse", "HEAD")); got != userHeadBefore || runGit(t, repositoryPath, "status", "--porcelain=v1") != userStatusBefore {
		t.Fatal("replay changed the user checkout")
	}
}

func TestReplayPreservesLaterExternalSourceEdit(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo")
	initializePatchRepository(t, root)
	repo, err := gitrepo.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	checkout := currentPatchCheckout(t, repo)
	base, err := repo.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "tracked.txt"), []byte("candidate\n"), 0600)
	captured, err := patch.Capture(ctx, repo, patch.CaptureSpec{AttemptID: "attempt_later_edit", ExecutionPath: checkout.Path, Identity: checkout.Identity, BaseCommit: base.Commit, BaseTree: base.Tree, MaxFileBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "tracked.txt"), []byte("later user edit\n"), 0600)
	policy, err := scope.NewPolicy([]string{"/**"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = patch.Replay(ctx, repo, patch.ReplaySpec{ExecutionPath: checkout.Path, Identity: checkout.Identity, IntegrationCommit: base.Commit, IntegrationTree: base.Tree, Captured: captured, Policy: policy, MaxFileBytes: 1 << 20})
	if !errors.Is(err, gitrepo.ErrCheckoutChanged) {
		t.Fatalf("accepted later edit: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil || string(content) != "later user edit\n" {
		t.Fatalf("replay overwrote later edit: %q %v", content, err)
	}
	if err := repo.CheckCheckoutIdentity(ctx, checkout.Identity); err != nil {
		t.Fatal(err)
	}
}

func TestReplayPreservesUnambiguousDecomposedFilename(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo")
	initializePatchRepository(t, root)
	repo, err := gitrepo.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	checkout := currentPatchCheckout(t, repo)
	base, err := repo.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	rawName := "cafe\u0301.txt"
	mustWrite(t, filepath.Join(root, rawName), []byte("unicode\n"), 0600)
	captured, err := patch.Capture(ctx, repo, patch.CaptureSpec{AttemptID: "attempt_unicode", ExecutionPath: checkout.Path, Identity: checkout.Identity, BaseCommit: base.Commit, BaseTree: base.Tree, MaxFileBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := scope.NewPolicy([]string{"/**"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := patch.Replay(ctx, repo, patch.ReplaySpec{ExecutionPath: checkout.Path, Identity: checkout.Identity, IntegrationCommit: base.Commit, IntegrationTree: base.Tree, Captured: captured, Policy: policy, MaxFileBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := repo.ListTree(ctx, result.CandidateTree)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Path == rawName {
			found = true
		}
	}
	if !found {
		t.Fatalf("raw filename changed in candidate: %#v", entries)
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
			attempt := currentPatchCheckout(t, repository)
			mustWrite(t, filepath.Join(attempt.Path, "tracked.txt"), []byte("attempt\n"), 0o600)
			captured, err := patch.Capture(ctx, repository, patch.CaptureSpec{
				AttemptID: "attempt_replay", ExecutionPath: attempt.Path, Identity: attempt.Identity,
				BaseCommit: base.Commit, BaseTree: base.Tree, MaxFileBytes: 1 << 20,
			})
			if err != nil {
				t.Fatal(err)
			}
			integration := base
			if test.changeIntegration {
				mustWrite(t, filepath.Join(repositoryPath, "tracked.txt"), []byte("integration\n"), 0o600)
				integration = privatePatchRevision(t, repository, base, map[string][]byte{"tracked.txt": []byte("integration\n")})
			}
			validation := currentPatchCheckout(t, repository)
			before := runGit(t, validation.Path, "status", "--porcelain=v1")
			policy, err := scope.NewPolicy(test.writeScope, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = patch.Replay(ctx, repository, patch.ReplaySpec{
				ExecutionPath: validation.Path, Identity: validation.Identity, IntegrationCommit: integration.Commit, IntegrationTree: integration.Tree,
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
	validation := currentPatchCheckout(t, repository)
	policy, err := scope.NewPolicy([]string{"/**"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = patch.Replay(ctx, repository, patch.ReplaySpec{
		ExecutionPath: validation.Path, Identity: validation.Identity, IntegrationCommit: integration.Commit, IntegrationTree: integration.Tree,
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
			attempt := currentPatchCheckout(t, repository)
			test.change(t, attempt.Path)
			captured, err := patch.Capture(ctx, repository, patch.CaptureSpec{
				AttemptID: "attempt_topology", ExecutionPath: attempt.Path, Identity: attempt.Identity,
				BaseCommit: base.Commit, BaseTree: base.Tree, MaxFileBytes: 1 << 20,
			})
			if err != nil {
				t.Fatal(err)
			}
			validation := currentPatchCheckout(t, repository)
			policy, err := scope.NewPolicy([]string{"/**"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := patch.Replay(ctx, repository, patch.ReplaySpec{
				ExecutionPath: validation.Path, Identity: validation.Identity, IntegrationCommit: base.Commit, IntegrationTree: base.Tree,
				Captured: captured, Policy: policy, MaxFileBytes: 1 << 20,
			}); err != nil {
				t.Fatalf("Replay() topology error = %v", err)
			}
			test.assertResult(t, validation.Path)
		})
	}
}

func privatePatchRevision(t *testing.T, repo *gitrepo.Repository, base gitrepo.Revision, changes map[string][]byte) gitrepo.Revision {
	t.Helper()
	ctx := context.Background()
	entries, err := repo.ListTree(ctx, base.Tree)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]gitrepo.TreeEntry{}
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	for path, content := range changes {
		object, err := repo.WriteBlob(ctx, content)
		if err != nil {
			t.Fatal(err)
		}
		byPath[path] = gitrepo.TreeEntry{Path: path, Mode: "100644", ObjectID: object}
	}
	entries = nil
	for _, entry := range byPath {
		entries = append(entries, entry)
	}
	tree, err := repo.BuildTree(ctx, entries)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.CreateCommit(ctx, gitrepo.CommitSpec{Tree: tree, Parent: base.Commit, Message: "private fixture", Timestamp: time.Unix(1700000000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	return gitrepo.Revision{Commit: commit.ID, Tree: tree}
}
