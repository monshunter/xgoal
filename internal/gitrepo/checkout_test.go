package gitrepo_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/gitrepo"
	"golang.org/x/sys/unix"
)

func TestSnapshotUsesRawFilesWithoutTouchingHeadIndexOrRunningFilters(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	writeCheckout(t, root, "tracked.txt", []byte("base\n"), 0600)
	writeCheckout(t, root, ".gitignore", []byte("ignored/\ntracked.txt\n"), 0600)
	writeCheckout(t, root, ".gitattributes", []byte("*.txt filter=tripwire\n"), 0600)
	runGit(t, root, "add", "-f", "tracked.txt", ".gitignore", ".gitattributes")
	runGit(t, root, "-c", "user.name=X", "-c", "user.email=x@invalid", "commit", "-q", "-m", "base")
	marker := filepath.Join(root, "must-not-run")
	runGit(t, root, "config", "filter.tripwire.clean", "touch "+marker)
	runGit(t, root, "config", "filter.tripwire.smudge", "touch "+marker)
	runGit(t, root, "config", "filter.tripwire.required", "true")
	monitor := filepath.Join(root, ".git", "fsmonitor-tripwire")
	writeCheckout(t, root, ".git/fsmonitor-tripwire", []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700)
	runGit(t, root, "config", "core.fsmonitor", monitor)
	repository, err := gitrepo.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repository.ReadCheckoutIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	indexBefore, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	writeCheckout(t, root, "tracked.txt", []byte("raw\r\nbytes\n"), 0600)
	writeCheckout(t, root, "new.bin", []byte{0, 1, 255}, 0755)
	writeCheckout(t, root, "ignored/cache", []byte("ignored"), 0600)
	writeCheckout(t, root, ".xgoal/state.db", []byte("metadata"), 0600)
	writeCheckout(t, root, "custom-runtime/state", []byte("metadata"), 0600)
	snapshot, err := repository.SnapshotTree(context.Background(), gitrepo.SnapshotSpec{BaseTree: identity.HeadTree, ExcludePaths: []string{filepath.Join(repository.Root(), "custom-runtime")}})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Identity != identity || snapshot.Tree == identity.HeadTree {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	files := map[string][]byte{}
	for _, file := range snapshot.Files {
		files[file.Path] = file.Content
	}
	if !bytes.Equal(files["tracked.txt"], []byte("raw\r\nbytes\n")) || !bytes.Equal(files["new.bin"], []byte{0, 1, 255}) {
		t.Fatalf("raw bytes not captured: %#v", files)
	}
	for _, path := range []string{"ignored/cache", ".xgoal/state.db", "custom-runtime/state"} {
		if _, ok := files[path]; ok {
			t.Fatalf("metadata or ignored file captured: %s", path)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("filter executed: %v", err)
	}
	indexAfter, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil || !bytes.Equal(indexBefore, indexAfter) {
		t.Fatal("user index changed")
	}
	if err := repository.CheckSnapshot(context.Background(), gitrepo.SnapshotSpec{BaseTree: identity.HeadTree, ExcludePaths: []string{filepath.Join(repository.Root(), "custom-runtime")}}, identity, snapshot.Tree); err != nil {
		t.Fatal(err)
	}
	writeCheckout(t, root, "new.bin", []byte("later edit"), 0600)
	if err := repository.CheckSnapshot(context.Background(), gitrepo.SnapshotSpec{BaseTree: identity.HeadTree, ExcludePaths: []string{filepath.Join(repository.Root(), "custom-runtime")}}, identity, snapshot.Tree); !errors.Is(err, gitrepo.ErrCheckoutChanged) {
		t.Fatalf("external change accepted: %v", err)
	}
}

func TestCheckoutIdentityDetectsIndexHeadAndIgnoreMetadataChanges(t *testing.T) {
	for _, kind := range []string{"index", "head", "ignore"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			runGit(t, root, "init", "-q", "-b", "main")
			writeCheckout(t, root, "file", []byte("base"), 0600)
			runGit(t, root, "add", "file")
			runGit(t, root, "-c", "user.name=X", "-c", "user.email=x@invalid", "commit", "-q", "-m", "base")
			repo, err := gitrepo.Open(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			before, err := repo.ReadCheckoutIdentity(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "index":
				writeCheckout(t, root, "file", []byte("staged"), 0600)
				runGit(t, root, "add", "file")
			case "head":
				runGit(t, root, "-c", "user.name=X", "-c", "user.email=x@invalid", "commit", "--allow-empty", "-q", "-m", "moved")
			case "ignore":
				writeCheckout(t, root, ".git/info/exclude", []byte("hidden-secret\n"), 0600)
			}
			if err := repo.CheckCheckoutIdentity(context.Background(), before); !errors.Is(err, gitrepo.ErrCheckoutChanged) {
				t.Fatalf("%s change accepted: %v", kind, err)
			}
		})
	}
}

func TestCheckoutIdentityTracksEffectiveGlobalIgnore(t *testing.T) {
	for _, kind := range []string{"xdg_existing", "xdg_created", "home_fallback", "explicit_path", "explicit_empty"} {
		t.Run(kind, func(t *testing.T) {
			configHome := t.TempDir()
			xdg := filepath.Join(configHome, "xdg")
			t.Setenv("HOME", configHome)
			t.Setenv("XDG_CONFIG_HOME", xdg)
			defaultPath := filepath.Join(xdg, "git", "ignore")
			if kind == "home_fallback" {
				t.Setenv("XDG_CONFIG_HOME", "")
				defaultPath = filepath.Join(configHome, ".config", "git", "ignore")
			}
			if kind != "xdg_created" {
				writeCheckout(t, filepath.Dir(defaultPath), filepath.Base(defaultPath), nil, 0600)
			}
			repo, _ := checkoutFixture(t)
			selectedPath := defaultPath
			if kind == "explicit_path" {
				selectedPath = filepath.Join(configHome, "custom ignore ")
				writeCheckout(t, configHome, filepath.Base(selectedPath), nil, 0600)
				runGit(t, repo.Root(), "config", "core.excludesFile", selectedPath)
			} else if kind == "explicit_empty" {
				runGit(t, repo.Root(), "config", "core.excludesFile", "")
			}
			ctx := context.Background()
			before, err := repo.ReadCheckoutIdentity(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "explicit_path" {
				writeCheckout(t, filepath.Dir(defaultPath), filepath.Base(defaultPath), []byte("hidden-file\n"), 0600)
				if err := repo.CheckCheckoutIdentity(ctx, before); err != nil {
					t.Fatalf("unused default ignore affected explicit setting: %v", err)
				}
			}
			writeCheckout(t, filepath.Dir(selectedPath), filepath.Base(selectedPath), []byte("hidden-file\n"), 0600)
			writeCheckout(t, repo.Root(), "hidden-file", []byte("must remain observable"), 0600)
			err = repo.CheckCheckoutIdentity(ctx, before)
			if kind == "explicit_empty" {
				if err != nil {
					t.Fatalf("disabled default ignore changed identity: %v", err)
				}
			} else if !errors.Is(err, gitrepo.ErrCheckoutChanged) {
				t.Fatalf("effective global ignore change was invisible: %v", err)
			}
			snapshot, err := repo.SnapshotTree(ctx, gitrepo.SnapshotSpec{BaseTree: before.HeadTree})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, file := range snapshot.Files {
				found = found || file.Path == "hidden-file"
			}
			if found != (kind == "explicit_empty") {
				t.Fatalf("Git ignore selection disagrees with identity, captured=%t", found)
			}
		})
	}
}

func TestPrivateIntegrationRefNeverMovesUserBranch(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	writeCheckout(t, root, "file", []byte("base"), 0600)
	runGit(t, root, "add", "file")
	runGit(t, root, "-c", "user.name=X", "-c", "user.email=x@invalid", "commit", "-q", "-m", "base")
	repo, err := gitrepo.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.ReadCheckoutIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/xgoal/goals/goal_1/integration"
	rev, created, err := repo.EnsureIntegrationRef(context.Background(), ref, before.HeadCommit)
	if err != nil || !created || rev.Commit != before.HeadCommit {
		t.Fatalf("create private ref: %#v %t %v", rev, created, err)
	}
	if _, _, err := repo.EnsureIntegrationRef(context.Background(), "refs/heads/main", before.HeadCommit); err == nil {
		t.Fatal("creation accepted a user branch")
	}
	if err := repo.UpdateRefCAS(context.Background(), "refs/heads/main", before.HeadCommit, before.HeadCommit); err == nil {
		t.Fatal("CAS accepted a user branch")
	}
	if err := repo.CheckCheckoutIdentity(context.Background(), before); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGit(t, root, "rev-parse", ref)); got != before.HeadCommit {
		t.Fatalf("private ref=%s", got)
	}
}

func TestPrivateIntegrationRefRejectsSymbolicUserBranch(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	writeCheckout(t, root, "file", []byte("base"), 0600)
	runGit(t, root, "add", "file")
	runGit(t, root, "-c", "user.name=X", "-c", "user.email=x@invalid", "commit", "-q", "-m", "base")
	ctx := context.Background()
	repo, err := gitrepo.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.ReadCheckoutIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next, err := repo.CreateCommit(ctx, gitrepo.CommitSpec{Tree: before.HeadTree, Parent: before.HeadCommit, Message: "private commit", Timestamp: time.Unix(1700000000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/xgoal/goals/symbolic/integration"
	runGit(t, root, "symbolic-ref", ref, "refs/heads/main")
	if _, _, err := repo.EnsureIntegrationRef(ctx, ref, before.HeadCommit); err == nil {
		t.Error("accepted a symbolic integration ref")
	}
	if err := repo.UpdateRefCAS(ctx, ref, next.ID, before.HeadCommit); err == nil {
		t.Error("updated a symbolic integration ref")
	}
	if err := repo.CheckCheckoutIdentity(ctx, before); err != nil {
		t.Fatal(err)
	}
}

func writeCheckout(t *testing.T, root, path string, content []byte, mode os.FileMode) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, content, mode); err != nil {
		t.Fatal(err)
	}
}

func checkoutFixture(t *testing.T) (*gitrepo.Repository, gitrepo.CheckoutIdentity) {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	writeCheckout(t, root, "file", []byte("base"), 0600)
	runGit(t, root, "add", "file")
	runGit(t, root, "-c", "user.name=X", "-c", "user.email=x@invalid", "commit", "-q", "-m", "base")
	repo, err := gitrepo.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repo.ReadCheckoutIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return repo, identity
}

func TestSnapshotRejectsUnsupportedOrAmbiguousSource(t *testing.T) {
	for _, kind := range []string{"fifo", "nested_repository", "escaping_symlink", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			repo, identity := checkoutFixture(t)
			switch kind {
			case "fifo":
				if err := unix.Mkfifo(filepath.Join(repo.Root(), "pipe"), 0600); err != nil {
					t.Fatal(err)
				}
			case "nested_repository":
				path := filepath.Join(repo.Root(), "nested")
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				runGit(t, path, "init", "-q")
				writeCheckout(t, path, "other", []byte("nested"), 0600)
			case "escaping_symlink":
				if err := os.Symlink("../outside", filepath.Join(repo.Root(), "escape")); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				writeCheckout(t, repo.Root(), "too-large", bytes.Repeat([]byte("x"), 32), 0600)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := repo.SnapshotTree(ctx, gitrepo.SnapshotSpec{BaseTree: identity.HeadTree, MaxFileBytes: 16}); err == nil {
				t.Fatalf("accepted %s source", kind)
			}
			if err := repo.CheckCheckoutIdentity(context.Background(), identity); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSnapshotPreservesMissingAndReadonlyUserIndex(t *testing.T) {
	for _, kind := range []string{"missing", "readonly"} {
		t.Run(kind, func(t *testing.T) {
			repo, base := checkoutFixture(t)
			index := filepath.Join(repo.CommonDir(), "index")
			if kind == "missing" {
				if err := os.Remove(index); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Chmod(index, 0400); err != nil {
				t.Fatal(err)
			}
			identity, err := repo.ReadCheckoutIdentity(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.SnapshotTree(context.Background(), gitrepo.SnapshotSpec{BaseTree: base.HeadTree}); err != nil {
				t.Fatal(err)
			}
			if err := repo.CheckCheckoutIdentity(context.Background(), identity); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(index)
			if kind == "missing" && !os.IsNotExist(err) {
				t.Fatalf("created user index: %v", err)
			}
			if kind == "readonly" && (err != nil || info.Mode().Perm() != 0400) {
				t.Fatalf("changed index permissions: %v", err)
			}
		})
	}
}

func TestBuildTreeRejectsCanonicalAliasesAndPreservesIndex(t *testing.T) {
	repo, identity := checkoutFixture(t)
	blob, err := repo.WriteBlob(context.Background(), []byte("content"))
	if err != nil {
		t.Fatal(err)
	}
	for _, paths := range [][]string{{"file", "FILE"}, {"caf\u00e9", "cafe\u0301"}, {"parent", "parent/child"}, {".git/config"}} {
		var entries []gitrepo.TreeEntry
		for _, path := range paths {
			entries = append(entries, gitrepo.TreeEntry{Path: path, Mode: "100644", ObjectID: blob})
		}
		if _, err := repo.BuildTree(context.Background(), entries); err == nil {
			t.Fatalf("accepted unsafe object tree paths: %q", paths)
		}
	}
	if err := repo.CheckCheckoutIdentity(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateCommitAndRefDoNotRunGitHooks(t *testing.T) {
	repo, identity := checkoutFixture(t)
	marker := filepath.Join(repo.Root(), "hook-ran")
	writeCheckout(t, repo.Root(), ".git/hooks/reference-transaction", []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700)
	commit, err := repo.CreateCommit(context.Background(), gitrepo.CommitSpec{Tree: identity.HeadTree, Parent: identity.HeadCommit, Message: "audit", Timestamp: time.Unix(1700000000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/xgoal/goals/hooks/integration"
	if _, _, err := repo.EnsureIntegrationRef(context.Background(), ref, identity.HeadCommit); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateRefCAS(context.Background(), ref, commit.ID, identity.HeadCommit); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Git hook ran: %v", err)
	}
	if err := repo.CheckCheckoutIdentity(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
}

func TestObjectReadsIgnoreReplaceRefs(t *testing.T) {
	repo, identity := checkoutFixture(t)
	entries, err := repo.ListTree(context.Background(), identity.HeadTree)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := repo.WriteBlob(context.Background(), []byte("replacement bytes"))
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root(), "replace", entries[0].ObjectID, replacement)
	content, err := repo.ReadBlob(context.Background(), entries[0].ObjectID, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "base" {
		t.Fatalf("immutable blob identity was overridden: %q", content)
	}
}
