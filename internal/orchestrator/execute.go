package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/evidence"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/patch"
	"github.com/monshunter/xgoal/internal/promotion"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/reconcile"
	"github.com/monshunter/xgoal/internal/review"
	"github.com/monshunter/xgoal/internal/scope"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/supervisor"
	"github.com/monshunter/xgoal/internal/validator"
	"github.com/monshunter/xgoal/internal/workspace"
)

var (
	errProjectNetworkGate    = errors.New("bootstrap requires project network authorization")
	errValidatorChange       = errors.New("attempt changes the trusted validator configuration")
	errExecutionStillRunning = errors.New("execution shutdown could not be confirmed")
)

type validationEvidence struct {
	ID        string
	Validator string
	Receipt   protocol.CommandReceipt
	Hash      string
	Flaky     bool
}

func (engine *Engine) executeWork(ctx context.Context, goal domain.Goal, work domain.WorkItem) error {
	revision, err := engine.store.GoalRevision(ctx, goal.ActiveRevisionID)
	if err != nil {
		return err
	}
	plan, err := engine.store.PlanRevision(ctx, work.PlanRevisionID)
	if err != nil {
		return err
	}
	frozen, err := decodeFrozenContract(revision.ContractJSON)
	if err != nil {
		return err
	}
	profile, err := engine.implementationProfile(work.RecommendedRole)
	if err != nil {
		return err
	}
	runtimeAdapter := engine.adapters[profile.ID]
	if runtimeAdapter == nil {
		return fmt.Errorf("profile %q has no runtime adapter", profile.ID)
	}
	capabilities, err := runtimeAdapter.Probe(ctx, adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: profile.ID, Timeout: 10 * time.Second})
	if err != nil {
		return engine.failUnclaimed(ctx, goal, work, revision, reconcile.AgentUnavailable, err, profile.ID)
	}
	integrationRef, integration, err := engine.integration(ctx, goal.ID)
	if err != nil {
		return err
	}
	registry, err := validator.LoadRegistry(ctx, engine.repository, integration.Commit, "xgoal.yaml")
	if err != nil {
		return engine.failUnclaimed(ctx, goal, work, revision, reconcile.ValidatorUnavailable, err, profile.ID)
	}
	if registry.ConfigHash() != engine.configHash || frozen.ConfigHash != engine.configHash {
		return engine.failUnclaimed(ctx, goal, work, revision, reconcile.InternalInvariantViolation, errors.New("frozen, running, and Git config hashes differ"), profile.ID)
	}
	if _, err := engine.store.RecordValidatorRegistry(ctx, registry); err != nil {
		return err
	}
	attemptID, err := randomID("attempt")
	if err != nil {
		return err
	}
	workspaceID, err := randomID("workspace")
	if err != nil {
		return err
	}
	attemptWorkspace, err := engine.workspaces.Create(ctx, workspace.Spec{
		ID: workspaceID, AttemptID: attemptID, Kind: workspace.Attempt,
		BaseCommit: integration.Commit, BaseTree: integration.Tree, ConfigHash: engine.configHash,
	})
	if err != nil {
		return err
	}
	claimed := false
	defer func() {
		if !claimed {
			_ = engine.workspaces.Cleanup(context.Background(), attemptWorkspace.ID)
		}
	}()
	dependencies, err := engine.store.WorkDependencyIDs(ctx, work.ID)
	if err != nil {
		return err
	}
	var priorAttempt *protocol.PacketPriorAttempt
	if previous, previousErr := engine.store.LatestWorkFailure(ctx, work.ID); previousErr == nil {
		references, evidenceErr := engine.store.CurrentWorkEvidenceIDs(ctx, work.ID)
		if evidenceErr != nil {
			return evidenceErr
		}
		priorAttempt = &protocol.PacketPriorAttempt{FailureClass: string(previous.Class), FailureFingerprint: previous.Fingerprint, EvidenceRefs: references}
	} else if !errors.Is(previousErr, basestore.ErrNotFound) {
		return previousErr
	}
	packet := protocol.WorkPacket{
		ProtocolVersion: protocol.WorkPacketVersion,
		Project:         protocol.PacketProject{Name: engine.config.Metadata.Name, BaseTree: integration.Tree, Workspace: attemptWorkspace.Path},
		Goal:            protocol.PacketGoal{ID: goal.ID, Revision: revision.Revision, Summary: frozen.Contract.Summary, ContractHash: revision.Hash},
		WorkItem: protocol.PacketWorkItem{
			ID: work.ID, Title: work.Title, Objective: work.Objective, Dependencies: dependencies,
			ReadScope: work.ReadScope, WriteScope: work.WriteScope,
			AcceptanceCriteria: work.AcceptanceCriteria, ValidatorIDs: work.ValidatorIDs,
		},
		Role: work.RecommendedRole,
		Constraints: protocol.PacketConstraints{
			ProjectNetwork: engine.config.Runtime.ProjectNetwork, ProjectSecrets: engine.config.Runtime.ProjectSecrets,
			GitPush: false, Production: false,
		},
		Environment: protocol.PacketEnvironment{
			OS: runtime.GOOS, Arch: runtime.GOARCH, GitCommit: integration.Commit,
			ToolVersions: map[string]string{profile.Adapter: capabilities.Version}, LockfileHashes: map[string]string{},
		},
		PriorAttempt:         priorAttempt,
		RequiredOutputSchema: protocol.AgentResultVersion,
	}
	packetArtifact, _, err := engine.packets.Save(attemptID, packet)
	if err != nil {
		return err
	}
	leaseID, err := randomID("lease")
	if err != nil {
		return err
	}
	attempt := domain.Attempt{
		ID: attemptID, WorkItemID: work.ID, AgentProfileID: profile.ID,
		State: domain.AttemptCreated, BaseTree: integration.Tree, PacketHash: packetArtifact.Hash, Version: 1,
	}
	lease, err := engine.store.ClaimWork(ctx, work.ID, work.Version, sqlite.LeaseDraft{
		ID: leaseID, Holder: "daemon/orchestrator", TTL: engine.config.Orchestration.LeaseTTL.Duration,
	}, attempt, event("WorkClaimed", "kernel", map[string]any{"attempt_id": attemptID, "packet_hash": packetArtifact.Hash}))
	if err != nil {
		return err
	}
	claimed = true
	var environments []environment.Handle
	cleanupEnvironments := func() error {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		var result error
		for index := len(environments) - 1; index >= 0; index-- {
			result = errors.Join(result, engine.environment.Cleanup(cleanupContext, environments[index]))
		}
		if result == nil {
			environments = nil
		}
		return result
	}
	defer cleanupEnvironments()
	fail := func(class reconcile.FailureClass, cause error, strategy, patchHash string) error {
		if err := cleanupEnvironments(); err != nil {
			cause = errors.Join(cause, errExecutionStillRunning, err)
		}
		return engine.failAttempt(ctx, goal, work, revision, lease, class, cause, strategy, patchHash)
	}
	ownedContext := supervisor.WithOwner(ctx, engine.store, supervisor.Owner{Kind: "attempt", ID: attempt.ID, GoalID: goal.ID, Generation: lease.Generation})
	attemptContext, cancelAttempt := context.WithCancelCause(ownedContext)
	engine.registerWork(work.ID, cancelAttempt)
	defer engine.unregisterWork(work.ID)
	heartbeatContext, stopHeartbeat := context.WithCancel(attemptContext)
	heartbeatDone := make(chan struct{})
	go engine.keepLeaseAlive(heartbeatContext, lease, cancelAttempt, heartbeatDone)
	defer func() {
		stopHeartbeat()
		<-heartbeatDone
	}()
	ctx = attemptContext
	if _, _, err := engine.store.RecordWorkspace(ctx, attemptWorkspace); err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	if err := engine.advanceAttempt(ctx, lease, domain.AttemptPreparing); err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}

	attemptEnvironment, environmentSnapshot, err := engine.prepareAttemptEnvironment(ctx, attemptWorkspace, revision, profile)
	if err != nil {
		class := reconcile.EnvironmentPrepFailed
		if errors.Is(err, errProjectNetworkGate) {
			class = reconcile.PolicyBlocked
		}
		if _, evidenceErr := engine.recordEnvironmentFailureEvidence(ctx, work.ID, revision, integration.Tree, "attempt", err); evidenceErr != nil {
			return fail(reconcile.InternalInvariantViolation, errors.Join(err, evidenceErr), profile.ID, "")
		}
		return fail(class, err, profile.ID, "")
	}
	environments = append(environments, attemptEnvironment)
	if _, _, err := engine.store.RecordEnvironmentSnapshot(ctx, attemptWorkspace.ID, environmentSnapshot); err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	if err := engine.advanceAttempt(ctx, lease, domain.AttemptStarting); err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	if err := engine.advanceAttempt(ctx, lease, domain.AttemptRunning); err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	if err := engine.advanceWork(ctx, work.ID, domain.WorkRunning); err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}

	schema, err := protocol.Schema(protocol.SchemaAgentResult)
	if err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	invocationID, err := randomID("invoke")
	if err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	invocation := adapter.Invocation{
		InvocationID: invocationID, AttemptID: attemptID, WorkItemID: work.ID, ProfileID: profile.ID,
		GoalRevisionHash: revision.Hash, PlanRevisionHash: plan.GraphHash, BaseTree: integration.Tree,
		PacketHash: packetArtifact.Hash, Role: work.RecommendedRole, WorkDir: attemptWorkspace.Path,
		PacketPath:   packetArtifact.Path,
		Prompt:       "Execute only the immutable Work Packet at " + packetArtifact.Path + ". Do not commit, push, publish, access project secrets, or exceed its scopes. Return only the required structured AgentResult; completion is a claim that xgoal will independently verify.",
		OutputSchema: schema, Environment: profileEnvironment(profile), Timeout: profile.Timeout.Duration,
		MaxOutputBytes: maxAgentOutput, SessionPolicy: adapter.SessionFresh,
	}
	if profile.Adapter == "codex-cli" {
		invocation.SandboxPolicy = sandboxFor(work.RecommendedRole)
	} else {
		invocation.PermissionMode = "dontAsk"
		invocation.ToolPolicy = toolsFor(work.RecommendedRole)
	}
	handle, err := runtimeAdapter.Start(ctx, invocation, nil)
	if err != nil {
		return fail(classifyAgentError(err), err, profile.ID, "")
	}
	claim, waitErr := engine.waitAgent(ctx, runtimeAdapter, handle)
	if waitErr != nil {
		return fail(classifyAgentError(waitErr), waitErr, profile.ID, "")
	}
	if err := claim.Validate(); err != nil {
		return fail(reconcile.AgentProtocolInvalid, err, profile.ID, "")
	}
	if claim.Status != protocol.ResultCompleted {
		return fail(reconcile.AgentProtocolInvalid, fmt.Errorf("agent returned %s: %s", claim.Status, claim.Summary), profile.ID, "")
	}
	sessionID, err := agentSessionID(runtimeAdapter, handle)
	if err != nil {
		return fail(reconcile.AgentProtocolInvalid, err, profile.ID, "")
	}
	if err := engine.advanceAttempt(ctx, lease, domain.AttemptCollecting); err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	captured, err := patch.Capture(ctx, engine.repository, patch.CaptureSpec{
		AttemptID: attemptID, ExecutionPath: attemptWorkspace.Path, Identity: attemptWorkspace.Identity, ExcludePaths: attemptWorkspace.ExcludePaths,
		BaseCommit: integration.Commit, BaseTree: integration.Tree, MaxFileBytes: maxPatchFile,
	})
	if err != nil {
		class := reconcile.PatchEmpty
		if !strings.Contains(err.Error(), "no changes") {
			class = reconcile.ScopeViolation
		}
		return fail(class, err, profile.ID, "")
	}
	if changesValidatorConfig(captured) && engine.config.ScopePolicy.ValidatorChanges == "human-gate" {
		return fail(reconcile.PolicyBlocked, errValidatorChange, profile.ID, captured.Bundle.BundleHash)
	}
	bundlePath, err := engine.patches.Save(captured)
	if err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, captured.Bundle.BundleHash)
	}
	if _, _, err := engine.store.RecordPatchBundle(ctx, captured.Bundle, bundlePath); err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, captured.Bundle.BundleHash)
	}

	validationID, err := randomID("validation")
	if err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	validationWorkspace, err := engine.workspaces.Create(ctx, workspace.Spec{
		ID: validationID, AttemptID: attemptID, Kind: workspace.Validation,
		BaseCommit: integration.Commit, BaseTree: integration.Tree, ConfigHash: engine.configHash,
	})
	if err != nil {
		return fail(reconcile.PatchConflict, err, profile.ID, captured.Bundle.BundleHash)
	}
	if _, _, err := engine.store.RecordWorkspace(ctx, validationWorkspace); err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, captured.Bundle.BundleHash)
	}
	policy, err := scope.NewPolicy(work.WriteScope, engine.config.ScopePolicy.Deny)
	if err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, captured.Bundle.BundleHash)
	}
	replayed, err := patch.Replay(ctx, engine.repository, patch.ReplaySpec{
		ExecutionPath: validationWorkspace.Path, Identity: validationWorkspace.Identity, ExcludePaths: validationWorkspace.ExcludePaths, IntegrationCommit: integration.Commit, IntegrationTree: integration.Tree,
		Captured: captured, Policy: policy, MaxFileBytes: maxPatchFile,
	})
	if err != nil {
		class := reconcile.PatchConflict
		if errors.Is(err, patch.ErrUnsafeReplay) || strings.Contains(strings.ToLower(err.Error()), "scope") {
			class = reconcile.ScopeViolation
		}
		return fail(class, err, profile.ID, captured.Bundle.BundleHash)
	}
	if err := engine.advanceAttempt(ctx, lease, domain.AttemptValidating); err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	if err := engine.advanceWork(ctx, work.ID, domain.WorkVerifying); err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	validationHandle, _, err := engine.prepareValidationEnvironment(ctx, validationWorkspace, revision)
	if err != nil {
		if _, evidenceErr := engine.recordEnvironmentFailureEvidence(ctx, work.ID, revision, replayed.CandidateTree, "change-validation", err); evidenceErr != nil {
			return fail(reconcile.InternalInvariantViolation, errors.Join(err, evidenceErr), profile.ID, captured.Bundle.BundleHash)
		}
		return fail(reconcile.EnvironmentPrepFailed, err, profile.ID, captured.Bundle.BundleHash)
	}
	environments = append(environments, validationHandle)
	validation, err := engine.runValidators(ctx, registry, validationHandle, validationWorkspace, revision, attemptID, work.ID, replayed.CandidateTree, work.ValidatorIDs, evidence.SetChange)
	if err != nil {
		return fail(classifyValidatorError(err), err, profile.ID, captured.Bundle.BundleHash)
	}
	evidenceIDs := make([]string, len(validation))
	runIDs := make([]string, len(validation))
	for index, item := range validation {
		evidenceIDs[index], runIDs[index] = item.ID, item.Receipt.ID
	}
	setID, err := randomID("evidence_set_change")
	if err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	changeSet, err := evidence.NewSet(setID, evidence.SetChange, revision.Hash, engine.configHash, replayed.CandidateTree, evidenceIDs, time.Now().UTC())
	if err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	if err := engine.store.CreateEvidenceSet(ctx, changeSet); err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, captured.Bundle.BundleHash)
	}

	if frozen.Mode == "standard" && engine.config.Review.RequiredInStandard {
		if err := engine.advanceAttempt(ctx, lease, domain.AttemptReviewing); err != nil {
			return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
		}
		if err := engine.runReview(ctx, profile, sessionID, revision, plan, work, validationWorkspace, replayed.CandidateTree, runIDs); err != nil {
			return fail(reconcile.ReviewBlocked, err, profile.ID, captured.Bundle.BundleHash)
		}
	}
	if err := engine.advanceAttempt(ctx, lease, domain.AttemptPromoting); err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	if err := cleanupEnvironments(); err != nil {
		return fail(reconcile.EnvironmentPrepFailed, err, profile.ID, captured.Bundle.BundleHash)
	}
	if err := engine.checkWorkspaceTree(ctx, validationWorkspace, replayed.CandidateTree); err != nil {
		return fail(reconcile.PatchConflict, err, profile.ID, captured.Bundle.BundleHash)
	}
	promotionID, err := randomID("promotion")
	if err != nil {
		return fail(reconcile.InternalInvariantViolation, err, profile.ID, "")
	}
	observation, err := engine.promotions.Promote(ctx, promotion.Request{
		ID: promotionID, EffectID: "effect_" + promotionID,
		EffectKey: "project/" + goal.ID + "/" + work.ID + "/" + attemptID + "/promotion/" + fmt.Sprint(lease.Generation),
		GoalID:    goal.ID, GoalRevision: revision.Revision, GoalRevisionHash: revision.Hash, ConfigHash: engine.configHash,
		WorkItemID: work.ID, AttemptID: attemptID, LeaseID: lease.ID, LeaseGeneration: lease.Generation,
		BundleHash: captured.Bundle.BundleHash, EvidenceSetID: changeSet.ID,
		IntegrationRef: integrationRef, OldCommit: integration.Commit, OldTree: integration.Tree,
		CandidateTree: replayed.CandidateTree, CommitAt: time.Now().UTC(),
		ExecutionModel: promotion.CurrentDirectory, ExecutionPath: validationWorkspace.Path, CheckoutIdentity: &validationWorkspace.Identity, ExcludePaths: validationWorkspace.ExcludePaths,
	})
	if err != nil {
		// An external ref effect may already exist. Preserve its lease and
		// journal intent until recovery can prove and observe the candidate.
		recoveryContext, stopRecovery := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stopRecovery()
		pending, readErr := engine.store.RecoverablePromotions(recoveryContext)
		if readErr != nil {
			return errors.Join(err, readErr)
		}
		for _, record := range pending {
			if record.ID == promotionID {
				return engine.openPromotionRecoveryGate(recoveryContext, goal, record, err)
			}
		}
		return fail(reconcile.PatchConflict, err, profile.ID, captured.Bundle.BundleHash)
	}
	if observation.IntegrationTree != replayed.CandidateTree {
		return errors.New("promotion observation tree mismatch")
	}
	_, err = engine.store.RefreshReadyWork(ctx, goal.ID, event("WorkReady", "kernel", map[string]any{"promoted_work": work.ID}))
	return err
}

