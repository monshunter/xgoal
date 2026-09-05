package patch_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/patch"
	"github.com/monshunter/xgoal/internal/protocol"
)

func TestCapturePreservesEveryFilesystemChangeWithoutMovingHeadOrIndex(t *testing.T) {
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
	work := currentPatchCheckout(t, repository)

	mustWrite(t, filepath.Join(work.Path, "tracked.txt"), []byte("modified\n"), 0o600)
	if err := os.Remove(filepath.Join(work.Path, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(work.Path, "rename-old.txt"), filepath.Join(work.Path, "rename-new.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(work.Path, "mode.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(work.Path, "binary.bin"), []byte{0x00, 0xff, 0x10, 0x80}, 0o600)
	if err := os.Remove(filepath.Join(work.Path, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tracked.txt", filepath.Join(work.Path, "link")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(work.Path, "untracked-empty.txt"), nil, 0o600)

	captured, err := patch.Capture(ctx, repository, patch.CaptureSpec{
		AttemptID: "attempt_1", ExecutionPath: work.Path, Identity: work.Identity, BaseCommit: base.Commit, BaseTree: base.Tree, MaxFileBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if err := repository.CheckCheckoutIdentity(ctx, work.Identity); err != nil {
		t.Fatal(err)
	}
	if err := captured.Bundle.Validate(); err != nil {
		t.Fatalf("captured bundle invalid: %v", err)
	}
	entries := make(map[string]protocol.PatchEntry)
	for _, entry := range captured.Bundle.Entries {
		entries[entry.Path] = entry
	}
	wantKinds := map[string]protocol.PatchKind{
		"binary.bin": protocol.PatchModified, "deleted.txt": protocol.PatchDeleted,
		"link": protocol.PatchModified, "mode.sh": protocol.PatchModified,
		"rename-new.txt": protocol.PatchRenamed, "tracked.txt": protocol.PatchModified,
		"untracked-empty.txt": protocol.PatchAdded,
	}
	if len(entries) != len(wantKinds) {
		t.Fatalf("captured entries = %+v", captured.Bundle.Entries)
	}
	for path, kind := range wantKinds {
		if entries[path].Kind != kind {
			t.Errorf("entry %s kind = %s, want %s", path, entries[path].Kind, kind)
		}
	}
	if entries["rename-new.txt"].PathBefore != "rename-old.txt" || entries["mode.sh"].ModeBefore != "100644" || entries["mode.sh"].ModeAfter != "100755" || entries["link"].ModeAfter != "120000" {
		t.Fatalf("rename/mode/symlink entries = %+v / %+v / %+v", entries["rename-new.txt"], entries["mode.sh"], entries["link"])
	}

	bundleStore, err := patch.NewStore(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := bundleStore.Save(captured)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("bundle path = %q, want absolute", path)
	}
	loaded, err := bundleStore.Load("attempt_1")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Bundle.BundleHash != captured.Bundle.BundleHash || len(loaded.Objects) != len(captured.Objects) {
		t.Fatalf("loaded bundle differs: %+v", loaded.Bundle)
	}
	for hash, content := range captured.Objects {
		if !bytes.Equal(loaded.Objects[hash], content) {
			t.Fatalf("loaded object %s differs", hash)
		}
	}
}

func TestCaptureRejectsEscapingSymlinkAndBundleStoreRejectsTampering(t *testing.T) {
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
	work := currentPatchCheckout(t, repository)
	if err := os.Symlink("../../outside", filepath.Join(work.Path, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := patch.Capture(ctx, repository, patch.CaptureSpec{
		AttemptID: "attempt_escape", ExecutionPath: work.Path, Identity: work.Identity, BaseCommit: base.Commit, BaseTree: base.Tree, MaxFileBytes: 1 << 20,
	}); err == nil {
		t.Fatal("Capture() accepted escaping symlink")
	}
	if err := os.Remove(filepath.Join(work.Path, "escape")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(work.Path, "safe.txt"), []byte("safe"), 0o600)
	captured, err := patch.Capture(ctx, repository, patch.CaptureSpec{
		AttemptID: "attempt_escape", ExecutionPath: work.Path, Identity: work.Identity, BaseCommit: base.Commit, BaseTree: base.Tree, MaxFileBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	bundleStore, err := patch.NewStore(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bundleStore.Save(captured); err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(runtimeRoot, "patches", "attempt_escape", filepath.FromSlash(captured.Bundle.Objects[0].Ref))
	if err := os.WriteFile(objectPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bundleStore.Load("attempt_escape"); err == nil {
		t.Fatal("Load() accepted a tampered object")
	}
}

func initializePatchRepository(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "init", "-b", "main")
	mustWrite(t, filepath.Join(path, "tracked.txt"), []byte("base\n"), 0o600)
	mustWrite(t, filepath.Join(path, "deleted.txt"), []byte("delete\n"), 0o600)
	mustWrite(t, filepath.Join(path, "rename-old.txt"), []byte("rename\n"), 0o600)
	mustWrite(t, filepath.Join(path, "mode.sh"), []byte("#!/bin/sh\n"), 0o600)
	mustWrite(t, filepath.Join(path, "binary.bin"), []byte{0x00, 0x01, 0x02}, 0o600)
	if err := os.Symlink("deleted.txt", filepath.Join(path, "link")); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "add", "-A")
	runGit(t, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "fixture")
}

func mustWrite(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v error = %v\n%s", arguments, err, output)
	}
	return string(output)
}

func currentPatchCheckout(t *testing.T, repository *gitrepo.Repository) struct {
	Path     string
	Identity gitrepo.CheckoutIdentity
} {
	t.Helper()
	identity, err := repository.ReadCheckoutIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return struct {
		Path     string
		Identity gitrepo.CheckoutIdentity
	}{repository.Root(), identity}
}
func TestCaptureRejectsAgentChangesToUserGitMetadata(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo")
	initializePatchRepository(t, root)
	repository, err := gitrepo.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	work := currentPatchCheckout(t, repository)
	mustWrite(t, filepath.Join(root, "tracked.txt"), []byte("agent change"), 0600)
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "-c", "user.name=Agent", "-c", "user.email=agent@invalid", "commit", "--no-verify", "-m", "unauthorized commit")
	_, err = patch.Capture(ctx, repository, patch.CaptureSpec{AttemptID: "attempt_metadata", ExecutionPath: work.Path, Identity: work.Identity, BaseCommit: work.Identity.HeadCommit, BaseTree: work.Identity.HeadTree, MaxFileBytes: 1 << 20})
	if !errors.Is(err, gitrepo.ErrCheckoutChanged) {
		t.Fatalf("metadata change was accepted: %v", err)
	}
}
