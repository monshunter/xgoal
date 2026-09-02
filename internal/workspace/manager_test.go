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

func TestIntegrationAndDetachedWorkspaceLifecyclePreservesUserCheckout(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repositoryPath := filepath.Join(t.TempDir(), "trusted repo")
	initializeRepository(t, repositoryPath)
	repository, err := gitrepo.Open(ctx, repositoryPath)
	if err != nil {
		t.Fatalf("gitrepo.Open() error = %v", err)
	}
	base, err := repository.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatalf("ResolveRevision() error = %v", err)
	}
	integration, created, err := repository.EnsureIntegrationBranch(ctx, "xgoal/goal_1/integration", base.Commit)
	if err != nil {
		t.Fatalf("EnsureIntegrationBranch() error = %v", err)
	}
	if !created || integration.Commit != base.Commit || integration.Tree != base.Tree {
		t.Fatalf("integration = %+v, created = %v", integration, created)
	}

	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	manager, err := workspace.NewManager(runtimeRoot, repository)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	attempt, err := manager.Create(ctx, workspace.Spec{
		ID: "workspace_attempt_1", AttemptID: "attempt_1", Kind: workspace.Attempt,
		BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("Create(attempt) error = %v", err)
	}
	if attempt.HeadCommit != base.Commit || attempt.HeadTree != base.Tree || attempt.CommonDir != repository.CommonDir() {
		t.Fatalf("attempt workspace = %+v", attempt)
	}
	if info, err := os.Stat(attempt.MarkerPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("attempt marker stat = %v, %v", info, err)
	}
	if err := os.WriteFile(filepath.Join(attempt.Path, "agent.txt"), []byte("agent\n"), 0o600); err != nil {
		t.Fatalf("write agent file: %v", err)
	}
	runGit(t, attempt.Path, "add", "agent.txt")
	runGit(t, attempt.Path, "-c", "user.name=Agent", "-c", "user.email=agent@example.invalid", "commit", "--no-verify", "-m", "agent commit")
	userHead := strings.TrimSpace(runGit(t, repositoryPath, "rev-parse", "HEAD"))
	integrationHead := strings.TrimSpace(runGit(t, repositoryPath, "rev-parse", "refs/heads/xgoal/goal_1/integration"))
	if userHead != base.Commit || integrationHead != base.Commit {
		t.Fatalf("agent commit changed user/integration refs: user=%s integration=%s base=%s", userHead, integrationHead, base.Commit)
	}

	readBack, err := manager.ReadBack(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("ReadBack() error = %v", err)
	}
	if readBack.HeadCommit == base.Commit || readBack.BaseCommit != base.Commit || readBack.Kind != workspace.Attempt {
		t.Fatalf("read-back workspace = %+v", readBack)
	}
	validation, err := manager.Create(ctx, workspace.Spec{
		ID: "workspace_validation_1", AttemptID: "attempt_1", Kind: workspace.Validation,
		BaseCommit: integration.Commit, BaseTree: integration.Tree, ConfigHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("Create(validation) error = %v", err)
	}
	if validation.HeadCommit != base.Commit || validation.Path == attempt.Path {
		t.Fatalf("validation workspace = %+v", validation)
	}
	if err := manager.Cleanup(ctx, validation.ID); err != nil {
		t.Fatalf("Cleanup(validation) error = %v", err)
	}
	if err := manager.Cleanup(ctx, attempt.ID); err != nil {
		t.Fatalf("Cleanup(attempt) error = %v", err)
	}
	for _, path := range []string{validation.Path, attempt.Path} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("cleaned worktree %s still exists: %v", path, err)
		}
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
