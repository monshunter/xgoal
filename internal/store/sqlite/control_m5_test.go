package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/budget"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/reconcile"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestGateAuthorizationIsScopedFiniteAndAtomic(t *testing.T) {
	ctx := context.Background()
	source := clock.NewFake(time.Date(2026, 9, 2, 20, 0, 0, 0, time.UTC))
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	goal, _ := seedReadyWork(t, store, "work_gate")
	gate, err := store.CreateGate(ctx, GateDraft{ID: "gate_finite", GoalID: goal.ID, ReasonCode: "NETWORK_REQUIRED", Facts: []any{}, Unknowns: []any{}, Options: []any{"allow"}, Recommendation: "allow", Action: domain.ActionAccessProjectNetwork, Scope: []string{"proxy.golang.org"}, ExpiresAt: source.Now().Add(time.Minute), MaxUses: 1, Revocable: true, Required: true}, EventInput{Type: "GateOpened", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	gate, err = store.DecideGate(ctx, gate.ID, gate.Version, domain.GateAllow, "user", "allow once", EventInput{Type: "GateDecided", ActorType: "human", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	request := AuthorizationRequest{GoalID: goal.ID, Action: domain.ActionAccessProjectNetwork, Scope: []string{"proxy.golang.org"}}
	const contenders = 8
	start := make(chan struct{})
	errorsSeen := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := store.ConsumeAuthorization(ctx, gate.ID, request, EventInput{Type: "GateAuthorizationConsumed", ActorType: "kernel", Payload: map[string]any{}})
			errorsSeen <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	winners := 0
	for err := range errorsSeen {
		if err == nil {
			winners++
		} else if !errors.Is(err, basestore.ErrAuthorizationDenied) && !errors.Is(err, basestore.ErrConflict) {
			t.Fatalf("unexpected consume error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want 1", winners)
	}
	persisted, err := store.Gate(ctx, gate.ID)
	if err != nil || persisted.Used != 1 || persisted.Version != 3 {
		t.Fatalf("gate after consume = %+v, err=%v", persisted, err)
	}
	wrong := request
	wrong.Scope = []string{"example.com"}
	if _, err := store.ConsumeAuthorization(ctx, gate.ID, wrong, EventInput{Type: "Denied", ActorType: "kernel", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrAuthorizationDenied) {
		t.Fatalf("wrong scope error = %v", err)
	}
}

func TestExpiredAuthorizationFailsClosedAndPersistsExpiry(t *testing.T) {
	ctx := context.Background()
	source := clock.NewFake(time.Date(2026, 9, 2, 20, 0, 0, 0, time.UTC))
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	goal, _ := seedReadyWork(t, store, "work_expiry")
	gate, err := store.CreateGate(ctx, GateDraft{ID: "gate_expiry", GoalID: goal.ID, ReasonCode: "SECRET", Facts: []any{}, Unknowns: []any{}, Options: []any{"allow"}, Recommendation: "allow", Action: domain.ActionUseProjectSecret, Scope: []string{"TOKEN"}, ExpiresAt: source.Now().Add(time.Minute), MaxUses: 1, Revocable: true}, EventInput{Type: "GateOpened", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	gate, err = store.DecideGate(ctx, gate.ID, gate.Version, domain.GateAllow, "user", "one minute", EventInput{Type: "GateDecided", ActorType: "human", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	source.Advance(time.Minute)
	_, err = store.ConsumeAuthorization(ctx, gate.ID, AuthorizationRequest{GoalID: goal.ID, Action: domain.ActionUseProjectSecret, Scope: []string{"TOKEN"}}, EventInput{Type: "Consume", ActorType: "kernel", Payload: map[string]any{}})
	if !errors.Is(err, basestore.ErrExpired) {
		t.Fatalf("expired consume error = %v", err)
	}
	persisted, _ := store.Gate(ctx, gate.ID)
	if persisted.State != domain.GateExpired || persisted.Version != 3 {
		t.Fatalf("expiry was not persisted atomically, got %+v", persisted)
	}
}

func TestBudgetAndFailureRecordsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	source := clock.NewFake(time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC))
	store, err := Open(ctx, projectDir, source)
	if err != nil {
		t.Fatal(err)
	}
	goal, _ := seedReadyWork(t, store, "work_budget")
	if _, err := store.CreateBudget(ctx, goal.ID, "", budget.Limit{Dimension: budget.GoalAttempts, Soft: 2, Hard: 3}, true, EventInput{Type: "BudgetConfigured", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	result, err := store.ConsumeBudget(ctx, goal.ID, "", budget.Request{Dimension: budget.GoalAttempts, Known: true, Amount: 2}, EventInput{Type: "BudgetConsumed", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil || result.Decision != budget.SoftAttention {
		t.Fatalf("consume = %+v, err=%v", result, err)
	}
	if _, err := store.ConsumeBudget(ctx, goal.ID, "", budget.Request{Dimension: budget.GoalAttempts, Known: true, Amount: 2}, EventInput{Type: "BudgetConsumed", ActorType: "kernel", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrBudgetExceeded) {
		t.Fatalf("hard budget error = %v", err)
	}
	failure := reconcile.Failure{Class: reconcile.ValidatorFailed, PrimaryError: "exit 1 at 2026-09-02T21:00:00Z", ValidatorDefinitionHash: "validator", BaseTree: "base", ResultTree: "result", GoalRevisionHash: "goalhash", RelevantConfigHash: "config"}
	first, err := store.RecordFailure(ctx, FailureDraft{ID: "failure_1", GoalID: goal.ID, Failure: failure, Strategy: "codex", Current: reconcile.Snapshot{PlanRevision: 1}}, EventInput{Type: "FailureRecorded", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil || first.RepeatCount != 1 {
		t.Fatalf("first failure = %+v err=%v", first, err)
	}
	second, err := store.RecordFailure(ctx, FailureDraft{ID: "failure_2", GoalID: goal.ID, Failure: failure, Strategy: "codex", Previous: reconcile.Snapshot{PlanRevision: 1}, Current: reconcile.Snapshot{PlanRevision: 1}}, EventInput{Type: "FailureRecorded", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil || second.RepeatCount != 2 || second.MaterialProgress {
		t.Fatalf("second failure = %+v err=%v", second, err)
	}
	decision, err := reconcile.Decide(reconcile.Input{Failure: failure, Previous: second.Previous, Current: second.Current, SameFingerprintStrategy: second.RepeatCount - 1, BudgetAvailable: true})
	if err != nil || decision.Action == reconcile.RetryNewAttempt {
		t.Fatalf("decision = %+v err=%v", decision, err)
	}
	if _, err := store.RecordReconcileDecision(ctx, ReconcileRecord{ID: "decision_1", FailureID: second.ID, Decision: decision, PlanRevision: 1}, EventInput{Type: "ReconcileDecided", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	events, err := store.EventsAfter(ctx, "", 100)
	if err != nil || len(events) == 0 {
		t.Fatalf("events = %d err=%v", len(events), err)
	}
	cursor := events[len(events)-2].ID
	tail, err := store.EventsAfter(ctx, cursor, 100)
	if err != nil || len(tail) != 1 || tail[0].ID != events[len(events)-1].ID {
		t.Fatalf("tail = %+v err=%v", tail, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, projectDir, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snapshot, err := reopened.Budget(ctx, goal.ID, "", budget.GoalAttempts)
	if err != nil || !snapshot.Usage.Known || snapshot.Usage.Consumed != 2 {
		t.Fatalf("reopened budget = %+v err=%v", snapshot, err)
	}
}

func TestWorkerRecoveryResolutionRevokesLeaseAndReconcilesWork(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, work := seedReadyWork(t, store, "work_worker")
	attempt := domain.Attempt{ID: "attempt_worker", WorkItemID: work.ID, AgentProfileID: "codex", State: domain.AttemptCreated, BaseTree: "base", PacketHash: "packet", Version: 1}
	lease, err := store.ClaimWork(ctx, work.ID, 2, LeaseDraft{ID: "lease_worker", Holder: "daemon/worker", TTL: time.Minute}, attempt, EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := store.RecordWorker(ctx, WorkerProcess{AttemptID: attempt.ID, PID: 12345, PGID: 12345, StartIdentity: "Tue Sep 2 21:00:00 2026", State: WorkerRunning, Version: 1}, EventInput{Type: "WorkerStarted", ActorType: "daemon", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	worker, err = store.ResolveWorkerRecovery(ctx, worker.AttemptID, worker.Version, WorkerLost, "pid was reused", EventInput{Type: "WorkerRecovered", ActorType: "daemon", Payload: map[string]any{}})
	if err != nil || worker.State != WorkerLost {
		t.Fatalf("worker=%+v err=%v", worker, err)
	}
	persistedAttempt, _ := store.Attempt(ctx, attempt.ID)
	persistedLease, _ := store.Lease(ctx, lease.ID)
	persistedWork, _ := store.WorkItem(ctx, work.ID)
	if persistedAttempt.State != domain.AttemptInterrupted || persistedLease.State != domain.LeaseRevoked || persistedWork.State != domain.WorkReconciling {
		t.Fatalf("attempt=%s lease=%s work=%s", persistedAttempt.State, persistedLease.State, persistedWork.State)
	}
}

func TestGoalStatusProjectsActiveRuntimeFactsWithoutNestedQueryDeadlock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	goal, work := seedReadyWork(t, store, "work_status")
	if _, err := store.CreateGate(ctx, GateDraft{ID: "gate_status", GoalID: goal.ID, WorkItemID: work.ID, ReasonCode: "SCOPE", Facts: []any{}, Unknowns: []any{}, Options: []any{"deny"}, Recommendation: "deny", Action: domain.ActionExpandScope, Scope: []string{"docs"}, ExpiresAt: time.Now().Add(time.Minute), MaxUses: 1, Revocable: true}, EventInput{Type: "GateOpened", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	status, err := store.GoalStatus(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.WorkItems) != 1 || status.WorkItems[0].ID != work.ID || len(status.Gates) != 1 || status.Authority["gates"] != domain.AuthorityDecision {
		t.Fatalf("status = %+v", status)
	}
}