func (engine *Engine) integration(ctx context.Context, goalID string) (string, gitrepo.Revision, error) {
	identity, err := engine.repository.ReadCheckoutIdentity(ctx)
	if err != nil {
		return "", gitrepo.Revision{}, err
	}
	baseTree := identity.HeadTree
	previous, err := engine.store.Checkout(ctx)
	if err == nil && previous.Identity == identity {
		baseTree = previous.AcceptedTree
	} else if err != nil && !errors.Is(err, basestore.ErrNotFound) {
		return "", gitrepo.Revision{}, err
	}
	current, err := engine.repository.SnapshotTree(ctx, gitrepo.SnapshotSpec{BaseTree: baseTree, ExcludePaths: []string{engine.runtimeRoot}, MaxFileBytes: maxPatchFile})
	if err != nil {
		return "", gitrepo.Revision{}, err
	}
	checkout, err := engine.store.AdmitCheckout(ctx, goalID, current.Identity, current.Tree)
	if err != nil {
		return "", gitrepo.Revision{}, err
	}
	ref := "refs/xgoal/goals/" + goalID + "/integration"
	revision, _, err := engine.repository.EnsureIntegrationRef(ctx, ref, checkout.AcceptedCommit)
	if err == nil && (revision.Commit != checkout.AcceptedCommit || revision.Tree != checkout.AcceptedTree) {
		err = errors.New("private integration ref differs from the accepted checkout; recover the pending Promotion before continuing")
	}
	return ref, revision, err
}

