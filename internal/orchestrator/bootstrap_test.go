package orchestrator

import (
	"context"
	"encoding/json"
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
	"github.com/monshunter/xgoal/internal/validator"
	"github.com/monshunter/xgoal/internal/workspace"
)

func TestBootstrapSourceMutationIsDriftEvenWhenCommandFails(t *testing.T) {
	for _, exitCode := range []int{0, 7} {
		t.Run(fmt.Sprint(exitCode), func(t *testing.T) {
			engine, snapshot, revision, registry := bootstrapFixture(t, fmt.Sprintf("printf changed > input.txt; exit %d", exitCode))
			_, _, err := engine.prepareEnvironment(context.Background(), snapshot, revision, registry, nil, nil)
			if !errors.Is(err, environment.ErrCheckoutDrift) {
				t.Fatalf("bootstrap source edit was not identified as drift: %v", err)
			}
			if err := engine.repository.CheckCheckoutIdentity(context.Background(), snapshot.Identity); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(filepath.Join(snapshot.Path, "input.txt")); err != nil || string(data) != "changed" {
				t.Fatalf("failure scene discarded: %q %v", data, err)
			}
		})
	}
}

func TestBootstrapDoesNotInheritProviderProfileEnvironment(t *testing.T) {
	t.Setenv("XGOAL_PROVIDER_TOKEN", "fixture-only-secret")
	t.Setenv("XGOAL_PROVIDER_ONLY", "fixture-only-setting")
	engine, snapshot, revision, registry := bootstrapFixture(t, `test -z "${XGOAL_PROVIDER_TOKEN:-}" && test -z "${XGOAL_PROVIDER_ONLY:-}"`)
	handle, observed, err := engine.prepareEnvironment(context.Background(), snapshot, revision, registry, nil, nil)
	if err != nil {
		t.Fatalf("bootstrap received Provider-only environment: %v", err)
	}
	defer engine.environment.Cleanup(context.Background(), handle)
	if strings.Contains(strings.Join(observed.EnvironmentNames, ","), "XGOAL_PROVIDER") {
		t.Fatalf("project environment contains Provider names: %v", observed.EnvironmentNames)
	}
}

func bootstrapFixture(t *testing.T, script string) (*Engine, workspace.Snapshot, domain.GoalRevision, *validator.Registry) {
	t.Helper()
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile("../../xgoal.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents[0].EnvironmentAllowlist = []string{"XGOAL_PROVIDER_TOKEN", "XGOAL_PROVIDER_ONLY"}
	cfg.Validators = nil
	cfg.Bootstrap.Commands = []config.Command{{ID: "prepare", Argv: []string{"sh", "-c", script}, TrustedFiles: []string{"xgoal.yaml"}, Timeout: config.Duration{Duration: 10 * time.Second}}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{"xgoal.yaml": data, "input.txt": []byte("original\n")} {
		if err := os.WriteFile(filepath.Join(root, path), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "xgoal.yaml", "input.txt"}, {"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	repo, err := gitrepo.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repo.ReadCheckoutIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := validator.LoadRegistry(ctx, repo, "HEAD", "xgoal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := t.TempDir()
	provider, err := environment.NewLocal(runtimeRoot, repo, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{repository: repo, projectRoot: root, runtimeRoot: runtimeRoot, environment: provider, configHash: registry.ConfigHash(), config: cfg}
	snapshot := workspace.Snapshot{ID: "bootstrap", Path: root, BaseCommit: identity.HeadCommit, BaseTree: identity.HeadTree, InputTree: identity.HeadTree, Identity: identity, ExecutionModel: workspace.ExecutionCurrentDirectory}
	return engine, snapshot, domain.GoalRevision{Hash: strings.Repeat("b", 64)}, registry
}
