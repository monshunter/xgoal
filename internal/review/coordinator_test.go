package review_test

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
	"github.com/monshunter/xgoal/internal/patch"
	"github.com/monshunter/xgoal/internal/review"
	"github.com/monshunter/xgoal/internal/scope"
	"github.com/monshunter/xgoal/internal/validator"
	"github.com/monshunter/xgoal/internal/workspace"
)

func TestCoordinatorBindsActualPatchWorkspaceAndValidatorReceipt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Join(root, "repository")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := `apiVersion: xgoal.dev/v1alpha1
kind: Project
metadata:
  name: review-coordinator
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
  - id: exact-file
    type: command
    phases: [change]
    argv: [test, -f, accepted.txt]
    timeout: 10s
    required: true
`
	if err := os.WriteFile(filepath.Join(repositoryRoot, "xgoal.yaml"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	runReviewGit(t, repositoryRoot, "init", "-b", "main")
	runReviewGit(t, repositoryRoot, "add", "README.md", "xgoal.yaml")
	runReviewGit(t, repositoryRoot, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "fixture")
	repository, err := gitrepo.Open(ctx, repositoryRoot)
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
	runtimeRoot := filepath.Join(root, "runtime")
	manager, err := workspace.NewManager(runtimeRoot, repository)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := manager.Create(ctx, workspace.Spec{ID: "workspace_attempt_review", AttemptID: "attempt_review", Kind: workspace.Attempt, BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: registry.ConfigHash()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Cleanup(context.Background(), attempt.ID) })
	if err := os.WriteFile(filepath.Join(attempt.Path, "accepted.txt"), []byte("accepted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	captured, err := patch.Capture(ctx, repository, patch.CaptureSpec{AttemptID: "attempt_review", ExecutionPath: attempt.Path, Identity: attempt.Identity, ExcludePaths: attempt.ExcludePaths, BaseCommit: base.Commit, BaseTree: base.Tree, MaxFileBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	patchStore, err := patch.NewStore(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := patchStore.Save(captured); err != nil {
		t.Fatal(err)
	}
	validation, err := manager.Create(ctx, workspace.Spec{ID: "workspace_validation_review", AttemptID: "attempt_review", Kind: workspace.Validation, BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: registry.ConfigHash()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Cleanup(context.Background(), validation.ID) })
	policy, err := scope.NewPolicy([]string{"/accepted.txt"}, []string{"/.git/**"})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := patch.Replay(ctx, repository, patch.ReplaySpec{ExecutionPath: validation.Path, Identity: validation.Identity, ExcludePaths: validation.ExcludePaths, IntegrationCommit: base.Commit, IntegrationTree: base.Tree, Captured: captured, Policy: policy, MaxFileBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := environment.NewLocal(runtimeRoot, repository, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := provider.Prepare(ctx, environment.Spec{ID: "environment_review", WorktreePath: validation.Path, BaseCommit: validation.Identity.HeadCommit, BaseTree: validation.InputTree, Identity: validation.Identity, ExcludePaths: validation.ExcludePaths, ConfigHash: registry.ConfigHash(), GoalRevisionHash: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
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
	receipt, err := runner.Run(ctx, validator.CommandRequest{RunID: "validator_review", ValidatorID: "exact-file", GoalRevisionHash: strings.Repeat("a", 64), ConfigHash: registry.ConfigHash(), TreeHash: replayed.CandidateTree, EnvironmentHash: environmentHash, MaxOutputBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := review.NewCoordinator(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	input := review.PrepareInput{ID: "review_coordinated", GoalRevisionHash: strings.Repeat("a", 64), PlanRevisionHash: strings.Repeat("b", 64), WorkItemID: "work_review", ImplementationAttemptID: "attempt_review", ImplementationProfileID: "codex-implementer", ImplementationSessionID: "codex-session", ReviewerProfileID: "claude-reviewer", CandidateTree: replayed.CandidateTree, ValidationWorkspace: validation, ValidatorRunIDs: []string{receipt.ID}, RequiredChecks: []string{"correctness", "scope"}}
	artifact, err := coordinator.Prepare(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Packet.PatchBundleHash != captured.Bundle.BundleHash || artifact.Packet.CandidateTree != receipt.TreeHash || artifact.Packet.ValidatorReceipts[0].ID != receipt.ID {
		t.Fatalf("coordinated packet = %+v", artifact.Packet)
	}
	drifted := input
	drifted.ID = "review_drifted"
	drifted.CandidateTree = strings.Repeat("f", 40)
	if _, err := coordinator.Prepare(ctx, drifted); err == nil {
		t.Fatal("coordinator accepted a candidate tree not bound to receipt")
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, "accepted.txt"), []byte("edited after validation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	drifted = input
	drifted.ID = "review_source_drift"
	if _, err := coordinator.Prepare(ctx, drifted); !errors.Is(err, gitrepo.ErrCheckoutChanged) {
		t.Fatalf("review accepted stale validator evidence after source mutation: %v", err)
	}
	if err := repository.CheckCheckoutIdentity(ctx, validation.Identity); err != nil {
		t.Fatal(err)
	}

}