func (engine *Engine) prepareAttemptEnvironment(ctx context.Context, snapshot workspace.Snapshot, revision domain.GoalRevision, profile config.Agent) (environment.Handle, protocol.EnvironmentSnapshot, error) {
	bootstrapHash := ""
	if len(engine.config.Bootstrap.Commands) > 0 {
		var err error
		bootstrapHash, err = canonical.Hash("bootstrap", config.APIVersion, engine.config.Bootstrap.Commands)
		if err != nil {
			return environment.Handle{}, protocol.EnvironmentSnapshot{}, err
		}
	}
	handle, err := engine.environment.Prepare(ctx, environment.Spec{
		ID: "environment_" + snapshot.ID, WorktreePath: snapshot.Path,
		BaseCommit: snapshot.Identity.HeadCommit, BaseTree: snapshot.InputTree,
		Identity: snapshot.Identity, ExcludePaths: snapshot.ExcludePaths,
		ConfigHash: engine.configHash, GoalRevisionHash: revision.Hash,
		EnvironmentAllowlist: profile.EnvironmentAllowlist, BootstrapHash: bootstrapHash,
	})
	if err != nil {
		return environment.Handle{}, protocol.EnvironmentSnapshot{}, err
	}
	for _, command := range engine.config.Bootstrap.Commands {
		if command.Network == "require-gate" || (command.Network == "allow" && engine.config.Runtime.ProjectNetwork != "allow") {
			_ = engine.environment.Cleanup(context.Background(), handle)
			return environment.Handle{}, protocol.EnvironmentSnapshot{}, errProjectNetworkGate
		}
		commandContext, cancel := context.WithTimeout(ctx, command.Timeout.Duration)
		execution, runErr := engine.environment.RunCommand(commandContext, handle, environment.CommandSpec{
			Argv: command.Argv, CWD: command.CWD, EnvironmentAllowlist: profile.EnvironmentAllowlist,
			GracePeriod: time.Second, Stdout: ioDiscard{}, Stderr: ioDiscard{},
		})
		cancel()
		verificationContext, stopVerification := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		checkoutErr := engine.environment.VerifyTree(verificationContext, handle, snapshot.InputTree)
		stopVerification()
		if runErr != nil || execution.ExitCode != 0 || checkoutErr != nil {
			cleanupContext, stopCleanup := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			cleanupErr := engine.environment.Cleanup(cleanupContext, handle)
			stopCleanup()
			return environment.Handle{}, protocol.EnvironmentSnapshot{}, fmt.Errorf("bootstrap %q failed with exit %d: %w", command.ID, execution.ExitCode, errors.Join(runErr, checkoutErr, cleanupErr))
		}
	}
	if err := engine.environment.VerifyTree(ctx, handle, snapshot.InputTree); err != nil {
		_ = engine.environment.Cleanup(context.Background(), handle)
		return environment.Handle{}, protocol.EnvironmentSnapshot{}, err
	}
	environmentSnapshot, err := engine.environment.Snapshot(ctx, handle)
	if err != nil {
		_ = engine.environment.Cleanup(context.Background(), handle)
		return environment.Handle{}, protocol.EnvironmentSnapshot{}, err
	}
	return handle, environmentSnapshot, nil
}

