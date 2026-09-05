package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/harness"
	"github.com/monshunter/xgoal/internal/patch"
	"github.com/monshunter/xgoal/internal/promotion"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/reconcile"
	"github.com/monshunter/xgoal/internal/scope"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/supervisor"
	"github.com/monshunter/xgoal/internal/validator"
)

type agentOutcomeError struct {
	Result       protocol.AgentResult
	InvocationID string
}

func (outcome *agentOutcomeError) Error() string {
	return fmt.Sprintf("agent returned %s: %s; blockers: %s; recommended next action: %s", outcome.Result.Status, outcome.Result.Summary, strings.Join(outcome.Result.Blockers, "; "), outcome.Result.RecommendedNextAction)
}

func (engine *Engine) failUnclaimed(ctx context.Context, goal domain.Goal, work domain.WorkItem, revision domain.GoalRevision, class reconcile.FailureClass, cause error, strategy string) error {
	return engine.recordAndWait(ctx, goal, work, revision, domain.Attempt{}, class, cause, strategy, "", false)
}

func (engine *Engine) failAttempt(ctx context.Context, goal domain.Goal, work domain.WorkItem, revision domain.GoalRevision, lease domain.Lease, class reconcile.FailureClass, cause error, strategy, patchHash string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if cause == nil {
		cause = errors.New("unspecified orchestration failure")
	}
	if errors.Is(cause, supervisor.ErrProcessUnconfirmed) {
		cause = errors.Join(cause, errExecutionStillRunning)
	}
	currentAttempt, attemptErr := engine.store.Attempt(ctx, lease.AttemptID)
	if attemptErr == nil && errors.Is(cause, errExecutionStillRunning) {
		// A cancellation request is not proof that writers stopped. Keep the
		// active generation fenced until worker recovery proves process exit.
		return engine.recordAndWait(ctx, goal, work, revision, currentAttempt, class, cause, strategy, patchHash, false)
	}
	if attemptErr == nil {
		target := domain.AttemptFailed
		switch class {
		case reconcile.ScopeViolation:
			target = domain.AttemptQuarantined
		case reconcile.AgentTimeout:
			target = domain.AttemptTimedOut
		case reconcile.AgentInterrupted:
			target = domain.AttemptInterrupted
		case reconcile.AgentProtocolInvalid:
			target = domain.AttemptInvalidOutput
		}
		if currentAttempt.State.CanTransition(target) {
			attemptErr = engine.store.UpdateAttemptStateWithLease(ctx, currentAttempt.ID, currentAttempt.Version, lease.ID, lease.Generation, target, event("AttemptFailed", "kernel", map[string]any{"class": class, "error": cause.Error()}))
		}
	}
	if errors.Is(attemptErr, basestore.ErrExpired) {
		currentLease, err := engine.store.Lease(ctx, lease.ID)
		if err != nil {
			return err
		}
		if _, err := engine.store.ResolveExpiredLease(ctx, currentLease.ID, currentLease.Generation, currentLease.Version, true, event("LeaseExpired", "kernel", map[string]any{"failure_class": class})); err != nil {
			return err
		}
		attemptErr = nil
	}
	currentWork, workErr := engine.store.WorkItem(ctx, work.ID)
	if workErr == nil && currentWork.State.CanTransition(domain.WorkReconciling) {
		workErr = engine.store.UpdateWorkState(ctx, currentWork.ID, currentWork.Version, domain.WorkReconciling, event("WorkReconciling", "kernel", map[string]any{"class": class}))
	}
	currentLease, leaseErr := engine.store.Lease(ctx, lease.ID)
	if leaseErr == nil && currentLease.State == domain.LeaseActive {
		_, leaseErr = engine.store.ReleaseLease(ctx, currentLease.ID, currentLease.Generation, currentLease.Version, event("LeaseReleased", "kernel", map[string]any{"failure_class": class}))
		if errors.Is(leaseErr, basestore.ErrExpired) {
			_, leaseErr = engine.store.ResolveExpiredLease(ctx, currentLease.ID, currentLease.Generation, currentLease.Version, true, event("LeaseExpired", "kernel", map[string]any{"failure_class": class}))
		}
	}
	if joined := errors.Join(attemptErr, workErr, leaseErr); joined != nil {
		return joined
	}
	currentAttempt, _ = engine.store.Attempt(ctx, lease.AttemptID)
	currentWork, workErr = engine.store.WorkItem(ctx, work.ID)
	if workErr == nil && currentWork.State == domain.WorkCancelled {
		return nil
	}
	if workErr != nil {
		return workErr
	}
	safeRetry, observeErr := engine.observeFailureScene(ctx, goal, work, currentAttempt, class, cause)
	if observeErr != nil {
		cause = errors.Join(cause, observeErr)
	}
	return engine.recordAndWait(ctx, goal, work, revision, currentAttempt, class, cause, strategy, patchHash, safeRetry)
}

