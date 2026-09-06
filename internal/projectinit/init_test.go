package projectinit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/project"
)

func TestInitializeCreatesStrictProjectAndSharedIDWithoutRemoteEffects(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/project\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "go.mod")
	runGitEnv(t, root, []string{"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@invalid", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@invalid"}, "commit", "-q", "-m", "initial")
	head := runGitOutput(t, root, "rev-parse", "HEAD")
	index, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}

	result, err := Initialize(context.Background(), Options{ProjectRoot: root, AvailableCommands: map[string]string{"codex": "/usr/bin/codex", "claude": "/usr/bin/claude"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectID == "" || !result.CreatedConfig || !result.CreatedIgnore {
		t.Fatalf("result = %+v", result)
	}
	if result.ValidationPreparation.Status != "entrypoints_detected" || len(result.ValidationPreparation.Entries) != 1 || !result.ValidationPreparation.Entries[0].Configured || result.ValidationPreparation.Coverage != "not_verified" {
		t.Fatalf("missing init preparation: %+v", result.ValidationPreparation)
	}
	loaded, err := config.LoadFile(filepath.Join(root, "xgoal.yaml"))
	if err != nil {
		t.Fatalf("generated config is invalid: %v", err)
	}
	if len(loaded.Agents) != 2 || loaded.Project.BaseBranch != "main" {
		t.Fatalf("generated config = %+v", loaded)
	}
	assertMode(t, filepath.Join(root, ".xgoal"), 0o700)
	if value := strings.TrimSpace(runGitOutput(t, root, "config", "--local", "--get", "xgoal.projectID")); value != result.ProjectID {
		t.Fatalf("project id = %q, want %q", value, result.ProjectID)
	}
	if output := runGitOutput(t, root, "remote"); strings.TrimSpace(output) != "" {
		t.Fatalf("init changed remotes: %q", output)
	}

	again, err := Initialize(context.Background(), Options{ProjectRoot: root, AvailableCommands: map[string]string{"codex": "/usr/bin/codex", "claude": "/usr/bin/claude"}})
	if err != nil {
		t.Fatal(err)
	}
	if again.ProjectID != result.ProjectID || again.CreatedConfig || again.CreatedIgnore {
		t.Fatalf("idempotent result = %+v", again)
	}
	afterIndex, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil || string(index) != string(afterIndex) || runGitOutput(t, root, "rev-parse", "HEAD") != head {
		t.Fatalf("init changed an existing HEAD or index: %v", err)
	}
}

func TestInitializeCommitsUnbornRepositoryOnce(t *testing.T) {
	root := newUnbornInitRepository(t)
	// Ignore rules and ignored user content must survive; only the three named
	// initialization files are part of the first commit.
	writeInitFixture(t, root, ".gitignore", "private.txt\n")
	writeInitFixture(t, root, "private.txt", "user-owned\n")
	writeInitFixture(t, root, ".xgoalignore", ".git/\n.xgoal/\nprivate.txt\n")
	hooks := t.TempDir()
	writeInitFixture(t, hooks, "pre-commit", "#!/bin/sh\nexit 99\n")
	if err := os.Chmod(filepath.Join(hooks, "pre-commit"), 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "config", "core.hooksPath", hooks)
	runGit(t, root, "config", "commit.gpgSign", "true")
	runGit(t, root, "config", "gpg.program", "/missing/signing-program")
	options := Options{ProjectRoot: root, AvailableCommands: map[string]string{"codex": "codex"}}
	result, err := Initialize(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(runGitOutput(t, root, "rev-parse", "--verify", "HEAD^{commit}"))
	raw, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(raw), `"initial_commit":"`+head+`"`) {
		t.Fatalf("init did not report its commit: %s %v", raw, err)
	}
	if got := runGitOutput(t, root, "ls-tree", "-r", "--name-only", "HEAD"); got != ".gitignore\n.xgoalignore\nxgoal.yaml\n" {
		t.Fatalf("unexpected first commit paths: %s", got)
	}
	if got := runGitOutput(t, root, "status", "--porcelain=v1"); got != "" {
		t.Fatalf("init did not leave a clean baseline: %s", got)
	}
	for name, want := range map[string]string{"private.txt": "user-owned\n", ".gitignore": "private.txt\n.xgoal/\n", ".xgoalignore": ".git/\n.xgoal/\nprivate.txt\n"} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(got) != want {
			t.Fatalf("init changed %s: %q %v", name, got, err)
		}
	}
	again, err := Initialize(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(again)
	if strings.Contains(string(raw), `"initial_commit"`) || runGitOutput(t, root, "rev-list", "--count", "HEAD") != "1\n" {
		t.Fatalf("repeat init created or reported another commit: %s", raw)
	}
}

func TestInitializeCommitFailureCanRetry(t *testing.T) {
	for _, failure := range []string{"identity", "index-lock"} {
		t.Run(failure, func(t *testing.T) {
			root := newUnbornInitRepository(t)
			if failure == "identity" {
				t.Setenv("GIT_AUTHOR_NAME", "")
				runGit(t, root, "config", "user.name", "")
			} else {
				writeInitFixture(t, root, ".git/index.lock", "other-owner\n")
			}
			options := Options{ProjectRoot: root, AvailableCommands: map[string]string{"codex": "codex"}}
			if _, err := Initialize(context.Background(), options); err == nil || !strings.Contains(err.Error(), "initial commit") {
				t.Fatalf("missing actionable commit failure: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, ".git/refs/heads/main")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed init created a HEAD: %v", err)
			}
			configuration, err := os.ReadFile(filepath.Join(root, "xgoal.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if failure == "identity" {
				t.Setenv("GIT_AUTHOR_NAME", "Fixture")
				runGit(t, root, "config", "user.name", "Fixture")
			} else {
				if err := os.Remove(filepath.Join(root, ".git/index.lock")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Initialize(context.Background(), options); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(filepath.Join(root, "xgoal.yaml"))
			if err != nil || string(after) != string(configuration) || runGitOutput(t, root, "rev-list", "--count", "HEAD") != "1\n" {
				t.Fatalf("retry overwrote config or duplicated commit: %v", err)
			}
		})
	}
}

func TestInitializeDoesNotCommitRuntimeIndexEntries(t *testing.T) {
	root := newUnbornInitRepository(t)
	if err := os.Mkdir(filepath.Join(root, ".xgoal"), 0700); err != nil {
		t.Fatal(err)
	}
	writeInitFixture(t, root, ".xgoal/user-note", "owned\n")
	runGit(t, root, "add", "--", ".xgoal/user-note")
	if _, err := Initialize(context.Background(), Options{ProjectRoot: root, AvailableCommands: map[string]string{"codex": "codex"}}); err != nil {
		t.Fatal(err)
	}
	if got := runGitOutput(t, root, "ls-tree", "-r", "--name-only", "HEAD"); got != ".gitignore\n.xgoalignore\nxgoal.yaml\n" {
		t.Fatalf("runtime data leaked into commit: %s", got)
	}
	if got := runGitOutput(t, root, "diff", "--cached", "--name-only"); got != ".xgoal/user-note\n" {
		t.Fatalf("user staging was lost: %s", got)
	}
}

func newUnbornInitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "Fixture")
	runGit(t, root, "config", "user.email", "fixture@invalid")
	return root
}

func writeInitFixture(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeRejectsDirtyRepositoryBeforeWriting(t *testing.T) {
	for _, staged := range []bool{false, true} {
		root := newUnbornInitRepository(t)
		writeInitFixture(t, root, "user.txt", "owned\n")
		if staged {
			runGit(t, root, "add", "--", "user.txt")
			writeInitFixture(t, root, "user.txt", "owned unstaged edit\n")
		}
		before := runGitOutput(t, root, "status", "--porcelain=v1")
		if _, err := Initialize(context.Background(), Options{ProjectRoot: root, AvailableCommands: map[string]string{"codex": "codex"}}); err == nil {
			t.Fatal("dirty repository unexpectedly initialized")
		}
		if _, err := os.Stat(filepath.Join(root, "xgoal.yaml")); !os.IsNotExist(err) {
			t.Fatal("dirty failure wrote xgoal.yaml")
		}
		if after := runGitOutput(t, root, "status", "--porcelain=v1"); after != before {
			t.Fatalf("init changed unrelated user staging: %s -> %s", before, after)
		}
	}
}

func TestInitializeRejectsBrokenHead(t *testing.T) {
	root := newUnbornInitRepository(t)
	writeInitFixture(t, root, ".git/refs/heads/main", "broken-reference\n")
	if _, err := Initialize(context.Background(), Options{ProjectRoot: root, AvailableCommands: map[string]string{"codex": "codex"}}); err == nil {
		t.Fatal("broken HEAD was treated as an unborn branch")
	}
	ref, err := os.ReadFile(filepath.Join(root, ".git/refs/heads/main"))
	if err != nil || string(ref) != "broken-reference\n" {
		t.Fatalf("init replaced the broken reference: %s %v", ref, err)
	}
}

func TestInitializeUsesRepositoryRootFromSubdirectoryAndRespectsOwner(t *testing.T) {
	root := newUnbornInitRepository(t)
	subdir := filepath.Join(root, "sub")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	paths, err := project.Resolve(context.Background(), subdir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := project.Acquire(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Initialize(context.Background(), Options{ProjectRoot: subdir, AvailableCommands: map[string]string{"codex": "codex"}})
	if !errors.Is(err, project.ErrAlreadyRunning) {
		t.Fatalf("init did not respect project owner: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "xgoal.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed init wrote config")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := Initialize(context.Background(), Options{ProjectRoot: subdir, AvailableCommands: map[string]string{"codex": "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectRoot != paths.ProjectRoot || result.StateDir != paths.StateDir {
		t.Fatalf("wrong initialization root: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(subdir, "xgoal.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created parallel subdirectory configuration")
	}
	if _, err := os.Stat(filepath.Join(result.StateDir, "state.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("init opened SQLite")
	}
}

func TestInitializePersistsExplicitStateOverrideWithoutCreatingDefaultState(t *testing.T) {
	root := newUnbornInitRepository(t)
	state := filepath.Join(t.TempDir(), "state")
	result, err := Initialize(context.Background(), Options{ProjectRoot: root, StateDir: state, AvailableCommands: map[string]string{"codex": "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := project.Resolve(context.Background(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.StateDir != resolved.StateDir {
		t.Fatalf("override was not bound: %+v / %+v", result, resolved)
	}
	if _, err := os.Stat(filepath.Join(root, ".xgoal")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("init created an unused default state directory")
	}
	assertMode(t, result.StateDir, 0700)
}

func TestInitializeRejectsLinkedEntryWithoutChangingRepository(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGitEnv(t, root, []string{"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@invalid", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@invalid"}, "commit", "--allow-empty", "-q", "-m", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, root, "worktree", "add", "-b", "other", linked)
	before := runGitOutput(t, root, "worktree", "list", "--porcelain")
	head := runGitOutput(t, root, "rev-parse", "HEAD")
	linkedHead := runGitOutput(t, linked, "rev-parse", "HEAD")
	indexes := map[string][]byte{}
	indexExists := map[string]bool{}
	for _, dir := range []string{root, linked} {
		path := strings.TrimSpace(runGitOutput(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "index"))
		contents, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		indexes[path], indexExists[path] = contents, err == nil
	}
	config, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(context.Background(), Options{ProjectRoot: linked, AvailableCommands: map[string]string{"codex": "codex"}}); err == nil || !strings.Contains(err.Error(), "linked worktree") {
		t.Fatalf("linked init accepted: %v", err)
	}
	for _, dir := range []string{root, linked} {
		for _, name := range []string{"xgoal.yaml", ".xgoalignore", ".gitignore", ".xgoal"} {
			if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected init wrote %s", filepath.Join(dir, name))
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "xgoal")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected init created ownership directory")
	}
	afterConfig, _ := os.ReadFile(filepath.Join(root, ".git", "config"))
	if string(afterConfig) != string(config) || runGitOutput(t, root, "rev-parse", "HEAD") != head || runGitOutput(t, linked, "rev-parse", "HEAD") != linkedHead || runGitOutput(t, root, "worktree", "list", "--porcelain") != before {
		t.Fatal("rejected init changed Git metadata")
	}
	for path, before := range indexes {
		after, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if string(before) != string(after) || indexExists[path] != (err == nil) {
			t.Fatalf("rejected init changed index %s", path)
		}
	}
}

func runGit(t *testing.T, root string, args ...string) { t.Helper(); runGitEnv(t, root, nil, args...) }

func runGitEnv(t *testing.T, root string, environment []string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = append(os.Environ(), environment...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), output, err)
	}
}

func runGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), output, err)
	}
	return string(output)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("mode %o, want %o", info.Mode().Perm(), want)
	}
}
