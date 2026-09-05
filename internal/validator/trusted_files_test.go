package validator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/validator"
)

// The requested candidate Tree already contains the changed script: ordinary
// checkout drift checks must pass, leaving the frozen trust boundary to reject it.
func TestFrozenInterpreterRejectsTamperedCandidateAndAcceptsNewBaseline(t *testing.T) {
	for _, test := range []struct{ name, command, extra, changed string }{
		{"interpreter", "[sh, scripts/trusted.sh]", "", "scripts/trusted.sh"},
		{"direct", "[./scripts/trusted.sh]", "", "scripts/trusted.sh"},
		{"interpreter-cwd", "[sh, trusted.sh]", "    cwd: scripts\n", "scripts/trusted.sh"},
		{"direct-cwd", "[./trusted.sh]", "    cwd: scripts\n", "scripts/trusted.sh"},
		{"dependency", "[sh, scripts/trusted.sh]", "    trustedFiles: [scripts/helper.sh]\n", "scripts/helper.sh"},
		{"wrapper-declaration", "[env, sh, scripts/trusted.sh]", "    trustedFiles: [scripts/trusted.sh]\n", "scripts/trusted.sh"},
	} {
		t.Run(test.name, func(t *testing.T) { testFrozenCandidate(t, test.command, test.extra, test.changed) })
	}
}

func testFrozenCandidate(t *testing.T, command, extra, changed string) {
	t.Helper()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo")
	initializeValidatorRepository(t, root)
	configPath := filepath.Join(root, "xgoal.yaml")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "    argv: [./scripts/trusted.sh]\n", "    argv: "+command+"\n"+extra, 1))
	if err := os.WriteFile(configPath, content, 0600); err != nil {
		t.Fatal(err)
	}
	if changed == "scripts/helper.sh" {
		if err := os.WriteFile(filepath.Join(root, changed), []byte("printf helper\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "scripts/trusted.sh"), []byte("#!/bin/sh\n. ./scripts/helper.sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	runValidatorGit(t, root, "add", "xgoal.yaml", "scripts")
	runValidatorGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "--no-verify", "-m", "interpreter baseline")
	repo, err := gitrepo.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	base, err := repo.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	old, err := validator.LoadRegistry(ctx, repo, base.Commit, "xgoal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, changed), []byte("#!/bin/sh\nprintf tampered\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	// A new user-reviewed commit represents a new Goal baseline. The old Goal
	// must still reject this same Tree under its old frozen definition.
	runValidatorGit(t, root, "add", changed)
	runValidatorGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "reviewed new baseline")
	candidate, err := repo.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	current, err := validator.LoadRegistry(ctx, repo, candidate.Commit, "xgoal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		registry *validator.Registry
		pass     bool
	}{
		{"old-goal", old, false}, {"new-goal", current, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeRoot := filepath.Join(t.TempDir(), "runtime")
			provider, err := environment.NewLocal(runtimeRoot, repo, clock.Real{})
			if err != nil {
				t.Fatal(err)
			}
			identity, err := repo.ReadCheckoutIdentity(ctx)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := provider.Prepare(ctx, environment.Spec{ID: "trusted_candidate", WorktreePath: repo.Root(), BaseCommit: candidate.Commit, BaseTree: candidate.Tree, Identity: identity, ConfigHash: tc.registry.ConfigHash(), GoalRevisionHash: strings.Repeat("a", 64), ToolProbes: []environment.ToolProbe{{Name: "git", Argv: []string{"git", "--version"}, Required: true}}})
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Cleanup(ctx, handle)
			runner, err := validator.NewCommandRunner(runtimeRoot, tc.registry, provider, handle)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := runner.Run(ctx, validator.CommandRequest{RunID: "candidate", ValidatorID: "trusted-script", GoalRevisionHash: strings.Repeat("a", 64), ConfigHash: tc.registry.ConfigHash(), TreeHash: candidate.Tree, EnvironmentHash: strings.Repeat("b", 64), MaxOutputBytes: 1 << 20})
			if (receipt.Result == protocol.CommandPassed) != tc.pass {
				t.Fatalf("pass=%v, receipt=%+v, error=%v", tc.pass, receipt, err)
			}
			if tc.pass && err != nil {
				t.Fatal(err)
			}
			if !tc.pass {
				if !errors.Is(err, validator.ErrTrustedFileChanged) {
					t.Fatalf("wrong rejection boundary: %v", err)
				}
				stdout, readErr := os.ReadFile(filepath.Join(runtimeRoot, "validator", filepath.FromSlash(receipt.StdoutRef)))
				if readErr != nil || len(stdout) != 0 {
					t.Fatalf("untrusted script executed: %q / %v", stdout, readErr)
				}
			}
		})
	}
	oldDef, _ := old.Definition("trusted-script")
	newDef, _ := current.Definition("trusted-script")
	if oldDef.Hash == newDef.Hash {
		t.Fatal("new trust baseline reused old definition/Evidence identity")
	}
}

