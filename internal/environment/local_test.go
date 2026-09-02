package environment_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/workspace"
)

func TestLocalProviderSnapshotsAllowlistedL0EnvironmentAndManagesServices(t *testing.T) {
	ctx := context.Background()
	repositoryPath := filepath.Join(t.TempDir(), "repo")
	initializeEnvironmentRepository(t, repositoryPath)
	repository, err := gitrepo.Open(ctx, repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	base, err := repository.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	manager, err := workspace.NewManager(runtimeRoot, repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(ctx, workspace.Spec{
		ID: "workspace_env", AttemptID: "attempt_env", Kind: workspace.Validation,
		BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Cleanup(ctx, worktree.ID)

	provider, err := environment.NewLocal(runtimeRoot, repository, clock.NewFake(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := provider.Probe(ctx, environment.Project{Root: repositoryPath})
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.Provider != "local-process" || capabilities.IsolationLevel != "L0" || capabilities.FilesystemIsolation || capabilities.NetworkIsolation || capabilities.CredentialIsolation != "L0" {
		t.Fatalf("Probe() capabilities = %+v", capabilities)
	}

	readyPath := filepath.Join(t.TempDir(), "ready")
	t.Setenv("XGOAL_ENV_HELPER", "1")
	t.Setenv("XGOAL_ENV_READY", readyPath)
	t.Setenv("XGOAL_ENV_SECRET", "must-not-be-forwarded")
	handle, err := provider.Prepare(ctx, environment.Spec{
		ID: "environment_1", WorktreePath: worktree.Path, BaseCommit: base.Commit, BaseTree: base.Tree,
		ConfigHash: strings.Repeat("b", 64), GoalRevisionHash: strings.Repeat("c", 64),
		EnvironmentAllowlist: []string{"XGOAL_ENV_HELPER", "XGOAL_ENV_READY"},
		Lockfiles:            []string{"go.sum"},
		ToolProbes:           []environment.ToolProbe{{Name: "go", Argv: []string{"go", "version"}, Required: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := provider.Snapshot(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	lockContent := []byte("fixture lock\n")
	if snapshot.IsolationLevel != "L0" || snapshot.BaseCommit != base.Commit || snapshot.BaseTree != base.Tree || snapshot.ToolVersions["go"] == "" || snapshot.ToolVersions["go"] == "unavailable" {
		t.Fatalf("Snapshot() = %+v", snapshot)
	}
	if snapshot.LockfileHashes["go.sum"] != fmt.Sprintf("%x", sha256.Sum256(lockContent)) {
		t.Fatalf("lockfile hash = %q", snapshot.LockfileHashes["go.sum"])
	}
	if !slices.Contains(snapshot.EnvironmentNames, "XGOAL_ENV_HELPER") || slices.Contains(snapshot.EnvironmentNames, "XGOAL_ENV_SECRET") {
		t.Fatalf("environment names = %v", snapshot.EnvironmentNames)
	}
	if _, err := snapshot.Hash(); err != nil {
		t.Fatalf("snapshot hash: %v", err)
	}

	err = provider.StartServices(ctx, handle, []environment.ServiceSpec{{
		ID: "service_1", Argv: []string{os.Args[0], "-test.run=^TestEnvironmentServiceHelper$"}, CWD: ".",
		EnvironmentAllowlist: []string{"XGOAL_ENV_HELPER", "XGOAL_ENV_READY"}, GracePeriod: 50 * time.Millisecond,
		ProbeTimeout: time.Second, ProbeInterval: 10 * time.Millisecond,
		Probe: func(context.Context) error {
			_, err := os.Stat(readyPath)
			return err
		},
	}})
	if err != nil {
		t.Fatalf("StartServices() error = %v", err)
	}
	if err := provider.StopServices(ctx, handle); err != nil {
		t.Fatalf("StopServices() error = %v", err)
	}
	for _, suffix := range []string{"stdout", "stderr"} {
		info, err := os.Stat(filepath.Join(handle.Root, "logs", "service_1."+suffix+".log"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("service %s log = %v, %v", suffix, info, err)
		}
	}
	forged := handle
	forged.Root = filepath.Join(t.TempDir(), "forged")
	if err := provider.Cleanup(ctx, forged); err == nil {
		t.Fatal("Cleanup() accepted a forged handle")
	}
	if err := provider.Cleanup(ctx, handle); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Lstat(handle.Root); !os.IsNotExist(err) {
		t.Fatalf("environment root remains after cleanup: %v", err)
	}
	if _, err := os.Stat(worktree.Path); err != nil {
		t.Fatalf("environment cleanup removed worktree: %v", err)
	}
}

func TestEnvironmentServiceHelper(t *testing.T) {
	if os.Getenv("XGOAL_ENV_HELPER") != "1" {
		return
	}
	if os.Getenv("XGOAL_ENV_SECRET") != "" {
		os.Exit(5)
	}
	if err := os.WriteFile(os.Getenv("XGOAL_ENV_READY"), []byte("ready"), 0o600); err != nil {
		os.Exit(6)
	}
	time.Sleep(time.Hour)
}

func initializeEnvironmentRepository(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	runEnvironmentGit(t, path, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(path, "go.sum"), []byte("fixture lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runEnvironmentGit(t, path, "add", "go.sum")
	runEnvironmentGit(t, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "fixture")
}

func runEnvironmentGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v error = %v\n%s", arguments, err, output)
	}
	return string(output)
}
