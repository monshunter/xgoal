package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/completion"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestPersistentSchedulerRespectsDependenciesGatesAndSingleSlot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	source := clock.NewFake(time.Date(2026, 9, 2, 18, 0, 0, 0, time.UTC))
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), source)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	goal, first, second := seedTwoWorkPlan(t, store)

	ready, err := store.RefreshReadyWork(ctx, goal.ID, EventInput{Type: "WorkReady", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("RefreshReadyWork() error = %v", err)
	}
	if len(ready) != 1 || ready[0] != first.ID {
		t.Fatalf("initial ready work = %v, want [%s]", ready, first.ID)
	}
	candidate, err := store.NextReadyWork(ctx, goal.ID)
	if err != nil {
		t.Fatalf("NextReadyWork() error = %v", err)
	}
	if candidate.ID != first.ID {
		t.Fatalf("candidate = %s, want %s", candidate.ID, first.ID)
	}
	lease := claimAndCompleteWork(t, store, first, "attempt_first", "lease_first")
	ready, err = store.RefreshReadyWork(ctx, goal.ID, EventInput{Type: "WorkReady", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("RefreshReadyWork(after dependency) error = %v", err)
	}
	if len(ready) != 1 || ready[0] != second.ID {
		t.Fatalf("dependency-cleared ready work = %v, want [%s]", ready, second.ID)
	}
	if _, err := store.NextReadyWork(ctx, goal.ID); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("NextReadyWork() with active lease error = %v, want ErrNotFound", err)
	}
	if _, err := store.ReleaseLease(ctx, lease.ID, lease.Generation, lease.Version, EventInput{Type: "LeaseReleased", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("ReleaseLease() error = %v", err)
	}

	gate, err := store.CreateGate(ctx, GateDraft{
		ID: "gate_second", GoalID: goal.ID, WorkItemID: second.ID,
		ReasonCode: "NETWORK_REQUIRED", Facts: []any{}, Unknowns: []any{},
		Options: []any{map[string]any{"id": "allow", "impact": "once"}}, Recommendation: "allow",
		Action: domain.ActionAccessProjectNetwork, Scope: []string{"proxy.golang.org"},
		ExpiresAt: source.Now().Add(time.Hour), MaxUses: 1, Revocable: true, Required: true,
	}, EventInput{Type: "GateOpened", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("CreateGate() error = %v", err)
	}
	if _, err := store.ClaimWork(ctx, second.ID, 2, LeaseDraft{ID: "lease_gate_bypass", Holder: "daemon/bypass", TTL: time.Minute}, domain.Attempt{
		ID: "attempt_gate_bypass", WorkItemID: second.ID, AgentProfileID: "fake", State: domain.AttemptCreated,
		BaseTree: "base", PacketHash: "packet", Version: 1,
	}, EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrConflict) {
		t.Fatalf("ClaimWork() with required Gate error = %v, want ErrConflict", err)
	}
	if _, err := store.NextReadyWork(ctx, goal.ID); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("NextReadyWork() with required Gate error = %v, want ErrNotFound", err)
	}
	if _, err := store.DecideGate(ctx, gate.ID, 1, domain.GateAllow, "user_1", "allow once", EventInput{Type: "GateDecided", ActorType: "human", Payload: map[string]any{}}); err != nil {
		t.Fatalf("DecideGate() error = %v", err)
	}
	candidate, err = store.NextReadyWork(ctx, goal.ID)
	if err != nil {
		t.Fatalf("NextReadyWork(unblocked) error = %v", err)
	}
	if candidate.ID != second.ID {
		t.Fatalf("unblocked candidate = %s, want %s", candidate.ID, second.ID)
	}
}

func TestPersistentCompletionRechecksFactsAndCommitsFinalState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	store, err := Open(ctx, projectDir, clock.NewFake(time.Date(2026, 9, 2, 19, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	goal, work := seedReadyWork(t, store, "work_1")
	lease := claimAndCompleteWork(t, store, work, "attempt_1", "lease_1")
	if _, err := store.ReleaseLease(ctx, lease.ID, lease.Generation, lease.Version, EventInput{Type: "LeaseReleased", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("ReleaseLease() error = %v", err)
	}
	if err := store.UpdateGoalState(ctx, goal.ID, 3, domain.GoalVerifying, EventInput{Type: "GoalVerifying", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("move goal to verifying: %v", err)
	}
	if err := store.UpdateGoalState(ctx, goal.ID, 4, domain.GoalCompleted, EventInput{Type: "BypassCompletion", ActorType: "agent", Payload: map[string]any{}}); err == nil {
		t.Fatal("UpdateGoalState() bypassed the completion predicate")
	}

	facts := CompletionFacts{
		IntegrationTree: "tree-final", ExpectedTree: "tree-final",
		Criteria:          []completion.CriterionStatus{{ID: "AC-1", Satisfied: false, Current: true, TreeHash: "tree-final"}},
		ScopePolicyPassed: true, FinalValidationSetCurrent: true,
		FinalEvidenceSetID: "evidence-set-1", FinalReportHash: "report-hash-1",
		HumanAcceptanceSatisfied: true,
	}
	if _, err := store.SetCompletionFacts(ctx, goal.ID, facts, EventInput{Type: "CompletionFactsRecorded", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("SetCompletionFacts() error = %v", err)
	}
	result, err := store.CompleteGoal(ctx, goal.ID, 4, EventInput{Type: "GoalCompleted", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("CompleteGoal(unsatisfied) error = %v", err)
	}
	if result.Complete || len(result.Reasons) == 0 {
		t.Fatalf("unsatisfied completion result = %+v", result)
	}
	unchanged, err := store.Goal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("Goal() error = %v", err)
	}
	if unchanged.State != domain.GoalVerifying || unchanged.Version != 4 {
		t.Fatalf("goal changed after rejected completion = %+v", unchanged)
	}

	facts.Criteria[0].Satisfied = true
	if _, err := store.SetCompletionFacts(ctx, goal.ID, facts, EventInput{Type: "CompletionFactsRecorded", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("SetCompletionFacts(satisfied) error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER reject_goal_completion_event
BEFORE INSERT ON events
WHEN NEW.event_type = 'GoalCompleted'
BEGIN
    SELECT RAISE(ABORT, 'rejected for completion atomicity test');
END;`); err != nil {
		t.Fatalf("create completion rejection trigger: %v", err)
	}
	if _, err := store.CompleteGoal(ctx, goal.ID, 4, EventInput{Type: "GoalCompleted", ActorType: "kernel", Payload: map[string]any{}}); err == nil {
		t.Fatal("CompleteGoal() with rejected event unexpectedly succeeded")
	}
	afterRejectedEvent, err := store.Goal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("Goal() after rejected completion event error = %v", err)
	}
	if afterRejectedEvent.State != domain.GoalVerifying || afterRejectedEvent.Version != 4 || afterRejectedEvent.FinalTree != "" {
		t.Fatalf("goal after rejected completion event = %+v", afterRejectedEvent)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_goal_completion_event`); err != nil {
		t.Fatalf("drop completion rejection trigger: %v", err)
	}
	result, err = store.CompleteGoal(ctx, goal.ID, 4, EventInput{Type: "GoalCompleted", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("CompleteGoal() error = %v", err)
	}
	if !result.Complete || len(result.Reasons) != 0 {
		t.Fatalf("completion result = %+v", result)
	}
	completed, err := store.Goal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("Goal(completed) error = %v", err)
	}
	if completed.State != domain.GoalCompleted || completed.Version != 5 || completed.FinalTree != "tree-final" || completed.FinalEvidenceSetID != "evidence-set-1" || completed.FinalReportHash != "report-hash-1" {
		t.Fatalf("completed goal = %+v", completed)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, projectDir, clock.Real{})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	persisted, err := reopened.Goal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("Goal() after reopen error = %v", err)
	}
	if persisted.State != domain.GoalCompleted || persisted.FinalTree != completed.FinalTree || persisted.FinalReportHash != completed.FinalReportHash {
		t.Fatalf("persisted completed goal = %+v", persisted)
	}
}

func seedTwoWorkPlan(t *testing.T, store *Store) (domain.Goal, domain.WorkItem, domain.WorkItem) {
	t.Helper()
	ctx := context.Background()
	goal := domain.Goal{ID: "goal_scheduler", State: domain.GoalDraft, Version: 1}
	if err := store.CreateGoal(ctx, goal, EventInput{Type: "GoalDrafted", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("CreateGoal() error = %v", err)
	}
	revision, err := store.FreezeGoalRevision(ctx, GoalRevisionDraft{
		ID: "goalrev_scheduler", GoalID: goal.ID, Revision: 1, RawGoal: "goal", Contract: map[string]any{"criteria": []any{"AC-1"}},
	}, 1, EventInput{Type: "GoalRevisionFrozen", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("FreezeGoalRevision() error = %v", err)
	}
	first := validTestWork("work_a", "plan_scheduler")
	second := validTestWork("work_b", "plan_scheduler")
	plan, err := store.CreatePlanRevision(ctx, PlanRevisionDraft{
		ID: "plan_scheduler", GoalRevisionID: revision.ID, Revision: 1,
		WorkItems:    []domain.WorkItem{second, first},
		Dependencies: []domain.WorkDependency{{FromID: first.ID, ToID: second.ID, Type: domain.DependencyHard}},
	}, EventInput{Type: "PlanRevisionCreated", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("CreatePlanRevision() error = %v", err)
	}
	if _, err := store.ActivatePlanRevision(ctx, plan.ID, 1, 2, EventInput{Type: "PlanActivated", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("ActivatePlanRevision() error = %v", err)
	}
	goal.State = domain.GoalRunning
	goal.ActiveRevisionID = revision.ID
	goal.Version = 3
	return goal, first, second
}

func claimAndCompleteWork(t *testing.T, store *Store, work domain.WorkItem, attemptID, leaseID string) domain.Lease {
	t.Helper()
	ctx := context.Background()
	persisted, err := store.WorkItem(ctx, work.ID)
	if err != nil {
		t.Fatalf("WorkItem() before claim error = %v", err)
	}
	if persisted.State == domain.WorkPending {
		if err := store.UpdateWorkState(ctx, work.ID, persisted.Version, domain.WorkReady, EventInput{Type: "WorkReady", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			t.Fatalf("make work ready: %v", err)
		}
		persisted, err = store.WorkItem(ctx, work.ID)
		if err != nil {
			t.Fatalf("WorkItem() after ready error = %v", err)
		}
	}
	attempt := domain.Attempt{ID: attemptID, WorkItemID: work.ID, AgentProfileID: "fake", State: domain.AttemptCreated, BaseTree: "base", PacketHash: "packet", Version: 1}
	lease, err := store.ClaimWork(ctx, work.ID, persisted.Version, LeaseDraft{ID: leaseID, Holder: "daemon/worker", TTL: time.Hour}, attempt, EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("ClaimWork() error = %v", err)
	}
	for index, state := range []domain.AttemptState{
		domain.AttemptPreparing,
		domain.AttemptStarting,
		domain.AttemptRunning,
		domain.AttemptCollecting,
		domain.AttemptValidating,
		domain.AttemptPromoting,
		domain.AttemptSucceeded,
	} {
		if err := store.UpdateAttemptStateWithLease(ctx, attempt.ID, int64(index+1), lease.ID, lease.Generation, state, EventInput{Type: "AttemptAdvanced", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			t.Fatalf("advance attempt to %s: %v", state, err)
		}
	}
	persisted, err = store.WorkItem(ctx, work.ID)
	if err != nil {
		t.Fatalf("WorkItem() after claim error = %v", err)
	}
	for _, state := range []domain.WorkState{domain.WorkRunning, domain.WorkVerifying, domain.WorkCompleted} {
		if err := store.UpdateWorkState(ctx, work.ID, persisted.Version, state, EventInput{Type: "WorkAdvanced", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			t.Fatalf("advance work to %s: %v", state, err)
		}
		persisted.Version++
		persisted.State = state
	}
	return lease
}
