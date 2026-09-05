package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/workspace"
)

func TestCurrentDirectorySessionRejectsChangedUserGitIdentity(t *testing.T) {
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
	manager, err := workspace.NewManager(filepath.Join(t.TempDir(), "runtime"), repo)
	if err != nil {
		t.Fatal(err)
	}
	session, err := manager.Create(ctx, workspace.Spec{ID: "session", AttemptID: "attempt", Kind: workspace.Attempt, BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agent.txt"), []byte("agent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "agent.txt")
	if _, err := manager.ReadBack(ctx, session.ID); err == nil {
		t.Fatal("session accepted a changed user index")
	}
	if err := manager.Cleanup(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "agent.txt")); err != nil {
		t.Fatal("cleanup removed user files")
	}
}

func TestWorkspaceReadBackRejectsTamperedMarker(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repositoryPath := filepath.Join(t.TempDir(), "repo")
	initializeRepository(t, repositoryPath)
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
	created, err := manager.Create(ctx, workspace.Spec{
		ID: "workspace_1", AttemptID: "attempt_1", Kind: workspace.Attempt,
		BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Cleanup(ctx, created.ID)
	if err := os.WriteFile(created.MarkerPath, []byte(`{"id":"forged"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReadBack(ctx, created.ID); err == nil {
		t.Fatal("ReadBack() accepted a tampered marker")
	}
}

func initializeRepository(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "add", "README.md")
	runGit(t, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "fixture")
}

func runGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v error = %v\n%s", arguments, err, output)
	}
	return string(output)
}
