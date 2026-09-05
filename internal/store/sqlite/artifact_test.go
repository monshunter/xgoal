package sqlite

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/patch"
	"github.com/monshunter/xgoal/internal/validator"
	"github.com/monshunter/xgoal/internal/workspace"
)

func TestM2ArtifactsPersistAndFailClosedOnDiskTamperingAcrossRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repositoryPath := filepath.Join(t.TempDir(), "repo")
	initializeArtifactRepository(t, repositoryPath)
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
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	source := clock.NewFake(time.Date(2026, 9, 2, 17, 0, 0, 0, time.UTC))
	store, err := Open(ctx, runtimeRoot, source)
	if err != nil {
		t.Fatal(err)
	}
	goal, work := seedReadyWork(t, store, "work_artifact")
	revision, err := store.GoalRevision(ctx, goal.ActiveRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	attempt := domain.Attempt{
		ID: "attempt_artifact", WorkItemID: work.ID, AgentProfileID: "fake",
		State: domain.AttemptCreated, BaseTree: base.Tree, PacketHash: strings.Repeat("1", 64), Version: 1,
	}
	lease, err := store.ClaimWork(ctx, work.ID, work.Version, LeaseDraft{
		ID: "lease_artifact", Holder: "daemon/worker", TTL: time.Hour,
	}, attempt, EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	_ = lease

	workspaces, err := workspace.NewManager(runtimeRoot, repository)
	if err != nil {
		t.Fatal(err)
	}
	misplacedWorkspaces, err := workspace.NewManager(filepath.Join(runtimeRoot, "nested-runtime"), repository)
	if err != nil {
		t.Fatal(err)
	}
	misplacedWorkspace, err := misplacedWorkspaces.Create(ctx, workspace.Spec{
		ID: "workspace_misplaced", AttemptID: attempt.ID, Kind: workspace.Attempt,
		BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: registry.ConfigHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := misplacedWorkspaces.Cleanup(ctx, misplacedWorkspace.ID); err != nil {
			t.Errorf("cleanup misplaced workspace: %v", err)
		}
	}()
	if _, _, err := store.RecordWorkspace(ctx, misplacedWorkspace); err == nil {
		t.Fatal("RecordWorkspace() accepted a workspace inside runtime but outside its fixed layout")
	}
	workspaceSnapshot, err := workspaces.Create(ctx, workspace.Spec{
		ID: "workspace_artifact", AttemptID: attempt.ID, Kind: workspace.Attempt,
		BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: registry.ConfigHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := workspaces.Cleanup(ctx, workspaceSnapshot.ID); err != nil {
			t.Errorf("cleanup workspace: %v", err)
		}
	}()
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER fail_workspace_artifact_event
BEFORE INSERT ON events
WHEN NEW.aggregate_type = 'workspace'
BEGIN
    SELECT RAISE(ABORT, 'injected workspace event failure');
END;`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordWorkspace(ctx, workspaceSnapshot); err == nil {
		t.Fatal("RecordWorkspace() committed despite injected event failure")
	}
	var workspaceRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE id = ?`, workspaceSnapshot.ID).Scan(&workspaceRows); err != nil || workspaceRows != 0 {
		t.Fatalf("workspace rows after rollback = %d, %v", workspaceRows, err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER fail_workspace_artifact_event`); err != nil {
		t.Fatal(err)
	}
	workspaceRecord, created, err := store.RecordWorkspace(ctx, workspaceSnapshot)
	if err != nil || !created || workspaceRecord.State != WorkspaceArtifactActive {
		t.Fatalf("RecordWorkspace() = %+v, %v, %v", workspaceRecord, created, err)
	}
	if _, created, err := store.RecordWorkspace(ctx, workspaceSnapshot); err != nil || created {
		t.Fatalf("idempotent RecordWorkspace() = created=%v, err=%v", created, err)
	}

	provider, err := environment.NewLocal(runtimeRoot, repository, source)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := provider.Prepare(ctx, environment.Spec{
		ID: "environment_artifact", WorktreePath: workspaceSnapshot.Path, Identity: workspaceSnapshot.Identity, ExcludePaths: workspaceSnapshot.ExcludePaths,
		BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: registry.ConfigHash(),
		GoalRevisionHash: revision.Hash,
		ToolProbes:       []environment.ToolProbe{{Name: "go", Argv: []string{"go", "version"}, Required: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Cleanup(ctx, handle); err != nil {
			t.Errorf("cleanup environment: %v", err)
		}
	}()
	environmentSnapshot, err := provider.Snapshot(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	environmentRecord, created, err := store.RecordEnvironmentSnapshot(ctx, workspaceSnapshot.ID, environmentSnapshot)
	if err != nil || !created || environmentRecord.Hash == "" {
		t.Fatalf("RecordEnvironmentSnapshot() = %+v, %v, %v", environmentRecord, created, err)
	}
	if _, created, err := store.RecordEnvironmentSnapshot(ctx, workspaceSnapshot.ID, environmentSnapshot); err != nil || created {
		t.Fatalf("idempotent RecordEnvironmentSnapshot() = created=%v, err=%v", created, err)
	}
	inserted, err := store.RecordValidatorRegistry(ctx, registry)
	if err != nil || inserted != 1 {
		t.Fatalf("RecordValidatorRegistry() = %d, %v", inserted, err)
	}
	if inserted, err := store.RecordValidatorRegistry(ctx, registry); err != nil || inserted != 0 {
		t.Fatalf("idempotent RecordValidatorRegistry() = %d, %v", inserted, err)
	}
	definition, exists := registry.Definition("go-version")
	if !exists {
		t.Fatal("go-version definition missing")
	}

	if err := os.WriteFile(filepath.Join(workspaceSnapshot.Path, "base.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	captured, err := patch.Capture(ctx, repository, patch.CaptureSpec{
		AttemptID: attempt.ID, ExecutionPath: workspaceSnapshot.Path, Identity: workspaceSnapshot.Identity, ExcludePaths: workspaceSnapshot.ExcludePaths,
		BaseCommit: base.Commit, BaseTree: base.Tree, MaxFileBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	patchStore, err := patch.NewStore(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	bundlePath, err := patchStore.Save(captured)
	if err != nil {
		t.Fatal(err)
	}
	patchRecord, created, err := store.RecordPatchBundle(ctx, captured.Bundle, bundlePath)
	if err != nil || !created || patchRecord.Bundle.BundleHash != captured.Bundle.BundleHash {
		t.Fatalf("RecordPatchBundle() = %+v, %v, %v", patchRecord, created, err)
	}
	if _, created, err := store.RecordPatchBundle(ctx, captured.Bundle, bundlePath); err != nil || created {
		t.Fatalf("idempotent RecordPatchBundle() = created=%v, err=%v", created, err)
	}

	candidate, err := repository.SnapshotTree(ctx, gitrepo.SnapshotSpec{BaseTree: workspaceSnapshot.BaseTree, ExcludePaths: workspaceSnapshot.ExcludePaths, MaxFileBytes: 1 << 20})
	candidateTree := candidate.Tree
	if err != nil {
		t.Fatal(err)
	}
	runner, err := validator.NewCommandRunner(runtimeRoot, registry, provider, handle)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := runner.Run(ctx, validator.CommandRequest{
		RunID: "validator_run_artifact", ValidatorID: "go-version",
		GoalRevisionHash: revision.Hash, ConfigHash: registry.ConfigHash(), TreeHash: candidateTree,
		EnvironmentHash: environmentRecord.Hash, MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	runRecord, created, err := store.RecordValidatorRun(ctx, attempt.ID, workspaceSnapshot.ID, receipt)
	if err != nil || !created || runRecord.Hash == "" {
		t.Fatalf("RecordValidatorRun() = %+v, %v, %v", runRecord, created, err)
	}
	if _, created, err := store.RecordValidatorRun(ctx, attempt.ID, workspaceSnapshot.ID, receipt); err != nil || created {
		t.Fatalf("idempotent RecordValidatorRun() = created=%v, err=%v", created, err)
	}
	if err := os.WriteFile(filepath.Join(repositoryPath, "unrelated.txt"), []byte("new base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runArtifactGit(t, repositoryPath, "add", "unrelated.txt")
	runArtifactGit(t, repositoryPath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "unrelated base change")
	secondBase, err := repository.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	secondRegistry, err := validator.LoadRegistry(ctx, repository, secondBase.Commit, "xgoal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	secondDefinition, exists := secondRegistry.Definition("go-version")
	if !exists || secondDefinition.Hash != definition.Hash || secondRegistry.ConfigHash() != registry.ConfigHash() {
		t.Fatal("unchanged validator definition/config did not survive an unrelated base change")
	}
	if registered, err := store.RecordValidatorRegistry(ctx, secondRegistry); err != nil || registered != 1 {
		t.Fatalf("RecordValidatorRegistry() across base = %d, %v", registered, err)
	}
	configuration, err := os.ReadFile(filepath.Join(repositoryPath, "xgoal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	changedConfiguration := strings.Replace(string(configuration), "leaseTTL: 90s", "leaseTTL: 91s", 1)
	if changedConfiguration == string(configuration) {
		t.Fatal("validator fixture config change was not applied")
	}
	if err := os.WriteFile(filepath.Join(repositoryPath, "xgoal.yaml"), []byte(changedConfiguration), 0o600); err != nil {
		t.Fatal(err)
	}
	runArtifactGit(t, repositoryPath, "add", "xgoal.yaml")
	runArtifactGit(t, repositoryPath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "unrelated config change")
	thirdBase, err := repository.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	thirdRegistry, err := validator.LoadRegistry(ctx, repository, thirdBase.Commit, "xgoal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	thirdDefinition, exists := thirdRegistry.Definition("go-version")
	if !exists || thirdDefinition.Hash != secondDefinition.Hash || thirdRegistry.ConfigHash() == secondRegistry.ConfigHash() {
		t.Fatal("unchanged validator definition did not retain identity across config evolution")
	}
	if registered, err := store.RecordValidatorRegistry(ctx, thirdRegistry); err != nil || registered != 1 {
		t.Fatalf("RecordValidatorRegistry() across config = %d, %v", registered, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, runtimeRoot, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if persisted, err := reopened.WorkspaceArtifact(ctx, workspaceSnapshot.ID); err != nil || persisted.Snapshot.MarkerHash != workspaceSnapshot.MarkerHash {
		t.Fatalf("reopened WorkspaceArtifact() = %+v, %v", persisted, err)
	}
	if persisted, err := reopened.PatchBundleArtifact(ctx, attempt.ID); err != nil || persisted.Bundle.BundleHash != captured.Bundle.BundleHash {
		t.Fatalf("reopened PatchBundleArtifact() = %+v, %v", persisted, err)
	}
	if persisted, err := reopened.EnvironmentArtifact(ctx, environmentSnapshot.ID); err != nil || persisted.Hash != environmentRecord.Hash {
		t.Fatalf("reopened EnvironmentArtifact() = %+v, %v", persisted, err)
	}
	if persisted, err := reopened.ValidatorDefinitionArtifact(ctx, definition.Hash); err != nil || persisted.Definition.Hash != definition.Hash {
		t.Fatalf("reopened ValidatorDefinitionArtifact() = %+v, %v", persisted, err)
	}
	if persisted, err := reopened.ValidatorRegistrationArtifact(ctx, secondRegistry.ConfigHash(), secondRegistry.BaseCommit(), secondDefinition.ID); err != nil || persisted.DefinitionHash != definition.Hash || persisted.BaseTree != secondRegistry.BaseTree() {
		t.Fatalf("reopened second ValidatorRegistrationArtifact() = %+v, %v", persisted, err)
	}
	if persisted, err := reopened.ValidatorRegistrationArtifact(ctx, thirdRegistry.ConfigHash(), thirdRegistry.BaseCommit(), thirdDefinition.ID); err != nil || persisted.DefinitionHash != definition.Hash || persisted.BaseTree != thirdRegistry.BaseTree() {
		t.Fatalf("reopened third ValidatorRegistrationArtifact() = %+v, %v", persisted, err)
	}
	var definitionCount, registrationCount int
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM validator_definitions`).Scan(&definitionCount); err != nil || definitionCount != 1 {
		t.Fatalf("validator definition count = %d, %v; want one immutable content object", definitionCount, err)
	}
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM validator_registrations`).Scan(&registrationCount); err != nil || registrationCount != 3 {
		t.Fatalf("validator registration count = %d, %v; want three provenance bindings", registrationCount, err)
	}
	if persisted, err := reopened.ValidatorRunArtifact(ctx, receipt.ID); err != nil || persisted.Hash != runRecord.Hash {
		t.Fatalf("reopened ValidatorRunArtifact() = %+v, %v", persisted, err)
	}
	if _, err := reopened.db.ExecContext(ctx, `UPDATE validator_definitions SET id = 'forged-validator' WHERE definition_hash = ?`, definition.Hash); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ValidatorRunArtifact(ctx, receipt.ID); err == nil {
		t.Fatal("ValidatorRunArtifact() accepted a registration detached from its immutable definition")
	}
	if _, err := reopened.db.ExecContext(ctx, `UPDATE validator_definitions SET id = ? WHERE definition_hash = ?`, definition.ID, definition.Hash); err != nil {
		t.Fatal(err)
	}
	externalWorkspaces, err := workspace.NewManager(filepath.Join(t.TempDir(), "external-runtime"), repository)
	if err != nil {
		t.Fatal(err)
	}
	externalWorkspace, err := externalWorkspaces.Create(ctx, workspace.Spec{
		ID: workspaceSnapshot.ID, AttemptID: attempt.ID, Kind: workspace.Attempt,
		BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: registry.ConfigHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := externalWorkspaces.Cleanup(ctx, externalWorkspace.ID); err != nil {
			t.Errorf("cleanup external workspace: %v", err)
		}
	}()
	if _, err := reopened.db.ExecContext(ctx, `
UPDATE workspaces
SET path = ?, common_dir = ?, base_commit = ?, base_tree = ?, config_hash = ?, marker_hash = ?, created_at = ?
WHERE id = ?`, externalWorkspace.Path, externalWorkspace.CommonDir, externalWorkspace.BaseCommit,
		externalWorkspace.BaseTree, externalWorkspace.ConfigHash, externalWorkspace.MarkerHash,
		externalWorkspace.CreatedAt.UTC().Format(time.RFC3339Nano), workspaceSnapshot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.WorkspaceArtifact(ctx, workspaceSnapshot.ID); err == nil {
		t.Fatal("WorkspaceArtifact() accepted a marker outside the project runtime")
	}
	if _, err := reopened.db.ExecContext(ctx, `
UPDATE workspaces
SET path = ?, common_dir = ?, base_commit = ?, base_tree = ?, config_hash = ?, marker_hash = ?, created_at = ?
WHERE id = ?`, workspaceSnapshot.Path, workspaceSnapshot.CommonDir, workspaceSnapshot.BaseCommit,
		workspaceSnapshot.BaseTree, workspaceSnapshot.ConfigHash, workspaceSnapshot.MarkerHash,
		workspaceSnapshot.CreatedAt.UTC().Format(time.RFC3339Nano), workspaceSnapshot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.db.ExecContext(ctx, `UPDATE patch_bundles SET bundle_path = ? WHERE attempt_id = ?`, filepath.Join(t.TempDir(), "external-bundle"), attempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.PatchBundleArtifact(ctx, attempt.ID); err == nil {
		t.Fatal("PatchBundleArtifact() accepted a bundle_path outside the project runtime")
	}
	if _, err := reopened.db.ExecContext(ctx, `UPDATE patch_bundles SET bundle_path = ? WHERE attempt_id = ?`, bundlePath, attempt.ID); err != nil {
		t.Fatal(err)
	}

	markerContent, err := os.ReadFile(workspaceSnapshot.MarkerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceSnapshot.MarkerPath, []byte(`{"forged":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.WorkspaceArtifact(ctx, workspaceSnapshot.ID); err == nil {
		t.Fatal("WorkspaceArtifact() accepted a tampered marker")
	}
	if err := os.WriteFile(workspaceSnapshot.MarkerPath, markerContent, 0o600); err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(bundlePath, filepath.FromSlash(captured.Bundle.Objects[0].Ref))
	objectContent, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(objectPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.PatchBundleArtifact(ctx, attempt.ID); err == nil {
		t.Fatal("PatchBundleArtifact() accepted a tampered object")
	}
	if err := os.WriteFile(objectPath, objectContent, 0o600); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(runtimeRoot, "validator", filepath.FromSlash(receipt.StdoutRef))
	stdoutContent, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stdoutPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ValidatorRunArtifact(ctx, receipt.ID); err == nil {
		t.Fatal("ValidatorRunArtifact() accepted a tampered log")
	}
	if err := os.WriteFile(stdoutPath, stdoutContent, 0o600); err != nil {
		t.Fatal(err)
	}
}

func initializeArtifactRepository(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := `apiVersion: xgoal.dev/v1alpha1
kind: Project
metadata:
  name: artifact-fixture
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
`
	if err := os.WriteFile(filepath.Join(path, "xgoal.yaml"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runArtifactGit(t, path, "init", "-b", "main")
	runArtifactGit(t, path, "add", "xgoal.yaml", "base.txt")
	runArtifactGit(t, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "fixture")
}

func runArtifactGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v error = %v\n%s", arguments, err, output)
	}
	return string(output)
}