func (engine *Engine) prepareValidationEnvironment(ctx context.Context, snapshot workspace.Snapshot, revision domain.GoalRevision) (environment.Handle, protocol.EnvironmentSnapshot, error) {
	handle, err := engine.environment.Prepare(ctx, environment.Spec{
		ID: "environment_" + snapshot.ID, WorktreePath: snapshot.Path,
		BaseCommit: snapshot.Identity.HeadCommit, BaseTree: snapshot.InputTree,
		Identity: snapshot.Identity, ExcludePaths: snapshot.ExcludePaths,
		ConfigHash: engine.configHash, GoalRevisionHash: revision.Hash,
	})
	if err != nil {
		return environment.Handle{}, protocol.EnvironmentSnapshot{}, err
	}
	environmentSnapshot, err := engine.environment.Snapshot(ctx, handle)
	if err != nil {
		_ = engine.environment.Cleanup(context.Background(), handle)
		return environment.Handle{}, protocol.EnvironmentSnapshot{}, err
	}
	return handle, environmentSnapshot, nil
}

func (engine *Engine) runValidators(ctx context.Context, registry *validator.Registry, handle environment.Handle, workspaceSnapshot workspace.Snapshot, revision domain.GoalRevision, attemptID, subjectID, tree string, validatorIDs []string, phase evidence.SetPhase) ([]validationEvidence, error) {
	runner, err := validator.NewCommandRunner(engine.runtimeRoot, registry, engine.environment, handle)
	if err != nil {
		return nil, err
	}
	environmentSnapshot, err := engine.environment.Snapshot(ctx, handle)
	if err != nil {
		return nil, err
	}
	environmentRecord, _, err := engine.store.RecordEnvironmentSnapshot(ctx, workspaceSnapshot.ID, environmentSnapshot)
	if err != nil {
		return nil, err
	}
	ids := uniqueSorted(validatorIDs)
	result := make([]validationEvidence, 0, len(ids))
	for _, validatorID := range ids {
		definition, exists := registry.Definition(validatorID)
		if !exists {
			return nil, fmt.Errorf("validator %q is unavailable", validatorID)
		}
		runID, err := randomID("validator")
		if err != nil {
			return nil, err
		}
		receipt, runErr := runner.Run(ctx, validator.CommandRequest{
			RunID: runID, ValidatorID: validatorID, GoalRevisionHash: revision.Hash,
			ConfigHash: engine.configHash, TreeHash: tree, EnvironmentHash: environmentRecord.Hash,
			MaxOutputBytes: 16 << 20,
		})
		if receipt.ID == "" {
			return nil, runErr
		}
		recordContext, stopRecord := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		run, _, err := engine.store.RecordValidatorRun(recordContext, attemptID, workspaceSnapshot.ID, receipt)
		stopRecord()
		if err != nil {
			return nil, errors.Join(runErr, err)
		}
		if runErr != nil {
			return nil, runErr
		}
		if receipt.Result != protocol.CommandPassed {
			return nil, fmt.Errorf("validator %q returned %s", validatorID, receipt.Result)
		}
		evidenceID, err := randomID("evidence")
		if err != nil {
			return nil, err
		}
		record := evidence.Record{Evidence: protocol.Evidence{
			ProtocolVersion: protocol.EvidenceVersion, ID: evidenceID, Kind: string(phase), SubjectID: subjectID,
			Producer: "validator/" + validatorID, Authority: domain.AuthorityDeterministic,
			GoalRevisionHash: revision.Hash, ConfigHash: engine.configHash, TreeHash: tree,
			PayloadHash: run.Hash, State: domain.EvidenceCurrent, CreatedAt: receipt.FinishedAt.UTC(),
		}, DefinitionHash: definition.Hash, EnvironmentHash: environmentRecord.Hash, ReceiptHash: run.Hash}
		if err := engine.store.AppendEvidence(ctx, record); err != nil {
			return nil, err
		}
		result = append(result, validationEvidence{ID: evidenceID, Validator: validatorID, Receipt: receipt, Hash: run.Hash, Flaky: definition.Flaky})
	}
	if err := engine.environment.StopServices(ctx, handle); err != nil {
		return nil, err
	}
	if err := engine.environment.VerifyTree(ctx, handle, tree); err != nil {
		return nil, err
	}
	return result, nil
}

