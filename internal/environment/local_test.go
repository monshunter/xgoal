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
	identity, err := repository.ReadCheckoutIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}

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
		ID: "environment_1", WorktreePath: repository.Root(), BaseCommit: base.Commit, BaseTree: base.Tree, Identity: identity,
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
	if _, err := os.Stat(filepath.Join(handle.Root, "logs", "service_1.stdout.log")); err != nil {
		t.Fatalf("environment cleanup discarded diagnostic logs: %v", err)
	}
	if _, err := provider.Snapshot(ctx, handle); err == nil {
		t.Fatal("cleanup did not revoke the environment handle")
	}
	if _, err := os.Stat(repository.Root()); err != nil {
		t.Fatalf("environment cleanup removed current source: %v", err)
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
	// The owner should terminate this service immediately after readiness.
	// Bound an orphan's lifetime and fail if normal ownership cleanup is lost.
	time.Sleep(30 * time.Second)
	os.Exit(124)
}

func TestLocalProviderUsesCurrentInputTreeWithoutMovingUserHEADOrIndex(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo")
	initializeEnvironmentRepository(t, root)
	repository, err := gitrepo.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	base, err := repository.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	indexBefore, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.sum"), []byte("accepted current content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	privateIndex := filepath.Join(t.TempDir(), "index")
	var candidate string
	for _, args := range [][]string{{"read-tree", base.Tree}, {"add", "--", "go.sum"}, {"write-tree"}} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		command.Env = append(os.Environ(), "GIT_INDEX_FILE="+privateIndex)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("candidate setup: %s: %v", output, err)
		}
		candidate = strings.TrimSpace(string(output))
	}
	provider, err := environment.NewLocal(t.TempDir(), repository, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repository.ReadCheckoutIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := provider.Prepare(ctx, environment.Spec{ID: "current_input", WorktreePath: repository.Root(), BaseCommit: base.Commit, BaseTree: candidate, Identity: identity,
		ConfigHash: strings.Repeat("a", 64), GoalRevisionHash: strings.Repeat("b", 64), ToolProbes: []environment.ToolProbe{{Name: "git", Argv: []string{"git", "--version"}, Required: true}}})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Cleanup(ctx, handle)
	snapshot, err := provider.Snapshot(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.BaseCommit != base.Commit || snapshot.BaseTree != candidate || handle.Worktree != repository.Root() {
		t.Fatalf("wrong current-directory environment: %+v / %+v", snapshot, handle)
	}
	indexAfter, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	if string(indexBefore) != string(indexAfter) || strings.TrimSpace(runEnvironmentGit(t, root, "rev-parse", "HEAD")) != base.Commit {
		t.Fatal("environment changed user's HEAD/index")
	}
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

func TestVersionProbesSeparateDiagnosticsAndRejectMalformedRequiredOutput(t *testing.T) {
	ctx := context.Background()
	projectPath := filepath.Join(t.TempDir(), "repo")
	initializeEnvironmentRepository(t, projectPath)
	repository, err := gitrepo.Open(ctx, projectPath)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repository.ReadCheckoutIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := environment.NewLocal(filepath.Join(t.TempDir(), "runtime"), repository, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	prepare := func(id string, probes []environment.ToolProbe) environment.Handle {
		t.Helper()
		handle, err := provider.Prepare(ctx, environment.Spec{
			ID: id, WorktreePath: repository.Root(), BaseCommit: identity.HeadCommit, BaseTree: identity.HeadTree, Identity: identity,
			ConfigHash: strings.Repeat("b", 64), GoalRevisionHash: strings.Repeat("c", 64), ToolProbes: probes,
		})
		if err != nil {
			t.Fatal(err)
		}
		return handle
	}
	malformed := []string{"sh", "-c", "printf 'first\\nsecond\\n'"}
	handle := prepare("version_diagnostics", []environment.ToolProbe{
		{Name: "warning", Argv: []string{"sh", "-c", "printf 'tool 1.0\\n'; printf 'PATH aliases unavailable\\n' >&2"}, Required: true},
		{Name: "stderr", Argv: []string{"sh", "-c", "printf 'tool 2.0\\n' >&2"}, Required: true},
		{Name: "optional", Argv: malformed},
	})
	snapshot, err := provider.Snapshot(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ToolVersions["warning"] != "tool 1.0" || snapshot.ToolVersions["stderr"] != "tool 2.0" || snapshot.ToolVersions["optional"] != "unavailable" {
		t.Fatalf("version attribution = %+v", snapshot.ToolVersions)
	}
	required := prepare("version_required", []environment.ToolProbe{{Name: "malformed", Argv: malformed, Required: true}})
	if _, err := provider.Snapshot(ctx, required); err == nil || !strings.Contains(err.Error(), `probe required tool "malformed"`) {
		t.Fatalf("malformed required version did not fail at the probe: %v", err)
	}
}
