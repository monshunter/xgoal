package orchestrator

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/reconcile"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func (engine *Engine) failUnclaimed(ctx context.Context, goal domain.Goal, work domain.WorkItem, revision domain.GoalRevision, class reconcile.FailureClass, cause error, strategy string) error {
	return engine.recordAndWait(ctx, goal, work, revision, domain.Attempt{}, class, cause, strategy, "")
}

func (engine *Engine) failAttempt(ctx context.Context, goal domain.Goal, work domain.WorkItem, revision domain.GoalRevision, lease domain.Lease, class reconcile.FailureClass, cause error, strategy, patchHash string) error {
	currentAttempt, attemptErr := engine.store.Attempt(context.Background(), lease.AttemptID)
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
			attemptErr = engine.store.UpdateAttemptStateWithLease(context.Background(), currentAttempt.ID, currentAttempt.Version, lease.ID, lease.Generation, target, event("AttemptFailed", "kernel", map[string]any{"class": class, "error": cause.Error()}))
		}
	}
	currentWork, workErr := engine.store.WorkItem(context.Background(), work.ID)
	if workErr == nil && currentWork.State.CanTransition(domain.WorkReconciling) {
		workErr = engine.store.UpdateWorkState(context.Background(), currentWork.ID, currentWork.Version, domain.WorkReconciling, event("WorkReconciling", "kernel", map[string]any{"class": class}))
	}
	currentLease, leaseErr := engine.store.Lease(context.Background(), lease.ID)
	if leaseErr == nil && currentLease.State == domain.LeaseActive {
		_, leaseErr = engine.store.ReleaseLease(context.Background(), currentLease.ID, currentLease.Generation, currentLease.Version, event("LeaseReleased", "kernel", map[string]any{"failure_class": class}))
		if errors.Is(leaseErr, basestore.ErrExpired) {
			_, leaseErr = engine.store.ResolveExpiredLease(context.Background(), currentLease.ID, currentLease.Generation, currentLease.Version, true, event("LeaseExpired", "kernel", map[string]any{"failure_class": class}))
		}
	}
	if joined := errors.Join(attemptErr, workErr, leaseErr); joined != nil {
		return joined
	}
	currentAttempt, _ = engine.store.Attempt(context.Background(), lease.AttemptID)
	currentWork, workErr = engine.store.WorkItem(context.Background(), work.ID)
	if workErr == nil && currentWork.State == domain.WorkCancelled {
		return nil
	}
	if workErr != nil {
		return workErr
	}
	return engine.recordAndWait(ctx, goal, work, revision, currentAttempt, class, cause, strategy, patchHash)
}

func (engine *Engine) recordAndWait(ctx context.Context, goal domain.Goal, work domain.WorkItem, revision domain.GoalRevision, attempt domain.Attempt, class reconcile.FailureClass, cause error, strategy, patchHash string) error {
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
	if err != nil || latestGoal.State == domain.GoalCancelled || latestGoal.State == domain.GoalWaiting {
		return err
	}
	currentWork, err := engine.store.WorkItem(ctx, work.ID)
	if err != nil {
		return err
	}
	retry := decision.Action == reconcile.RetryNewAttempt || decision.Action == reconcile.FixWorkItem || decision.Action == reconcile.SwitchStrategy
	if retry && recorded.RepeatCount <= int64(engine.config.Orchestration.NoProgressLimit) && currentWork.State == domain.WorkReconciling {
		return engine.store.UpdateWorkState(ctx, currentWork.ID, currentWork.Version, domain.WorkReady, event("WorkRetryReady", "kernel", map[string]any{"failure_id": failureID, "action": decision.Action}))
	}
	return engine.openFailureGate(ctx, latestGoal, currentWork, attempt, class, cause, decision)
}

func (engine *Engine) openFailureGate(ctx context.Context, goal domain.Goal, work domain.WorkItem, attempt domain.Attempt, class reconcile.FailureClass, cause error, decision reconcile.Decision) error {
	gateID, err := randomID("gate")
	if err != nil {
		return err
	}
	action := domain.ActionExecCommand
	scopeValues := []string{"goal:" + goal.ID}
	switch {
	case errors.Is(cause, errProjectNetworkGate):
		action, scopeValues = domain.ActionAccessProjectNetwork, []string{"project-network"}
	case errors.Is(cause, errValidatorChange):
		action, scopeValues = domain.ActionModifyValidator, []string{"xgoal.yaml"}
	case class == reconcile.ScopeViolation:
		action, scopeValues = domain.ActionExpandScope, work.WriteScope
	}
	_, err = engine.store.CreateGate(ctx, sqlite.GateDraft{
		ID: gateID, GoalID: goal.ID, WorkItemID: work.ID, AttemptID: attempt.ID,
		ReasonCode: strings.ToLower(string(class)),
		Facts:      map[string]any{"failure_class": class, "error": cause.Error(), "reconcile_action": decision.Action},
		Unknowns:   []string{"Whether a bounded retry, repaired plan, or explicit authorization is appropriate"},
		Options: []map[string]string{
			{"id": "approve", "effect": "authorize exactly the displayed action and scope once"},
			{"id": "deny", "effect": "keep the Goal waiting without side effects"},
			{"id": "replan", "effect": "submit a replacement Plan Revision"},
		},
		Recommendation: "Inspect the failure evidence and replan unless the displayed one-use scope is intentional",
		Action:         action, Scope: scopeValues, ExpiresAt: time.Now().UTC().Add(24 * time.Hour), MaxUses: 1,
		Revocable: true, Required: true,
	}, event("GateOpened", "kernel", map[string]any{"failure_class": class, "recovery": "approve then resume, or replan"}))
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