func (engine *Engine) runReview(ctx context.Context, implementer config.Agent, implementationSession string, revision domain.GoalRevision, plan domain.PlanRevision, work domain.WorkItem, validationWorkspace workspace.Snapshot, candidateTree string, validatorRunIDs []string) error {
	if err := engine.checkWorkspaceTree(ctx, validationWorkspace, candidateTree); err != nil {
		return err
	}
	profile, reviewer, err := engine.reviewerProfile(implementer)
	if err != nil {
		return err
	}
	if _, err := engine.adapters[profile.ID].Probe(ctx, adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: profile.ID, Timeout: 10 * time.Second}); err != nil {
		return err
	}
	reviewID, err := randomID("review")
	if err != nil {
		return err
	}
	packet, err := engine.reviews.Prepare(ctx, review.PrepareInput{
		ID: reviewID, GoalRevisionHash: revision.Hash, PlanRevisionHash: plan.GraphHash,
		WorkItemID: work.ID, ImplementationAttemptID: validationWorkspace.AttemptID,
		ImplementationProfileID: implementer.ID, ImplementationSessionID: implementationSession,
		ReviewerProfileID: profile.ID, CandidateTree: candidateTree,
		ValidationWorkspace: validationWorkspace, ValidatorRunIDs: validatorRunIDs,
		RequiredChecks: []string{"correctness", "scope", "regression", "test_gap", "security"},
	})
	if err != nil {
		return err
	}
	schema, err := protocol.Schema(protocol.SchemaReviewResult)
	if err != nil {
		return err
	}
	invocationID, err := randomID("review_invoke")
	if err != nil {
		return err
	}
	execution, err := reviewer.Review(ctx, review.Invocation{
		InvocationID: invocationID, ReviewID: reviewID, ReviewerProfileID: profile.ID,
		ImplementationProfileID: implementer.ID, ImplementationSessionID: implementationSession,
		GoalRevisionHash: revision.Hash, PlanRevisionHash: plan.GraphHash,
		BaseTree: validationWorkspace.BaseTree, CandidateTree: candidateTree,
		PacketHash: packet.Hash, WorkDir: validationWorkspace.Path, PacketPath: packet.Path,
		Prompt:       "Independently review the immutable Review Packet at " + packet.Path + ". Treat validator receipts as evidence, inspect only the read-only candidate, and return the required structured ReviewResult.",
		OutputSchema: schema, Environment: profileEnvironment(profile), PermissionMode: "dontAsk",
		Tools: []string{"Read", "Glob", "Grep"}, Timeout: profile.Timeout.Duration, MaxOutputBytes: maxAgentOutput,
	}, nil)
	verificationContext, stopVerification := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	checkoutErr := engine.checkWorkspaceTree(verificationContext, validationWorkspace, candidateTree)
	stopVerification()
	if err != nil || checkoutErr != nil {
		return errors.Join(err, checkoutErr)
	}
	if execution.SessionID == "" || execution.SessionID == implementationSession {
		return errors.New("Reviewer session is not independent from implementation")
	}
	result, err := engine.reviewStore.SaveResult(reviewID, execution.Result)
	if err != nil {
		return err
	}
	if err := engine.checkWorkspaceTree(ctx, validationWorkspace, candidateTree); err != nil {
		return err
	}
	stored, _, err := engine.store.RecordReview(ctx, packet, result, execution.SessionID)
	if err != nil {
		return err
	}
	if stored.Status != protocol.ReviewApproved {
		return fmt.Errorf("review requested changes with %d findings", len(stored.Findings))
	}
	return nil
}

