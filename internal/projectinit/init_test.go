package projectinit

import (
	"context"
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
}

func TestInitializeRejectsDirtyRepositoryBeforeWriting(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "user.txt"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(context.Background(), Options{ProjectRoot: root, AvailableCommands: map[string]string{"codex": "codex"}}); err == nil {
		t.Fatal("dirty repository unexpectedly initialized")
	}
	if _, err := os.Stat(filepath.Join(root, "xgoal.yaml")); !os.IsNotExist(err) {
		t.Fatal("dirty failure wrote xgoal.yaml")
	}
}

func TestInitializeUsesRepositoryRootFromSubdirectoryAndRespectsOwner(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
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
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
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
