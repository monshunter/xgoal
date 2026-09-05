package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/workspace"
)

func TestBootstrapSourceMutationIsDriftEvenWhenCommandFails(t *testing.T) {
	for _, exitCode := range []int{0, 7} {
		t.Run(fmt.Sprint(exitCode), func(t *testing.T) {
			ctx := context.Background()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(root, "source")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "input.txt"), []byte("original\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"init", "-b", "main"}, {"add", "input.txt"}, {"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "fixture"}} {
				cmd := exec.Command("git", append([]string{"-C", source}, args...)...)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v %s", args, err, output)
				}
			}
			repository, err := gitrepo.Open(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := repository.ReadCheckoutIdentity(ctx)
			if err != nil {
				t.Fatal(err)
			}
			runtimeRoot := filepath.Join(root, "state")
			provider, err := environment.NewLocal(runtimeRoot, repository, clock.Real{})
			if err != nil {
				t.Fatal(err)
			}
			engine := &Engine{repository: repository, projectRoot: source, runtimeRoot: runtimeRoot, environment: provider, configHash: strings.Repeat("a", 64), config: config.Config{Bootstrap: config.Bootstrap{Commands: []config.Command{{ID: "mutating-bootstrap", Argv: []string{"sh", "-c", fmt.Sprintf("printf changed > input.txt; exit %d", exitCode)}, Timeout: config.Duration{Duration: time.Second}}}}}}
			snapshot := workspace.Snapshot{ID: "bootstrap", Path: source, BaseCommit: identity.HeadCommit, BaseTree: identity.HeadTree, InputTree: identity.HeadTree, Identity: identity, ExecutionModel: workspace.ExecutionCurrentDirectory}
			_, _, err = engine.prepareAttemptEnvironment(ctx, snapshot, domain.GoalRevision{Hash: strings.Repeat("b", 64)})
			if !errors.Is(err, environment.ErrCheckoutDrift) {
				t.Fatalf("bootstrap source edit was not identified as drift: %v", err)
			}
			if err := repository.CheckCheckoutIdentity(ctx, identity); err != nil {
				t.Fatal(err)
			}
			if content, err := os.ReadFile(filepath.Join(source, "input.txt")); err != nil || string(content) != "changed" {
				t.Fatalf("bootstrap failure scene was discarded: %q %v", content, err)
			}
		})
	}
}

func TestBootstrapDoesNotInheritProviderProfileEnvironment(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "--no-verify", "-m", "fixture"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, output)
		}
	}
	repository, err := gitrepo.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repository.ReadCheckoutIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := environment.NewLocal(t.TempDir(), repository, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XGOAL_PROVIDER_TOKEN", "fixture-only-secret")
	t.Setenv("XGOAL_PROVIDER_ONLY", "fixture-only-setting")
	engine := &Engine{repository: repository, projectRoot: root, environment: provider, configHash: strings.Repeat("a", 64), config: config.Config{Bootstrap: config.Bootstrap{Commands: []config.Command{{ID: "check-env", Argv: []string{"sh", "-c", `test -z "${XGOAL_PROVIDER_TOKEN:-}" && test -z "${XGOAL_PROVIDER_ONLY:-}"`}, Timeout: config.Duration{Duration: 10 * time.Second}}}}}}
	snapshot := workspace.Snapshot{ID: "env_boundary", Path: root, BaseCommit: identity.HeadCommit, BaseTree: identity.HeadTree, InputTree: identity.HeadTree, Identity: identity, ExecutionModel: workspace.ExecutionCurrentDirectory}
	engine.config.Agents = []config.Agent{{EnvironmentAllowlist: []string{"XGOAL_PROVIDER_TOKEN", "XGOAL_PROVIDER_ONLY"}}}
	handle, observed, err := engine.prepareAttemptEnvironment(ctx, snapshot, domain.GoalRevision{Hash: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatalf("bootstrap received Provider-only environment: %v", err)
	}
	defer provider.Cleanup(context.Background(), handle)
	if strings.Contains(strings.Join(observed.EnvironmentNames, ","), "XGOAL_PROVIDER") {
		t.Fatalf("project environment contains Provider names: %v", observed.EnvironmentNames)
	}
}