func (engine *Engine) checkWorkspaceTree(ctx context.Context, snapshot workspace.Snapshot, expectedTree string) error {
	if snapshot.ExecutionModel != workspace.ExecutionCurrentDirectory || snapshot.Path != engine.projectRoot {
		return errors.New("workspace requires migration to current-directory execution")
	}
	return engine.repository.CheckSnapshot(ctx, gitrepo.SnapshotSpec{BaseTree: snapshot.BaseTree, ExcludePaths: snapshot.ExcludePaths, MaxFileBytes: maxPatchFile}, snapshot.Identity, expectedTree)
}

func (engine *Engine) waitAgent(ctx context.Context, runtimeAdapter adapter.Adapter, handle adapter.Handle) (protocol.AgentResult, error) {
	type outcome struct {
		result protocol.AgentResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := runtimeAdapter.Wait(context.WithoutCancel(ctx), handle)
		done <- outcome{result: result, err: err}
	}()
	for {
		select {
		case result := <-done:
			if errors.Is(result.err, supervisor.ErrProcessUnconfirmed) {
				result.err = errors.Join(result.err, errExecutionStillRunning)
			}
			return result.result, result.err
		case <-ctx.Done():
			cleanupContext, stopCleanup := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			cancelled := make(chan error, 1)
			go func() { cancelled <- runtimeAdapter.Cancel(cleanupContext, handle) }()
			var cancelErr error
			select {
			case cancelErr = <-cancelled:
			case <-cleanupContext.Done():
				stopCleanup()
				return protocol.AgentResult{}, errors.Join(context.Cause(ctx), errExecutionStillRunning)
			}
			select {
			case outcome := <-done:
				stopCleanup()
				if errors.Is(outcome.err, supervisor.ErrProcessUnconfirmed) || errors.Is(cancelErr, supervisor.ErrProcessUnconfirmed) {
					cancelErr = errors.Join(cancelErr, errExecutionStillRunning)
				}
				return protocol.AgentResult{}, errors.Join(context.Cause(ctx), cancelErr, outcome.err)
			case <-cleanupContext.Done():
				stopCleanup()
				return protocol.AgentResult{}, errors.Join(context.Cause(ctx), cancelErr, errExecutionStillRunning)
			}
		}
	}
}

