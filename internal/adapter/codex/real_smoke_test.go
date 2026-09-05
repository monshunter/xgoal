package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/patch"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/scope"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/validator"
	"github.com/monshunter/xgoal/internal/workpacket"
	"github.com/monshunter/xgoal/internal/workspace"
)

const realSmokeConfig = `apiVersion: xgoal.dev/v1alpha1
kind: Project
metadata:
  name: codex-real-smoke
project:
  baseBranch: main
  trustedRepository: true
orchestration:
  defaultMode: standard
  maxParallel: 1
  leaseTTL: 30m
  heartbeatInterval: 1m
agents:
  - id: codex-real
    adapter: codex-cli
    command: codex
    roles: [implementer, reviewer]
    timeout: 5m
    providerTransport: allow
    credentialSource: cli-session
    activeProbe: explicit
runtime:
  provider: local-process
  isolationLevelRequired: L0
  projectNetwork: deny
  projectSecrets: deny
validators:
  - id: fast-file
    type: command
    phases: [change]
    argv: [test, -f, fast.txt]
    timeout: 10s
    required: true
  - id: standard-file
    type: command
    phases: [change]
    argv: [test, -f, standard.txt]
    timeout: 10s
    required: true
`

type realSmokeHarness struct {
	ctx         context.Context
	repository  *gitrepo.Repository
	base        gitrepo.Revision
	runtimeRoot string
	manager     *workspace.Manager
	packets     *workpacket.Store
	registry    *validator.Registry
	adapter     *Adapter
}

type realSmokeRun struct {
	attemptWorkspace    workspace.Snapshot
	validationWorkspace workspace.Snapshot
	captured            patch.Captured
	environment         protocol.EnvironmentSnapshot
	receipt             protocol.CommandReceipt
	result              protocol.AgentResult
	sessionID           string
	packetHash          string
	lease               *domain.Lease
}