// observeFailureScene grants no execution permission. It persists only a stopped,
// attributable scene whose complete delta satisfies this Work's write policy.
func (engine *Engine) observeFailureScene(ctx context.Context, goal domain.Goal, work domain.WorkItem, attempt domain.Attempt, class reconcile.FailureClass, cause error) (bool, error) {
	if errors.Is(cause, errExecutionStillRunning) || errors.Is(cause, gitrepo.ErrCheckoutChanged) || errors.Is(cause, environment.ErrCheckoutDrift) ||
		errors.Is(cause, errValidatorChange) || errors.Is(cause, errProjectNetworkGate) || errors.Is(cause, harness.ErrRequired) || class == reconcile.ScopeViolation {
		return false, nil
	}
	checkout, err := engine.store.Checkout(ctx)
	if errors.Is(err, basestore.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if checkout.GoalID != goal.ID || checkout.WorkID != work.ID {
		return false, nil
	}
	spec := gitrepo.SnapshotSpec{BaseTree: checkout.AcceptedTree, ExcludePaths: []string{engine.runtimeRoot}, MaxFileBytes: maxPatchFile}
	snapshot, err := engine.repository.SnapshotTree(ctx, spec)
	if err != nil {
		return false, err
	}
	if snapshot.Identity != checkout.Identity {
		return false, gitrepo.ErrCheckoutChanged
	}
	if snapshot.Tree != checkout.AcceptedTree {
		captured, err := patch.Capture(ctx, engine.repository, patch.CaptureSpec{
			AttemptID: attempt.ID, ExecutionPath: engine.projectRoot, Identity: checkout.Identity, ExcludePaths: spec.ExcludePaths,
			BaseCommit: checkout.AcceptedCommit, BaseTree: checkout.AcceptedTree, MaxFileBytes: maxPatchFile,
		})
		if err != nil {
			return false, err
		}
		registry, err := validator.LoadRegistry(ctx, engine.repository, checkout.AcceptedCommit, "xgoal.yaml")
		if err != nil {
			return false, err
		}
		if changesValidatorConfig(captured, registry.ProtectedPaths()...) {
			return false, errValidatorChange
		}
		policy, err := scope.NewPolicy(work.WriteScope, engine.config.ScopePolicy.Deny)
		if err != nil {
			return false, err
		}
		result, err := patch.Replay(ctx, engine.repository, patch.ReplaySpec{
			ExecutionPath: engine.projectRoot, Identity: checkout.Identity, ExcludePaths: spec.ExcludePaths,
			IntegrationCommit: checkout.AcceptedCommit, IntegrationTree: checkout.AcceptedTree,
			Captured: captured, Policy: policy, MaxFileBytes: maxPatchFile,
		})
		if err != nil {
			return false, err
		}
		if result.CandidateTree != snapshot.Tree {
			return false, gitrepo.ErrCheckoutChanged
		}
	}
	if err := engine.repository.CheckSnapshot(ctx, spec, checkout.Identity, snapshot.Tree); err != nil {
		return false, err
	}
	if err := engine.store.ObserveCheckoutFailure(ctx, goal.ID, work.ID, checkout.Identity, snapshot.Tree); err != nil {
		return false, err
	}
	return true, nil
}

func (engine *Engine) recordAndWait(ctx context.Context, goal domain.Goal, work domain.WorkItem, revision domain.GoalRevision, attempt domain.Attempt, class reconcile.FailureClass, cause error, strategy, patchHash string, safeRetry bool) error {
	if cause == nil {
		cause = errors.New("unspecified orchestration failure")
	}
	if strategy == "" {
		strategy = "deterministic-orchestrator"
	}
	failureID, err := randomID("failure")
	if err != nil {
		return err
	}
	plan, _ := engine.store.PlanRevision(ctx, work.PlanRevisionID)
	current := reconcile.Snapshot{AcceptedPatchHash: patchHash, PlanRevision: plan.Revision}
	recorded, err := engine.store.RecordFailure(ctx, sqlite.FailureDraft{
		ID: failureID, GoalID: goal.ID, WorkItemID: work.ID, AttemptID: attempt.ID,
		Failure: reconcile.Failure{
			Class: class, PrimaryError: cause.Error(), ValidatorDefinitionHash: "not-applicable",
			BaseTree: firstNonEmpty(attempt.BaseTree, "unknown"), ResultTree: attempt.ResultTree,
			GoalRevisionHash: revision.Hash, RelevantConfigHash: engine.configHash,
		},
		Strategy: strategy, Previous: current, Current: current,
	}, event("FailureRecorded", "kernel", map[string]any{"class": class}))
	if err != nil {
		return err
	}
	decision, err := reconcile.Decide(reconcile.Input{
		Failure: recorded.Failure, Current: current,
		SameFingerprintStrategy: recorded.RepeatCount - 1,
		SideEffectsObserved:     patchHash != "",
	})
	if err != nil {
		return err
	}
	reconcileID, err := randomID("reconcile")
	if err != nil {
		return err
	}
	if _, err := engine.store.RecordReconcileDecision(ctx, sqlite.ReconcileRecord{
		ID: reconcileID, FailureID: failureID, Decision: decision, PlanRevision: plan.Revision,
	}, event("ReconcileDecided", "kernel", map[string]any{"action": decision.Action, "reason": decision.Reason})); err != nil {
		return err
	}
	latestGoal, err := engine.store.Goal(ctx, goal.ID)
	if err != nil || latestGoal.State == domain.GoalCancelled {
		return err
	}
	currentWork, err := engine.store.WorkItem(ctx, work.ID)
	if err != nil {
		return err
	}
	retry := decision.Action == reconcile.RetryNewAttempt || decision.Action == reconcile.FixWorkItem || decision.Action == reconcile.SwitchStrategy
	checkout, checkoutErr := engine.store.Checkout(ctx)
	if checkoutErr != nil && !errors.Is(checkoutErr, basestore.ErrNotFound) {
		return checkoutErr
	}
	checkoutOwned := checkoutErr == nil && checkout.GoalID == goal.ID && checkout.WorkID == work.ID
	if retry && safeRetry && checkoutOwned && engine.config.Orchestration.AutoRetryLimit > 0 && currentWork.State == domain.WorkReconciling {
		if err := engine.repository.CheckSnapshot(ctx, gitrepo.SnapshotSpec{BaseTree: checkout.AcceptedTree, ExcludePaths: []string{engine.runtimeRoot}, MaxFileBytes: maxPatchFile}, checkout.Identity, checkout.ObservedTree); err != nil {
			cause = errors.Join(cause, err)
			safeRetry = false
		} else {
			_, retryErr := engine.store.AutomaticRetryCheckoutWork(ctx, currentWork.ID, currentWork.Version, checkout.Identity, checkout.ObservedTree, sqlite.AutomaticRetry{FailureID: failureID, ConfigHash: engine.configHash, Limit: engine.config.Orchestration.AutoRetryLimit}, event("WorkAutomaticallyRetried", "kernel", map[string]any{"failure_id": failureID, "action": decision.Action, "limit": engine.config.Orchestration.AutoRetryLimit}))
			if retryErr == nil {
				return nil
			}
			if !errors.Is(retryErr, sqlite.ErrAutomaticRetryDenied) && !errors.Is(retryErr, basestore.ErrAuthorizationDenied) && !errors.Is(retryErr, sqlite.ErrCheckoutConflict) && !errors.Is(retryErr, sqlite.ErrCheckoutBusy) {
				return retryErr
			}
			cause = errors.Join(cause, retryErr)
			if errors.Is(retryErr, sqlite.ErrCheckoutBusy) || errors.Is(retryErr, sqlite.ErrCheckoutConflict) {
				safeRetry = false
			}
		}
	}
	return engine.openFailureGate(ctx, latestGoal, currentWork, attempt, class, cause, decision, safeRetry)
}

func (engine *Engine) openFailureGate(ctx context.Context, goal domain.Goal, work domain.WorkItem, attempt domain.Attempt, class reconcile.FailureClass, cause error, decision reconcile.Decision, safeRetry bool) error {
	gateID, err := randomID("gate")
	if err != nil {
		return err
	}
	action := domain.ActionExecCommand
	scopeValues := []string{"goal:" + goal.ID}
	switch {
	case errors.Is(cause, errProjectNetworkGate):
		action, scopeValues = domain.ActionAccessProjectNetwork, []string{"project-network"}
	case errors.Is(cause, errValidatorChange), errors.Is(cause, sqlite.ErrTrustBindingMigrationRequired):
		action, scopeValues = domain.ActionModifyValidator, []string{"xgoal.yaml"}
	case class == reconcile.ScopeViolation:
		action, scopeValues = domain.ActionExpandScope, work.WriteScope
	}
	reason := strings.ToLower(string(class))
	unknowns := []string{"Whether a bounded retry, repaired plan, or explicit authorization is appropriate"}
	options := []map[string]string{
		{"id": "approve", "effect": "authorize exactly the displayed action and scope once"},
		{"id": "deny", "effect": "keep the Goal waiting without side effects"},
		{"id": "replan", "effect": "submit a replacement Plan Revision"},
	}
	recommendation := "Inspect the failure evidence and replan unless the displayed one-use scope is intentional"
	facts := map[string]any{"failure_class": class, "error": cause.Error(), "reconcile_action": decision.Action}
	var outcome *agentOutcomeError
	if errors.As(cause, &outcome) {
		facts["agent_result"] = outcome.Result
		facts["invocation_id"] = outcome.InvocationID
	}
	checkout, checkoutErr := engine.store.Checkout(ctx)
	if checkoutErr != nil && !errors.Is(checkoutErr, basestore.ErrNotFound) {
		return checkoutErr
	}
	if checkoutErr == nil && checkout.GoalID == goal.ID && checkout.WorkID == work.ID {
		facts["accepted_tree"] = checkout.AcceptedTree
		facts["recoverable_observed_tree"] = checkout.ObservedTree
		unknowns = []string{"Whether current files can be restored to the recorded recoverable scene and original Git identity without losing operator changes"}
		options = []map[string]string{
			{"id": "preserve", "effect": "keep current files and inspect the failure, accepted tree, and recorded recoverable tree"},
			{"id": "restore", "effect": "restore the recoverable_observed_tree and original HEAD/index, then resolve this Gate and explicitly retry the same Work"},
			{"id": "cancel", "effect": "cancel this Goal, review and commit intended changes, then create a new Goal from a clean checkout"},
		}
		recommendation = "Preserve the scene; restore the recorded recoverable tree and original Git identity, or cancel and review/commit changes before a new Goal"
	}
	if errors.Is(cause, errExecutionStillRunning) {
		options = []map[string]string{{"id": "inspect", "effect": "inspect the active worker and preserve source files"}, {"id": "recover", "effect": "have worker recovery confirm that every writer stopped before restoring files or retrying"}}
		recommendation = "Confirm process exit through worker recovery before changing or retrying this checkout"
	}
	if errors.Is(cause, errValidatorChange) || errors.Is(cause, sqlite.ErrTrustBindingMigrationRequired) {
		reason = "trusted_validator_change"
		facts["trust_update_requires_new_goal"] = true
		unknowns = []string{"Whether the operator intends to change the trusted validation baseline"}
		options = []map[string]string{
			{"id": "preserve", "effect": "keep files and inspect the frozen validator entrypoints and dependencies"},
			{"id": "new-goal", "effect": "cancel this Goal, review and commit the intended trust baseline, then create a new Goal"},
		}
		recommendation = "Review and commit a new trust baseline, then create a new Goal; approval or ordinary replan cannot replace this Goal's frozen validator authority"
		if errors.Is(cause, sqlite.ErrTrustBindingMigrationRequired) {
			reason = "trust_binding_migration_required"
			recommendation = sqlite.ErrTrustBindingMigrationRequired.Error()
		}
	}
	if safeRetry && class != reconcile.AgentBlocked {
		reason = "checkout_retry_required"
		unknowns = []string{"Whether the operator wants this Work to continue from its preserved, scope-checked files"}
		options = []map[string]string{
			{"id": "retry", "effect": "xgoal work retry " + work.ID + " --reason <reason>; only the unchanged observed scene may continue"},
			{"id": "preserve", "effect": "keep the Goal waiting and retain all current files"},
			{"id": "cancel", "effect": "cancel the Goal while preserving the failure scene"},
		}
		recommendation = "Inspect current files and explicitly retry this Work if the preserved changes are intentional"
	}
	if class == reconcile.AgentBlocked && outcome != nil && safeRetry {
		unknowns = append([]string(nil), outcome.Result.Blockers...)
		if len(unknowns) == 0 {
			unknowns = []string{outcome.Result.Summary}
		}
		options = []map[string]string{
			{"id": "answer", "effect": "decide this Gate with a reason, then retry the same Work only if its preserved scene remains unchanged"},
			{"id": "preserve", "effect": "keep the Goal waiting for the required decision or external condition"},
			{"id": "cancel", "effect": "cancel the Goal and preserve all files and evidence"},
		}
		recommendation = firstNonEmpty(outcome.Result.RecommendedNextAction, "Inspect blockers and answer the Gate before continuing")
	}
	if errors.Is(cause, harness.ErrRequired) {
		reason = "project_harness_required"
		recommendation = "Prepare the required project-local Harness for the selected Provider, review and commit the resulting baseline, then create a new Goal; xgoal doctor lists missing inputs. Approval alone cannot install project knowledge."
		options = []map[string]string{{"id": "prepare", "effect": recommendation}, {"id": "preserve", "effect": "keep this Goal and its files for diagnosis"}}
	}
	_, err = engine.store.CreateGate(ctx, sqlite.GateDraft{
		ID: gateID, GoalID: goal.ID, WorkItemID: work.ID, AttemptID: attempt.ID,
		ReasonCode: reason,
		Facts:      facts,
		Unknowns:   unknowns, Options: options, Recommendation: recommendation,
		Action: action, Scope: scopeValues, ExpiresAt: time.Now().UTC().Add(24 * time.Hour), MaxUses: 1,
		Revocable: true, Required: true,
	}, event("GateOpened", "kernel", map[string]any{"failure_class": class, "recovery": recommendation}))
	if err != nil {
		return err
	}
	if work.State.CanTransition(domain.WorkWaiting) {
		if err := engine.store.UpdateWorkState(ctx, work.ID, work.Version, domain.WorkWaiting, event("WorkWaiting", "kernel", map[string]any{"gate_id": gateID})); err != nil {
			return err
		}
	}
	latest, err := engine.store.Goal(ctx, goal.ID)
	if err != nil {
		return err
	}
	if latest.State == domain.GoalRunning || latest.State == domain.GoalVerifying {
		return engine.store.UpdateGoalState(ctx, latest.ID, latest.Version, domain.GoalWaiting, event("GoalWaiting", "kernel", map[string]any{"gate_id": gateID}))
	}
	return nil
}

func (engine *Engine) openPromotionRecoveryGate(ctx context.Context, goal domain.Goal, record promotion.Record, cause error) error {
	gates, err := engine.store.Gates(ctx, goal.ID, true)
	if err != nil {
		return err
	}
	exists := false
	for _, gate := range gates {
		if gate.ReasonCode == "promotion_recovery_required" && gate.WorkItemID == record.WorkItemID && gate.AttemptID == record.AttemptID {
			exists = true
			break
		}
	}
	if !exists {
		id, err := randomID("gate_promotion")
		if err != nil {
			return err
		}
		_, err = engine.store.CreateGate(ctx, sqlite.GateDraft{
			ID: id, GoalID: goal.ID, WorkItemID: record.WorkItemID, AttemptID: record.AttemptID,
			ReasonCode:     "promotion_recovery_required",
			Facts:          map[string]any{"error": cause.Error(), "promotion_id": record.ID, "candidate_tree": record.CandidateTree, "integration_ref": record.IntegrationRef},
			Unknowns:       []string{"Whether current files and Git identity can be restored to the recorded candidate without discarding operator changes"},
			Options:        []map[string]string{{"id": "inspect", "effect": "preserve files and inspect the pending Promotion"}, {"id": "recover", "effect": "restore the recorded candidate and original HEAD/index, then resume or restart; recovery verifies before observing the effect"}},
			Recommendation: "Preserve the scene and recover the pending Promotion before retrying any Work",
			Action:         domain.ActionExecCommand, Scope: []string{"promotion:" + record.ID}, ExpiresAt: time.Now().UTC().Add(24 * time.Hour), MaxUses: 1, Revocable: true, Required: true,
		}, event("PromotionRecoveryGateOpened", "kernel", map[string]any{"promotion_id": record.ID, "error": cause.Error()}))
		if err != nil {
			return err
		}
	}
	latest, err := engine.store.Goal(ctx, goal.ID)
	if err != nil {
		return err
	}
	if latest.State == domain.GoalRunning || latest.State == domain.GoalVerifying {
		return engine.store.UpdateGoalState(ctx, latest.ID, latest.Version, domain.GoalWaiting, event("GoalWaiting", "kernel", map[string]any{"reason": "promotion_recovery_required", "promotion_id": record.ID}))
	}
	return nil
}

func (engine *Engine) stopInvariant(ctx context.Context, goalID string, cause error) error {
	goal, err := engine.store.Goal(ctx, goalID)
	if err != nil || (goal.State != domain.GoalRunning && goal.State != domain.GoalVerifying) {
		return err
	}
	gateID, err := randomID("gate_invariant")
	if err != nil {
		return err
	}
	if _, err := engine.store.CreateGate(ctx, sqlite.GateDraft{
		ID: gateID, GoalID: goalID, ReasonCode: "internal_invariant_violation",
		Facts: map[string]any{"error": cause.Error()}, Unknowns: []string{"The deterministic state owner that requires repair"},
		Options:        []map[string]string{{"id": "diagnose", "effect": "inspect persisted state without retry"}, {"id": "cancel", "effect": "cancel the Goal while preserving history"}},
		Recommendation: "Diagnose the invariant before resuming",
		Action:         domain.ActionExecCommand, Scope: []string{"goal:" + goalID},
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour), MaxUses: 1, Revocable: true, Required: true,
	}, event("InvariantGateOpened", "kernel", map[string]any{"error": cause.Error()})); err != nil {
		return err
	}
	return engine.store.UpdateGoalState(ctx, goalID, goal.Version, domain.GoalWaiting, event("GoalWaiting", "kernel", map[string]any{"gate_id": gateID}))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "unknown"
}