// keepLeaseAlive owns the Lease version for the full Attempt lifecycle. Agent
// execution is only one phase: environment preparation, validation, review and
// promotion can also outlive a TTL and must retain the same live generation.
func (engine *Engine) keepLeaseAlive(ctx context.Context, lease domain.Lease, cancel context.CancelCauseFunc, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(engine.config.Orchestration.HeartbeatInterval.Duration)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			updated, err := engine.store.HeartbeatLease(ctx, lease.ID, lease.Generation, lease.Version, engine.config.Orchestration.LeaseTTL.Duration, event("LeaseHeartbeat", "daemon", map[string]any{"attempt_id": lease.AttemptID}))
			if err == nil {
				lease = updated
				continue
			}
			if ctx.Err() != nil {
				return
			}
			persisted, readErr := engine.store.Lease(context.Background(), lease.ID)
			if readErr == nil && persisted.State != domain.LeaseActive {
				return
			}
			cancel(fmt.Errorf("heartbeat lease %q: %w", lease.ID, errors.Join(err, readErr)))
			return
		}
	}
}

func (engine *Engine) advanceAttempt(ctx context.Context, lease domain.Lease, target domain.AttemptState) error {
	attempt, err := engine.store.Attempt(ctx, lease.AttemptID)
	if err != nil {
		return err
	}
	return engine.store.UpdateAttemptStateWithLease(ctx, attempt.ID, attempt.Version, lease.ID, lease.Generation, target, event("Attempt"+string(target), "kernel", map[string]any{"lease_generation": lease.Generation}))
}

