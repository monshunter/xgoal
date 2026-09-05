package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitTest(t, root, "init", "-b", "main")
	gitTest(t, root, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "initial")
	return root
}

func gitTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, b, err)
	}
	return strings.TrimSpace(string(b))
}

func TestResolverSharesMainRepositoryAcrossSubdirectoriesAndAliases(t *testing.T) {
	root := fixture(t)
	sub := filepath.Join(root, "src")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	first, err := Resolve(context.Background(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{sub, alias} {
		got, err := Resolve(context.Background(), dir, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if got.ProjectID != first.ProjectID || got.CommonDir != first.CommonDir || got.StateDir != first.StateDir || got.SocketPath != first.SocketPath || got.ProjectRoot != first.ProjectRoot {
			t.Fatalf("%s resolves differently: %+v / %+v", dir, got, first)
		}
	}
	if _, err := os.Stat(first.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only resolver created state")
	}
}

func TestResolverRejectsLinkedWorktreeWithoutWriting(t *testing.T) {
	root := fixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, root, "worktree", "add", "-b", "other", linked)
	sub := filepath.Join(linked, "src")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(linked, alias); err != nil {
		t.Fatal(err)
	}
	before := gitTest(t, root, "worktree", "list", "--porcelain")
	for _, dir := range []string{linked, sub, alias} {
		if _, err := Resolve(context.Background(), dir, "", ""); err == nil || !strings.Contains(err.Error(), "linked worktree") {
			t.Fatalf("linked entry %s: %v", dir, err)
		}
	}
	if after := gitTest(t, root, "worktree", "list", "--porcelain"); after != before {
		t.Fatal("resolution modified worktree registration")
	}
	for _, path := range []string{filepath.Join(root, ".xgoal"), filepath.Join(linked, ".xgoal"), filepath.Join(root, ".git", "xgoal")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejected resolution wrote %s", path)
		}
	}
}

func TestIndependentClonesHaveDistinctIdentityAndSocket(t *testing.T) {
	root := fixture(t)
	first, err := Resolve(context.Background(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := Acquire(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Bind(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	gitTest(t, root, "clone", "--no-local", root, clone)
	second, err := Resolve(context.Background(), clone, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.ProjectID == second.ProjectID || first.SocketPath == second.SocketPath {
		t.Fatal("independent clones share identity")
	}
}

func TestOwnershipExcludesAlternateStateAndRejectsBoundOverride(t *testing.T) {
	root := fixture(t)
	first, err := Resolve(context.Background(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	alternate, err := Resolve(context.Background(), root, filepath.Join(t.TempDir(), "state"), "")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := Acquire(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if other, err := Acquire(context.Background(), alternate); !errors.Is(err, ErrAlreadyRunning) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("alternate owner: %v", err)
	}
	if _, err := os.Stat(filepath.Join(alternate.StateDir, "state.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed lock created database")
	}
	if err := owner.Bind(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), root, alternate.StateDir, ""); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("bound override: %v", err)
	}
}

func TestResolverRejectsLinkedLegacyStateAndAmbiguousHistories(t *testing.T) {
	root := fixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, root, "worktree", "add", "-b", "other", linked)
	for index, path := range []string{linked, root} {
		if err := os.Mkdir(filepath.Join(path, ".xgoal"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, ".xgoal/state.db"), []byte("legacy marker"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Resolve(context.Background(), root, "", "")
		if index == 0 {
			if !errors.Is(err, ErrBindingMismatch) {
				t.Fatalf("legacy linked state adopted: %v", err)
			}
		} else if !errors.Is(err, ErrAmbiguousState) {
			t.Fatalf("multiple legacy histories: %v", err)
		}
	}
}

func TestResolverKeepsDefaultMainLegacyState(t *testing.T) {
	root := fixture(t)
	if err := os.Mkdir(filepath.Join(root, ".xgoal"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".xgoal", "state.db"), []byte("legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	paths, err := Resolve(context.Background(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := canonicalPath(root)
	if paths.ProjectRoot != expected || !paths.LegacyState {
		t.Fatalf("default main legacy changed: %+v", paths)
	}
}

func TestResolverRejectsOldLinkedLocatorFromMainDirectory(t *testing.T) {
	for _, registered := range []bool{true, false} {
		t.Run(fmt.Sprintf("registered=%t", registered), func(t *testing.T) {
			root := fixture(t)
			linked := filepath.Join(t.TempDir(), "old-linked")
			if registered {
				gitTest(t, root, "worktree", "add", "-b", "other", linked)
			}
			linked, err := canonicalPath(linked)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(binding{ProjectID: "legacy_project", ProjectRoot: linked, StateDir: filepath.Join(linked, ".xgoal")})
			if err != nil {
				t.Fatal(err)
			}
			gitTest(t, root, "config", "xgoal.stateBinding", string(raw))
			configBefore, err := os.ReadFile(filepath.Join(root, ".git", "config"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Resolve(context.Background(), root, "", ""); !errors.Is(err, ErrBindingMismatch) {
				t.Fatalf("old locator adopted: %v", err)
			}
			configAfter, _ := os.ReadFile(filepath.Join(root, ".git", "config"))
			if string(configBefore) != string(configAfter) {
				t.Fatal("rejected locator was rewritten")
			}
			if _, err := os.Stat(filepath.Join(root, ".xgoal")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected locator created new main state")
			}
		})
	}
}

func TestResolverRejectsSubmoduleButKeepsIndependentNestedRepository(t *testing.T) {
	outer := fixture(t)
	source := fixture(t)
	gitTest(t, outer, "-c", "protocol.file.allow=always", "submodule", "add", source, "module")
	if _, err := Resolve(context.Background(), filepath.Join(outer, "module"), "", ""); err == nil || !strings.Contains(err.Error(), "submodule") {
		t.Fatalf("submodule accepted: %v", err)
	}
	nested := filepath.Join(outer, "independent")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, nested, "init", "-b", "main")
	paths, err := Resolve(context.Background(), nested, "", "")
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := canonicalPath(nested)
	if paths.ProjectRoot != expected || paths.CommonDir != filepath.Join(expected, ".git") {
		t.Fatalf("independent nested repo identity changed: %+v", paths)
	}
}

func TestPathsAreShortAndOverridesUseOnePrecedence(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "xp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	gitTest(t, root, "init", "-b", "main")
	deep := filepath.Join(root, strings.Repeat("long", 40))
	if err := os.Mkdir(deep, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "env-state")
	t.Setenv("XGOAL_STATE_DIR", state)
	t.Setenv("XGOAL_SOCKET", "custom.sock")
	paths, err := Resolve(context.Background(), deep, "", "")
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := canonicalPath(state)
	if paths.StateDir != canonical || paths.SocketPath != filepath.Join(paths.ProjectRoot, "custom.sock") {
		t.Fatalf("env resolution = %+v", paths)
	}
	flagState := filepath.Join(t.TempDir(), "flag-state")
	paths, err = Resolve(context.Background(), root, flagState, "/tmp/explicit.sock")
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ = canonicalPath(flagState)
	if paths.StateDir != canonical || paths.SocketPath != "/private/tmp/explicit.sock" && paths.SocketPath != "/tmp/explicit.sock" {
		t.Fatalf("flags = %+v", paths)
	}
	t.Setenv("XGOAL_SOCKET", "")
	paths, err = Resolve(context.Background(), deep, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths.SocketPath) > 100 {
		t.Fatalf("default socket too long: %s", paths.SocketPath)
	}
	if _, err := Resolve(context.Background(), root, "", filepath.Join(root, strings.Repeat("s", 100))); err == nil {
		t.Fatal("long explicit socket accepted")
	}
}

func TestBindingRejectsAmbiguousJSONAndConfig(t *testing.T) {
	for _, raw := range []string{
		`{"project_id":"p","project_root":"/a","state_dir":"/b"} {}`,
		`{"project_id":"p","project_id":"q","project_root":"/a","state_dir":"/b"}`,
		`null`,
	} {
		t.Run(raw, func(t *testing.T) {
			root := fixture(t)
			gitTest(t, root, "config", "xgoal.stateBinding", raw)
			if _, err := Resolve(context.Background(), root, "", ""); !errors.Is(err, ErrBindingMismatch) {
				t.Fatalf("binding accepted: %v", err)
			}
		})
	}
	root := fixture(t)
	gitTest(t, root, "config", "--add", "xgoal.projectID", "a")
	gitTest(t, root, "config", "--add", "xgoal.projectID", "b")
	if _, err := Resolve(context.Background(), root, "", ""); err == nil {
		t.Fatal("multiple identities accepted")
	}
}

func TestOwnershipValidatesPathsBeforeWritingAndRejectsLinkedLocks(t *testing.T) {
	root := fixture(t)
	paths, err := Resolve(context.Background(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Paths){
		func(p *Paths) { p.ProjectID = "wrong" },
		func(p *Paths) { p.CommonDir = filepath.Join(root, "wrong") },
		func(p *Paths) { p.RepositoryIdentity = "wrong" },
		func(p *Paths) { p.ProjectRoot = filepath.Join(root, "wrong") },
	} {
		bad := paths
		mutate(&bad)
		if owner, err := Acquire(context.Background(), bad); err == nil {
			owner.Close()
			t.Fatal("forged paths acquired")
		}
		if _, err := os.Stat(paths.StateDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("rejected paths created state")
		}
		if _, err := os.Stat(filepath.Join(paths.CommonDir, "xgoal")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("rejected paths created repository lock directory")
		}
	}
	lockdir := filepath.Join(paths.CommonDir, "xgoal")
	if err := os.Mkdir(lockdir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "owned.txt")
	if err := os.WriteFile(target, []byte("preserve"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(lockdir, "owner.lock")); err != nil {
		t.Fatal(err)
	}
	if owner, err := Acquire(context.Background(), paths); err == nil {
		owner.Close()
		t.Fatal("linked lock accepted")
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0644 {
		t.Fatal("linked file permissions changed")
	}
}

func TestResolverRejectsLinkedSensitivePathsAndHonorsProjectEnvironment(t *testing.T) {
	root := fixture(t)
	t.Setenv("XGOAL_PROJECT", root)
	paths, err := Resolve(context.Background(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := canonicalPath(root)
	if paths.ProjectRoot != resolved {
		t.Fatalf("environment ignored: %+v", paths)
	}
	if err := os.Symlink(t.TempDir(), paths.StateDir); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), root, "", ""); err == nil {
		t.Fatal("linked state root accepted")
	}
}

func TestOwnershipHonorsLegacyLockAndPreservesLockInode(t *testing.T) {
	paths, err := Resolve(context.Background(), fixture(t), "", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(paths.StateDir, "run", "daemon.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if owner, err := Acquire(context.Background(), paths); !errors.Is(err, ErrAlreadyRunning) {
		if owner != nil {
			owner.Close()
		}
		t.Fatalf("legacy owner ignored: %v", err)
	}
	if held, err := OwnershipHeld(paths); err != nil || !held {
		t.Fatalf("status missed old owner: held=%v err=%v", held, err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	before, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := Acquire(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lock inode replaced: %v", err)
	}
	if held, err := OwnershipHeld(paths); err != nil || held {
		t.Fatalf("released owner still held: held=%v err=%v", held, err)
	}
}

func TestOwnershipRejectsHardlinkedLockWithoutChangingTarget(t *testing.T) {
	paths, err := Resolve(context.Background(), fixture(t), "", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(paths.CommonDir, "xgoal")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(paths.ProjectRoot, "target")
	if err := os.WriteFile(target, []byte("preserve"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(dir, "owner.lock")); err != nil {
		t.Fatal(err)
	}
	if owner, err := Acquire(context.Background(), paths); err == nil {
		owner.Close()
		t.Fatal("hardlinked lock accepted")
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0644 {
		t.Fatal("hardlinked target permissions changed")
	}
}

func TestExplicitProjectIsNotRedirectedByGitEnvironment(t *testing.T) {
	root := fixture(t)
	other := fixture(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	paths, err := Resolve(context.Background(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := canonicalPath(root)
	if paths.ProjectRoot != expected {
		t.Fatalf("Git environment redirected project to %s, want %s", paths.ProjectRoot, expected)
	}
}

func TestOwnershipStatusRejectsFIFOWithoutBlocking(t *testing.T) {
	paths, err := Resolve(context.Background(), fixture(t), "", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(paths.CommonDir, "xgoal", "owner.lock")
	if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := OwnershipHeld(paths); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO lock accepted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("status blocked on FIFO")
	}
}