// TestM3RealCodexFastAndStandardImplementer is deliberately opt-in because it
// uses the installed Codex login and provider transport. It is the M3 release
// gate, not a hermetic unit test.
func TestM3RealCodexFastAndStandardImplementer(t *testing.T) {
	if os.Getenv("XGOAL_RUN_CODEX_SMOKE") != "1" {
		t.Skip("set XGOAL_RUN_CODEX_SMOKE=1 to run the real Codex acceptance gate")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	harness := newRealSmokeHarness(t, ctx)

	capabilities, err := harness.adapter.Probe(ctx, adapter.ProbeSpec{
		Mode: adapter.ProbeActiveContract, ProfileID: "codex-real",
		ProviderTransport: true, Timeout: 3 * time.Minute,
	})
	if err != nil {
		logRealCodexDiagnostics(t, harness.adapter.root)
		t.Fatalf("active Codex contract probe: %v", err)
	}
	if capabilities.ProviderTransport != "available" || capabilities.CredentialStatus != "available" || capabilities.ProbeRef == "" {
		t.Fatalf("active Codex capabilities = %+v", capabilities)
	}

	fastGoalHash := strings.Repeat("a", 64)
	fast := harness.run(t, realWorkSpec{
		name: "fast", filename: "fast.txt", content: "fast goal accepted\n",
		goalID: "goal_real_fast", goalHash: fastGoalHash, planHash: strings.Repeat("b", 64),
		workID: "work_real_fast", attemptID: "attempt_real_fast", profileID: "codex-real-fast",
		validatorID: "fast-file", resume: true,
	})
	assertRealSmokeRun(t, harness.runtimeRoot, fast, "fast.txt", "fast goal accepted\n")

	// The two independent smoke goals use separate clean main checkouts.
	harness = newRealSmokeHarness(t, ctx)
	store, standardRevision, standardPlan, standardWork := seedRealStandardAttempt(t, ctx, harness)
	standard := harness.run(t, realWorkSpec{
		name: "standard", filename: "standard.txt", content: "standard implementer accepted\n",
		goalID: standardRevision.GoalID, goalHash: standardRevision.Hash, planHash: standardPlan.GraphHash,
		workID: standardWork.ID, attemptID: "attempt_real_standard", profileID: "codex-real-standard",
		validatorID: "standard-file", store: store,
	})
	assertRealSmokeRun(t, harness.runtimeRoot, standard, "standard.txt", "standard implementer accepted\n")
	if standard.lease == nil {
		t.Fatal("Standard smoke did not acquire a lease")
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, harness.runtimeRoot, clock.Real{})
	if err != nil {
		t.Fatalf("reopen Standard state: %v", err)
	}
	defer reopened.Close()
	persistedAttempt, err := reopened.Attempt(ctx, standard.lease.AttemptID)
	if err != nil || persistedAttempt.State != domain.AttemptValidating || persistedAttempt.Version != 6 || persistedAttempt.PacketHash != standard.packetHash {
		t.Fatalf("persisted Standard attempt = %+v, %v", persistedAttempt, err)
	}
	if _, err := reopened.WorkspaceArtifact(ctx, standard.validationWorkspace.ID); err != nil {
		t.Fatalf("read persisted validation workspace: %v", err)
	}
	if _, err := reopened.PatchBundleArtifact(ctx, standard.lease.AttemptID); err != nil {
		t.Fatalf("read persisted patch bundle: %v", err)
	}
	if _, err := reopened.ValidatorRunArtifact(ctx, standard.receipt.ID); err != nil {
		t.Fatalf("read persisted validator run: %v", err)
	}
	t.Logf("M3 real acceptance passed: Codex %s; fast session %s; Standard attempt %s; validator trees %s/%s",
		capabilities.Version, fast.sessionID, standard.lease.AttemptID, fast.receipt.TreeHash, standard.receipt.TreeHash)
}

type realWorkSpec struct {
	name, filename, content      string
	goalID, goalHash, planHash   string
	workID, attemptID, profileID string
	validatorID                  string
	resume                       bool
	store                        *sqlite.Store
}

func newRealSmokeHarness(t *testing.T, ctx context.Context) realSmokeHarness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Join(root, "repository")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("# Codex real smoke\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, "xgoal.yaml"), []byte(realSmokeConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealSmokeGit(t, repositoryRoot, "init", "-b", "main")
	runRealSmokeGit(t, repositoryRoot, "add", "README.md", "xgoal.yaml")
	runRealSmokeGit(t, repositoryRoot, "-c", "user.name=XGoal Smoke", "-c", "user.email=xgoal-smoke@example.invalid", "commit", "--no-verify", "-m", "fixture")

	repository, err := gitrepo.Open(ctx, repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	base, err := repository.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	manager, err := workspace.NewManager(runtimeRoot, repository)
	if err != nil {
		t.Fatal(err)
	}
	packets, err := workpacket.NewStore(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := validator.LoadRegistry(ctx, repository, base.Commit, "xgoal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatalf("locate real Codex CLI: %v", err)
	}
	environmentValues := make(map[string]string)
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "CODEX_HOME"} {
		if value, exists := os.LookupEnv(name); exists {
			environmentValues[name] = value
		}
	}
	adapterRuntime, err := New(Config{
		Binary: binary, RuntimeRoot: runtimeRoot, ProjectRoot: repositoryRoot,
		Environment: environmentValues, Clock: clock.Real{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return realSmokeHarness{
		ctx: ctx, repository: repository, base: base, runtimeRoot: runtimeRoot,
		manager: manager, packets: packets, registry: registry, adapter: adapterRuntime,
	}
}

func (harness realSmokeHarness) run(t *testing.T, spec realWorkSpec) realSmokeRun {
	t.Helper()
	workspaceID := "workspace_real_" + spec.name
	attemptWorkspace, err := harness.manager.Create(harness.ctx, workspace.Spec{
		ID: workspaceID, AttemptID: spec.attemptID, Kind: workspace.Attempt,
		BaseCommit: harness.base.Commit, BaseTree: harness.base.Tree, ConfigHash: harness.registry.ConfigHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	packet := protocol.WorkPacket{
		ProtocolVersion: protocol.WorkPacketVersion,
		Project:         protocol.PacketProject{Name: "codex-real-smoke", BaseTree: harness.base.Tree, Workspace: attemptWorkspace.Path},
		Goal:            protocol.PacketGoal{ID: spec.goalID, Revision: 1, Summary: spec.name + " Codex acceptance", ContractHash: spec.goalHash},
		WorkItem: protocol.PacketWorkItem{
			ID: spec.workID, Title: spec.name + " file", Objective: "create one exact file",
			ReadScope: []string{"/**"}, WriteScope: []string{"/" + spec.filename},
			AcceptanceCriteria: []string{"AC-" + strings.ToUpper(spec.name)}, ValidatorIDs: []string{spec.validatorID},
		},
		Role:                 domain.RoleImplementer,
		Constraints:          protocol.PacketConstraints{ProjectNetwork: "deny", ProjectSecrets: "deny"},
		RequiredOutputSchema: protocol.AgentResultVersion,
	}
	packetArtifact, created, err := harness.packets.Save(spec.attemptID, packet)
	if err != nil || !created {
		t.Fatalf("save %s packet = created %v, %v", spec.name, created, err)
	}

	var claimedLease *domain.Lease
	if spec.store != nil {
		attempt := domain.Attempt{
			ID: spec.attemptID, WorkItemID: spec.workID, AgentProfileID: spec.profileID,
			State: domain.AttemptCreated, BaseTree: harness.base.Tree, PacketHash: packetArtifact.Hash, Version: 1,
		}
		lease, err := spec.store.ClaimWork(harness.ctx, spec.workID, 2, sqlite.LeaseDraft{
			ID: "lease_real_" + spec.name, Holder: "m3-real-smoke", TTL: 30 * time.Minute,
		}, attempt, realSmokeEvent("LeaseAcquired"))
		if err != nil {
			t.Fatal(err)
		}
		claimedLease = &lease
		if _, created, err := spec.store.RecordWorkspace(harness.ctx, attemptWorkspace); err != nil || !created {
			t.Fatalf("record attempt workspace = created %v, %v", created, err)
		}
		advanceRealAttempt(t, harness.ctx, spec.store, *claimedLease, 1, domain.AttemptPreparing)
		advanceRealAttempt(t, harness.ctx, spec.store, *claimedLease, 2, domain.AttemptStarting)
		advanceRealAttempt(t, harness.ctx, spec.store, *claimedLease, 3, domain.AttemptRunning)
		if err := spec.store.UpdateWorkState(harness.ctx, spec.workID, 3, domain.WorkRunning, realSmokeEvent("WorkRunning")); err != nil {
			t.Fatal(err)
		}
	}

	schema, err := protocol.Schema(protocol.SchemaAgentResult)
	if err != nil {
		t.Fatal(err)
	}
	prompt := fmt.Sprintf("Execute this bounded xgoal work in the current Git workspace. The immutable Work Packet is at %s. Create only %s with exact UTF-8 content %q. Do not modify other files, do not commit, do not use network, and return the required structured AgentResult.", packetArtifact.Path, spec.filename, spec.content)
	invocation := adapter.Invocation{
		InvocationID: "invocation_real_" + spec.name, AttemptID: spec.attemptID, WorkItemID: spec.workID,
		ProfileID: spec.profileID, GoalRevisionHash: spec.goalHash, PlanRevisionHash: spec.planHash,
		BaseTree: harness.base.Tree, PacketHash: packetArtifact.Hash, Role: domain.RoleImplementer,
		WorkDir: attemptWorkspace.Path, PacketPath: packetArtifact.Path, Prompt: prompt,
		OutputSchema: schema, Environment: harness.adapter.environment, SandboxPolicy: "workspace-write",
		Timeout: 5 * time.Minute, MaxOutputBytes: 16 << 20, SessionPolicy: adapter.SessionFresh,
	}
	model := os.Getenv("XGOAL_SMOKE_CODEX_MODEL")
	if model == "" {
		model = "gpt-6-astra"
	}
	capabilities, err := harness.adapter.Probe(harness.ctx, adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: spec.profileID, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := (config.Agent{ID: spec.profileID, Adapter: "codex-cli", Roles: []string{"implementer"}, Model: model, ReasoningEffort: "low"}).Effective("implementer", capabilities.Version)
	if err != nil {
		t.Fatal(err)
	}
	invocation.ExecutionConfig = &effective
	invocation.PermissionMode = effective.PermissionMode
	handle, err := harness.adapter.Start(harness.ctx, invocation, nil)
	if err != nil {
		t.Fatalf("start real %s Codex: %v", spec.name, err)
	}
	result, err := harness.adapter.Wait(harness.ctx, handle)
	if err != nil {
		t.Fatalf("wait real %s Codex: %v", spec.name, err)
	}
	if result.Status != protocol.ResultCompleted {
		t.Fatalf("real %s AgentResult = %+v", spec.name, result)
	}
	sessionID, err := harness.adapter.SessionID(handle)
	if err != nil || sessionID == "" {
		t.Fatalf("real %s session = %q, %v", spec.name, sessionID, err)
	}
	if spec.resume {
		resume := invocation
		resume.InvocationID += "_resume"
		resume.Prompt = "Do not inspect or modify files. Return a completed AgentResult stating that the bounded xgoal session resume contract passed."
		resume.SessionPolicy = adapter.SessionResumeCompatible
		resumed, err := harness.adapter.Resume(harness.ctx, resume, sessionID, nil)
		if err != nil {
			t.Fatalf("resume real %s Codex: %v", spec.name, err)
		}
		if resumedResult, err := harness.adapter.Wait(harness.ctx, resumed); err != nil || resumedResult.Status != protocol.ResultCompleted {
			t.Fatalf("wait resumed real %s Codex = %+v, %v", spec.name, resumedResult, err)
		}
	}

	if spec.store != nil {
		advanceRealAttempt(t, harness.ctx, spec.store, *claimedLease, 4, domain.AttemptCollecting)
	}
	captured, err := patch.Capture(harness.ctx, harness.repository, patch.CaptureSpec{
		AttemptID: spec.attemptID, ExecutionPath: attemptWorkspace.Path, Identity: attemptWorkspace.Identity, ExcludePaths: attemptWorkspace.ExcludePaths,
		BaseCommit: harness.base.Commit, BaseTree: harness.base.Tree, MaxFileBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("capture real %s patch: %v", spec.name, err)
	}
	if spec.store != nil {
		patchStore, err := patch.NewStore(harness.runtimeRoot)
		if err != nil {
			t.Fatal(err)
		}
		bundlePath, err := patchStore.Save(captured)
		if err != nil {
			t.Fatal(err)
		}
		if _, created, err := spec.store.RecordPatchBundle(harness.ctx, captured.Bundle, bundlePath); err != nil || !created {
			t.Fatalf("record Standard patch = created %v, %v", created, err)
		}
		advanceRealAttempt(t, harness.ctx, spec.store, *claimedLease, 5, domain.AttemptValidating)
		if err := spec.store.UpdateWorkState(harness.ctx, spec.workID, 4, domain.WorkVerifying, realSmokeEvent("WorkVerifying")); err != nil {
			t.Fatal(err)
		}
	}

	policy, err := scope.NewPolicy([]string{"/" + spec.filename}, []string{"/.git/**"})
	if err != nil {
		t.Fatal(err)
	}
	validationWorkspace, err := harness.manager.Create(harness.ctx, workspace.Spec{
		ID: "validation_real_" + spec.name, AttemptID: spec.attemptID, Kind: workspace.Validation,
		BaseCommit: harness.base.Commit, BaseTree: harness.base.Tree, ConfigHash: harness.registry.ConfigHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := patch.Replay(harness.ctx, harness.repository, patch.ReplaySpec{
		ExecutionPath: validationWorkspace.Path, Identity: validationWorkspace.Identity, ExcludePaths: validationWorkspace.ExcludePaths, IntegrationCommit: harness.base.Commit, IntegrationTree: harness.base.Tree,
		Captured: captured, Policy: policy, MaxFileBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("replay real %s patch: %v", spec.name, err)
	}
	provider, err := environment.NewLocal(harness.runtimeRoot, harness.repository, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	handleEnvironment, err := provider.Prepare(harness.ctx, environment.Spec{
		ID: "environment_real_" + spec.name, WorktreePath: validationWorkspace.Path, Identity: validationWorkspace.Identity, ExcludePaths: validationWorkspace.ExcludePaths,
		BaseCommit: validationWorkspace.Identity.HeadCommit, BaseTree: validationWorkspace.InputTree,
		ConfigHash: harness.registry.ConfigHash(), GoalRevisionHash: spec.goalHash,
		ToolProbes: []environment.ToolProbe{{Name: "git", Argv: []string{"git", "--version"}, Required: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := provider.Snapshot(harness.ctx, handleEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	environmentHash, err := snapshot.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if spec.store != nil {
		if _, created, err := spec.store.RecordWorkspace(harness.ctx, validationWorkspace); err != nil || !created {
			t.Fatalf("record validation workspace = created %v, %v", created, err)
		}
		if _, created, err := spec.store.RecordEnvironmentSnapshot(harness.ctx, validationWorkspace.ID, snapshot); err != nil || !created {
			t.Fatalf("record environment = created %v, %v", created, err)
		}
		if registered, err := spec.store.RecordValidatorRegistry(harness.ctx, harness.registry); err != nil || registered != 2 {
			t.Fatalf("record validator registry = %d, %v", registered, err)
		}
	}
	runner, err := validator.NewCommandRunner(harness.runtimeRoot, harness.registry, provider, handleEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := runner.Run(harness.ctx, validator.CommandRequest{
		RunID: "validator_real_" + spec.name, ValidatorID: spec.validatorID,
		GoalRevisionHash: spec.goalHash, ConfigHash: harness.registry.ConfigHash(), TreeHash: replayed.CandidateTree,
		EnvironmentHash: environmentHash, MaxOutputBytes: 1 << 20,
	})
	if err != nil || receipt.Result != protocol.CommandPassed {
		t.Fatalf("real %s validator = %+v, %v", spec.name, receipt, err)
	}
	if spec.store != nil {
		if _, created, err := spec.store.RecordValidatorRun(harness.ctx, spec.attemptID, validationWorkspace.ID, receipt); err != nil || !created {
			t.Fatalf("record validator run = created %v, %v", created, err)
		}
	}
	return realSmokeRun{
		attemptWorkspace: attemptWorkspace, validationWorkspace: validationWorkspace,
		captured: captured, environment: snapshot, receipt: receipt, result: result, sessionID: sessionID,
		packetHash: packetArtifact.Hash, lease: claimedLease,
	}
}

func seedRealStandardAttempt(t *testing.T, ctx context.Context, harness realSmokeHarness) (*sqlite.Store, domain.GoalRevision, domain.PlanRevision, domain.WorkItem) {
	t.Helper()
	store, err := sqlite.Open(ctx, harness.runtimeRoot, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	goal := domain.Goal{ID: "goal_real_standard", State: domain.GoalDraft, Version: 1}
	if err := store.CreateGoal(ctx, goal, realSmokeEvent("GoalDrafted")); err != nil {
		t.Fatal(err)
	}
	revision, err := store.FreezeGoalRevision(ctx, sqlite.GoalRevisionDraft{
		ID: "goalrev_real_standard", GoalID: goal.ID, Revision: 1,
		RawGoal:  "create the bounded Standard acceptance file",
		Contract: map[string]any{"acceptance_criteria": []string{"AC-STANDARD"}},
	}, 1, realSmokeEvent("GoalRevisionFrozen"))
	if err != nil {
		t.Fatal(err)
	}
	work := domain.WorkItem{
		ID: "work_real_standard", PlanRevisionID: "planrev_real_standard", State: domain.WorkPending,
		Title: "standard file", Objective: "create one exact file",
		ReadScope: []string{"/**"}, WriteScope: []string{"/standard.txt"},
		AcceptanceCriteria: []string{"AC-STANDARD"}, ValidatorIDs: []string{"standard-file"},
		RecommendedRole: domain.RoleImplementer, Required: true, Version: 1,
	}
	plan, err := store.CreatePlanRevision(ctx, sqlite.PlanRevisionDraft{
		ID: work.PlanRevisionID, GoalRevisionID: revision.ID, Revision: 1, WorkItems: []domain.WorkItem{work},
	}, realSmokeEvent("PlanRevisionCreated"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivatePlanRevision(ctx, plan.ID, 1, 2, realSmokeEvent("PlanActivated")); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkState(ctx, work.ID, 1, domain.WorkReady, realSmokeEvent("WorkReady")); err != nil {
		t.Fatal(err)
	}

	work.State = domain.WorkReady
	work.Version = 2
	return store, revision, plan, work
}

func advanceRealAttempt(t *testing.T, ctx context.Context, store *sqlite.Store, lease domain.Lease, version int64, state domain.AttemptState) {
	t.Helper()
	if err := store.UpdateAttemptStateWithLease(ctx, lease.AttemptID, version, lease.ID, lease.Generation, state, realSmokeEvent("AttemptAdvanced")); err != nil {
		t.Fatal(err)
	}
}

func assertRealSmokeRun(t *testing.T, runtimeRoot string, run realSmokeRun, filename, content string) {
	t.Helper()
	if len(run.captured.Bundle.Entries) != 1 || run.captured.Bundle.Entries[0].Path != filename || run.captured.Bundle.Entries[0].Kind != protocol.PatchAdded {
		t.Fatalf("captured entries = %+v", run.captured.Bundle.Entries)
	}
	actual, err := os.ReadFile(filepath.Join(run.validationWorkspace.Path, filename))
	if err != nil || string(actual) != content {
		t.Fatalf("validated %s = %q, %v", filename, actual, err)
	}
	if _, err := workspace.ReadMarkerSnapshot(run.validationWorkspace.MarkerPath); err != nil {
		t.Fatalf("read validation workspace marker: %v", err)
	}
	readReceipt, err := validator.ReadReceipt(runtimeRoot, run.receipt.ID)
	if err != nil || readReceipt.TreeHash != run.receipt.TreeHash || readReceipt.Result != protocol.CommandPassed {
		t.Fatalf("independent receipt readback = %+v, %v", readReceipt, err)
	}
	if run.result.Authority() != domain.AuthorityClaim || run.environment.IsolationLevel != "L0" {
		t.Fatalf("authority/environment = %s/%s", run.result.Authority(), run.environment.IsolationLevel)
	}
}

func realSmokeEvent(eventType string) sqlite.EventInput {
	return sqlite.EventInput{Type: eventType, ActorType: "kernel", ActorID: "m3-real-smoke", Payload: map[string]any{}}
}

func runRealSmokeGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return string(output)
}

func logRealCodexDiagnostics(t *testing.T, root string) {
	t.Helper()
	var diagnostics strings.Builder
	err := filepath.WalkDir(filepath.Join(root, "invocations"), func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		if !strings.Contains(relative, string(filepath.Separator)+"events"+string(filepath.Separator)) && filepath.Base(filename) != "stderr.log" {
			return nil
		}
		content, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		if diagnostics.Len()+len(content) > 64<<10 {
			return nil
		}
		fmt.Fprintf(&diagnostics, "%s: %s\n", filepath.ToSlash(relative), content)
		return nil
	})
	if err != nil {
		t.Logf("read sanitized Codex diagnostics: %v", err)
		return
	}
	t.Logf("sanitized Codex diagnostics:\n%s", diagnostics.String())
}