func (engine *Engine) advanceWork(ctx context.Context, workID string, target domain.WorkState) error {
	work, err := engine.store.WorkItem(ctx, workID)
	if err != nil {
		return err
	}
	return engine.store.UpdateWorkState(ctx, workID, work.Version, target, event("Work"+string(target), "kernel", map[string]any{}))
}

func changesValidatorConfig(captured patch.Captured) bool {
	for _, entry := range captured.Bundle.Entries {
		if entry.Path == "xgoal.yaml" || entry.PathBefore == "xgoal.yaml" {
			return true
		}
	}
	return false
}

func agentSessionID(runtimeAdapter adapter.Adapter, handle adapter.Handle) (string, error) {
	observer, ok := runtimeAdapter.(interface {
		SessionID(adapter.Handle) (string, error)
	})
	if !ok {
		return "", errors.New("Agent adapter does not expose a persisted session ID")
	}
	return observer.SessionID(handle)
}

func profileEnvironment(profile config.Agent) map[string]string {
	result := make(map[string]string)
	for _, name := range profile.EnvironmentAllowlist {
		if value, exists := os.LookupEnv(name); exists {
			result[name] = value
		}
	}
	return result
}

func sandboxFor(role domain.Role) string {
	if role == domain.RoleImplementer {
		return "workspace-write"
	}
	return "read-only"
}

func toolsFor(role domain.Role) []string {
	if role == domain.RoleImplementer {
		return []string{"Read", "Glob", "Grep", "Edit", "Write"}
	}
	return []string{"Read", "Glob", "Grep"}
}

func classifyAgentError(err error) reconcile.FailureClass {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return reconcile.AgentTimeout
	case errors.Is(err, context.Canceled):
		return reconcile.AgentInterrupted
	case errors.Is(err, adapter.ErrInvalidOutput):
		return reconcile.AgentProtocolInvalid
	default:
		return reconcile.AgentUnavailable
	}
}

func classifyValidatorError(err error) reconcile.FailureClass {
	if strings.Contains(strings.ToLower(err.Error()), "unavailable") || strings.Contains(strings.ToLower(err.Error()), "unsupported") {
		return reconcile.ValidatorUnavailable
	}
	return reconcile.ValidatorFailed
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

type ioDiscard struct{}

func (ioDiscard) Write(value []byte) (int, error) { return len(value), nil }
