package validator_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/validator"
	"github.com/monshunter/xgoal/internal/workspace"
)

func TestRegistryIsFrozenToBaseAndCommandReceiptsCoverOutcomes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repositoryPath := filepath.Join(t.TempDir(), "repo")
	initializeValidatorRepository(t, repositoryPath)
	repository, err := gitrepo.Open(ctx, repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	base, err := repository.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := validator.LoadRegistry(ctx, repository, base.Commit, "xgoal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 5 || definitions[0].ID != "expected-nonzero" || definitions[4].ID != "trusted-script" {
		t.Fatalf("Definitions() = %+v", definitions)
	}
	trusted, exists := registry.Definition("trusted-script")
	if !exists || trusted.TrustedExecutableHash == "" || trusted.TrustedExecutablePath != "scripts/trusted.sh" {
		t.Fatalf("trusted definition = %+v, exists=%v", trusted, exists)
	}

	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	manager, err := workspace.NewManager(runtimeRoot, repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(ctx, workspace.Spec{
		ID: "workspace_validator", AttemptID: "attempt_validator", Kind: workspace.Validation,
		BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: registry.ConfigHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Cleanup(ctx, worktree.ID)
	provider, err := environment.NewLocal(runtimeRoot, repository, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := provider.Prepare(ctx, environment.Spec{
		ID: "environment_validator", WorktreePath: worktree.Path, BaseCommit: base.Commit, BaseTree: base.Tree,
		ConfigHash: registry.ConfigHash(), GoalRevisionHash: strings.Repeat("a", 64),
		ToolProbes: []environment.ToolProbe{{Name: "go", Argv: []string{"go", "version"}, Required: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Cleanup(ctx, handle)
	snapshot, err := provider.Snapshot(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	environmentHash, err := snapshot.Hash()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := validator.NewCommandRunner(runtimeRoot, registry, provider, handle)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(worktree.Path, "xgoal.yaml"), []byte("agent changed config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree.Path, "scripts", "trusted.sh"), []byte("#!/bin/sh\nprintf attacked > attacked.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		id   string
		want protocol.CommandResult
	}{
		{id: "go-version", want: protocol.CommandPassed},
		{id: "go-fail", want: protocol.CommandFailed},
		{id: "expected-nonzero", want: protocol.CommandPassed},
		{id: "timeout", want: protocol.CommandTimedOut},
		{id: "trusted-script", want: protocol.CommandUnavailable},
	}
	for index, test := range tests {
		receipt, err := runner.Run(ctx, validator.CommandRequest{
			RunID: "run_" + test.id, ValidatorID: test.id,
			GoalRevisionHash: strings.Repeat("a", 64), ConfigHash: registry.ConfigHash(),
			TreeHash: base.Tree, EnvironmentHash: environmentHash, MaxOutputBytes: 1 << 20,
		})
		if err != nil {
			t.Fatalf("Run(%s) error = %v", test.id, err)
		}
		if receipt.Result != test.want || receipt.DefinitionHash == "" || receipt.ConfigHash != registry.ConfigHash() || receipt.TreeHash != base.Tree {
			t.Errorf("Run(%s) receipt = %+v", test.id, receipt)
		}
		if _, err := receipt.Hash(); err != nil {
			t.Errorf("Run(%s) invalid receipt: %v", test.id, err)
		}
		stored, err := validator.ReadReceipt(runtimeRoot, receipt.ID)
		if err != nil {
			t.Errorf("ReadReceipt(%s) error = %v", test.id, err)
		} else if storedHash, hashErr := stored.Hash(); hashErr != nil || storedHash == "" {
			t.Errorf("ReadReceipt(%s) invalid = %+v, %v", test.id, stored, hashErr)
		}
		receiptInfo, err := os.Stat(filepath.Join(runtimeRoot, "validator", "receipts", receipt.ID+".json"))
		if err != nil || receiptInfo.Mode().Perm() != 0o600 {
			t.Errorf("Run(%s) receipt = %v, %v", test.id, receiptInfo, err)
		}
		for _, reference := range []string{receipt.StdoutRef, receipt.StderrRef} {
			info, err := os.Stat(filepath.Join(runtimeRoot, "validator", filepath.FromSlash(reference)))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("Run(%s) log %s = %v, %v", test.id, reference, info, err)
			}
		}
		if index == 0 {
			if _, err := runner.Run(ctx, validator.CommandRequest{
				RunID: "run_" + test.id, ValidatorID: test.id,
				GoalRevisionHash: strings.Repeat("a", 64), ConfigHash: registry.ConfigHash(),
				TreeHash: base.Tree, EnvironmentHash: environmentHash, MaxOutputBytes: 1 << 20,
			}); err == nil {
				t.Error("Run() overwrote immutable logs for a duplicate run id")
			}
		}
	}
	if err := os.WriteFile(filepath.Join(runtimeRoot, "validator", "logs", "run_go-version.stdout.log"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validator.ReadReceipt(runtimeRoot, "run_go-version"); err == nil {
		t.Fatal("ReadReceipt() accepted a log whose content no longer matches the receipt")
	}
	matchingLog := filepath.Join(t.TempDir(), "matching.log")
	if err := os.WriteFile(matchingLog, []byte("go version "+runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(runtimeRoot, "validator", "logs", "run_go-version.stdout.log")
	if err := os.Remove(stdoutPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(matchingLog, stdoutPath); err != nil {
		t.Fatal(err)
	}
	if _, err := validator.ReadReceipt(runtimeRoot, "run_go-version"); err == nil {
		t.Fatal("ReadReceipt() accepted a symlinked log with matching content")
	}
	if err := os.Remove(stdoutPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stdoutPath, []byte("go version "+runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	validatorRoot := filepath.Join(runtimeRoot, "validator")
	externalValidatorRoot := filepath.Join(t.TempDir(), "validator-copy")
	if err := os.Rename(validatorRoot, externalValidatorRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalValidatorRoot, validatorRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := validator.ReadReceipt(runtimeRoot, "run_go-version"); err == nil {
		t.Fatal("ReadReceipt() accepted an external validator directory symlink")
	}
	if err := os.Remove(validatorRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(externalValidatorRoot, validatorRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree.Path, "attacked.txt")); !os.IsNotExist(err) {
		t.Fatalf("modified trusted script executed: %v", err)
	}
}

func initializeValidatorRepository(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `apiVersion: xgoal.dev/v1alpha1
kind: Project
metadata:
  name: fixture
project:
  baseBranch: main
  trustedRepository: true
orchestration:
  defaultMode: standard
  maxParallel: 1
  leaseTTL: 90s
  heartbeatInterval: 20s
agents:
  - id: fake
    adapter: fake
    command: fake
    roles: [implementer]
    timeout: 1m
    providerTransport: deny
    credentialSource: none
    activeProbe: disabled
runtime:
  provider: local-process
  isolationLevelRequired: L0
  projectNetwork: deny
  projectSecrets: deny
validators:
  - id: go-version
    type: command
    phases: [change, final]
    argv: [go, version]
    timeout: 5s
    required: true
  - id: go-fail
    type: command
    phases: [change]
    argv: [go, version, invalid-argument]
    timeout: 5s
    required: true
  - id: expected-nonzero
    type: command
    phases: [change]
    argv: [false]
    timeout: 5s
    expectedExitCodes: [1]
    required: true
  - id: timeout
    type: command
    phases: [change]
    argv: [sleep, "2"]
    timeout: 50ms
    required: true
  - id: trusted-script
    type: command
    phases: [change]
    argv: [./scripts/trusted.sh]
    timeout: 5s
    required: true
`
	if err := os.WriteFile(filepath.Join(path, "xgoal.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "scripts", "trusted.sh"), []byte("#!/bin/sh\nprintf trusted\\n\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runValidatorGit(t, path, "init", "-b", "main")
	runValidatorGit(t, path, "add", "xgoal.yaml", "scripts/trusted.sh")
	runValidatorGit(t, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "fixture")
}

func runValidatorGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v error = %v\n%s", arguments, err, output)
	}
	return string(output)
}