func TestRegistryRejectsUndeclaredControlAndUnsafeFiles(t *testing.T) {
	for _, test := range []struct{ name, replacement string }{
		{"inline", "    argv: [sh, -c, 'exit 0']\n"},
		{"make", "    argv: [make, test]\n"},
		{"package", "    argv: [npm, test]\n"},
		{"options", "    argv: [python3, -u, scripts/trusted.sh]\n"},
		{"wrapper", "    argv: [env, sh, scripts/trusted.sh]\n"},
		{"versioned-interpreter", "    argv: [python3.11, scripts/check.py]\n"},
		{"busybox", "    argv: [busybox, sh, scripts/trusted.sh]\n"},
		{"unknown-runner", "    argv: [custom-runner, scripts/trusted.sh]\n"},
		{"go-run", "    argv: [go, run, scripts/check.go]\n"},
		{"go-exec", "    argv: [go, test, '-exec=./scripts/trusted.sh', ./...]\n"},
		{"go-vettool", "    argv: [go, vet, '-vettool=./scripts/trusted.sh', ./...]\n"},
		{"go-vettool-separated", "    argv: [go, vet, -vettool, ./scripts/trusted.sh, ./...]\n"},
		{"entry-escape", "    argv: [sh, ../trusted.sh]\n"},
		{"cwd-escape", "    argv: [./../trusted.sh]\n    cwd: scripts\n"},
		{"missing-dependency", "    argv: [sh, scripts/trusted.sh]\n    trustedFiles: [missing.sh]\n"},
		{"linked-dependency", "    argv: [sh, scripts/trusted.sh]\n    trustedFiles: [linked.sh]\n"},
		{"glob-dependency", "    argv: [sh, scripts/trusted.sh]\n    trustedFiles: ['scripts/*']\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "repo")
			initializeValidatorRepository(t, root)
			content, err := os.ReadFile(filepath.Join(root, "xgoal.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			content = []byte(strings.Replace(string(content), "    argv: [./scripts/trusted.sh]\n", test.replacement, 1))
			if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), content, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("scripts/trusted.sh", filepath.Join(root, "linked.sh")); err != nil {
				t.Fatal(err)
			}
			runValidatorGit(t, root, "add", "xgoal.yaml", "linked.sh")
			runValidatorGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "invalid control config")
			repo, err := gitrepo.Open(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := validator.LoadRegistry(ctx, repo, "HEAD", "xgoal.yaml"); err == nil {
				t.Fatal("unsafe validator declaration accepted")
			}
		})
	}
}

func TestServiceReadinessIsTrustedButBusinessEntryRemainsEditable(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo")
	initializeValidatorRepository(t, root)
	path := filepath.Join(root, "xgoal.yaml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	configuration := string(original) + `
services:
  - id: app
    argv: [python3, app.py]
    readiness: {argv: [sh, scripts/ready.sh], timeout: 5s, interval: 50ms}
    stopGracePeriod: 1s
`
	if err := os.WriteFile(path, []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.py"), []byte("print('business')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts/ready.sh"), []byte("exit 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runValidatorGit(t, root, "add", "xgoal.yaml", "app.py", "scripts/ready.sh")
	runValidatorGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "services")
	repository, err := gitrepo.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := validator.LoadRegistry(ctx, repository, "HEAD", "xgoal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	paths := "\n" + strings.Join(registry.ProtectedPaths(), "\n") + "\n"
	if !strings.Contains(paths, "\nscripts/ready.sh\n") || strings.Contains(paths, "\napp.py\n") {
		t.Fatalf("wrong trust boundary: %s", paths)
	}

	if err := os.WriteFile(filepath.Join(root, "app.py"), []byte("print('new business')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := registry.VerifyControl(root, "service/app/readiness"); err != nil {
		t.Fatalf("business source was frozen: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts/ready.sh"), []byte("exit 7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := registry.VerifyControl(root, "service/app/readiness"); !errors.Is(err, validator.ErrTrustedFileChanged) {
		t.Fatalf("readiness drift accepted: %v", err)
	}
}
