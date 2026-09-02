package projectinit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/config"
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
