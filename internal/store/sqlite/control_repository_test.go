package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestConcurrentClaimsAcrossWorkItemsCreateOnlyOneProjectLease(t *testing.T) {
	t.Parallel()

	const contenders = 32
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), clock.Real{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	works := seedIndependentReadyWork(t, store)

	start := make(chan struct{})
	results := make(chan error, contenders)
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			work := works[index%len(works)]
			_, err := store.ClaimWork(ctx, work.ID, 2, LeaseDraft{
				ID: fmt.Sprintf("lease_%02d", index), Holder: fmt.Sprintf("daemon/worker_%02d", index), TTL: time.Minute,
			}, domain.Attempt{
				ID: fmt.Sprintf("attempt_%02d", index), WorkItemID: work.ID, AgentProfileID: "fake",
				State: domain.AttemptCreated, BaseTree: "base", PacketHash: fmt.Sprintf("packet_%02d", index), Version: 1,
			}, EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{"contender": index}})
			results <- err
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
			continue
		}
		if !errors.Is(err, basestore.ErrActiveLease) && !errors.Is(err, basestore.ErrConflict) {
			t.Fatalf("concurrent claim error = %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent claim winners = %d, want 1", winners)
	}
	var activeLeases, attempts, claimedWork int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM leases WHERE state = ?`, domain.LeaseActive).Scan(&activeLeases); err != nil {
		t.Fatalf("count active leases: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts`).Scan(&attempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE state = ?`, domain.WorkClaimed).Scan(&claimedWork); err != nil {
		t.Fatalf("count claimed work: %v", err)
	}
	if activeLeases != 1 || attempts != 1 || claimedWork != 1 {
		t.Fatalf("active leases = %d, attempts = %d, claimed work = %d; want 1/1/1", activeLeases, attempts, claimedWork)
	}
}

func TestClaimWorkCreatesAttemptLeaseAndEventsAtomically(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	source := clock.NewFake(time.Date(2026, 9, 2, 15, 0, 0, 0, time.UTC))
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), source)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	_, work := seedReadyWork(t, store, "work_1")
	attempt := domain.Attempt{
		ID: "attempt_1", WorkItemID: work.ID, AgentProfileID: "codex-implementer",
		State: domain.AttemptCreated, BaseTree: "base123", PacketHash: "packet123", Version: 1,
	}
	lease, err := store.ClaimWork(ctx, work.ID, 2, LeaseDraft{
		ID: "lease_1", Holder: "daemon_1/worker_1", TTL: 90 * time.Second,
	}, attempt, EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("ClaimWork() error = %v", err)
	}
	if lease.State != domain.LeaseActive || lease.Generation != 1 || lease.AttemptID != attempt.ID || lease.AcquiredAt != source.Now() || lease.ExpiresAt != source.Now().Add(90*time.Second) {
		t.Fatalf("active lease = %+v", lease)
	}
	gotWork, err := store.WorkItem(ctx, work.ID)
	if err != nil {
		t.Fatalf("WorkItem() error = %v", err)
	}
	if gotWork.State != domain.WorkClaimed || gotWork.Version != 3 {
		t.Fatalf("claimed work = %+v", gotWork)
	}
	gotAttempt, err := store.Attempt(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("Attempt() error = %v", err)
	}
	if gotAttempt.State != domain.AttemptCreated || gotAttempt.Version != 1 || gotAttempt.BaseTree != "base123" {
		t.Fatalf("created attempt = %+v", gotAttempt)
	}
	if err := store.UpdateAttemptStateWithLease(ctx, attempt.ID, 1, lease.ID, lease.Generation, domain.AttemptPreparing, EventInput{Type: "AttemptPreparing", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("UpdateAttemptStateWithLease() error = %v", err)
	}
	if err := store.UpdateAttemptStateWithLease(ctx, attempt.ID, 1, lease.ID, lease.Generation, domain.AttemptStarting, EventInput{Type: "stale", ActorType: "kernel", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrConflict) {
		t.Fatalf("stale UpdateAttemptStateWithLease() error = %v, want ErrConflict", err)
	}
	if _, err := store.ClaimWork(ctx, work.ID, 2, LeaseDraft{
		ID: "lease_2", Holder: "daemon_1/worker_2", TTL: 90 * time.Second,
	}, domain.Attempt{ID: "attempt_2", WorkItemID: work.ID, AgentProfileID: "claude", State: domain.AttemptCreated, BaseTree: "base123", PacketHash: "packet456", Version: 1}, EventInput{Type: "duplicate", ActorType: "kernel", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrConflict) {
		t.Fatalf("duplicate ClaimWork() error = %v, want ErrConflict", err)
	}
	workEvents, err := store.Events(ctx, "work", work.ID)
	if err != nil {
		t.Fatalf("work Events() error = %v", err)
	}
	if len(workEvents) != 3 || workEvents[2].EventType != "LeaseAcquired" {
		t.Fatalf("work Events() = %+v", workEvents)
	}
	attemptEvents, err := store.Events(ctx, "attempt", attempt.ID)
	if err != nil {
		t.Fatalf("attempt Events() error = %v", err)
	}
	if len(attemptEvents) != 2 || attemptEvents[1].EventType != "AttemptPreparing" {
		t.Fatalf("attempt Events() = %+v", attemptEvents)
	}
}

func TestGatePersistsBoundedDecisionAndReopens(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	source := clock.NewFake(time.Date(2026, 9, 2, 16, 0, 0, 0, time.UTC))
	store, err := Open(ctx, projectDir, source)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	goal, work := seedReadyWork(t, store, "work_1")
	attempt := domain.Attempt{ID: "attempt_1", WorkItemID: work.ID, AgentProfileID: "codex", State: domain.AttemptCreated, BaseTree: "base", PacketHash: "packet", Version: 1}
	if _, err := store.ClaimWork(ctx, work.ID, 2, LeaseDraft{ID: "lease_1", Holder: "daemon/worker", TTL: time.Minute}, attempt, EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("ClaimWork() error = %v", err)
	}

	gate, err := store.CreateGate(ctx, GateDraft{
		ID: "gate_1", GoalID: goal.ID, WorkItemID: work.ID, AttemptID: attempt.ID,
		ReasonCode: "NETWORK_REQUIRED",
		Facts:      []any{map[string]any{"kind": "dependency", "value": "proxy.golang.org"}},
		Unknowns:   []any{"whether cache is warm"},
		Options: []any{
			map[string]any{"id": "deny-and-replan", "impact": "use cache"},
			map[string]any{"id": "allow-once", "impact": "limited access"},
		},
		Recommendation: "allow-once",
		Action:         domain.ActionAccessProjectNetwork, Scope: []string{"proxy.golang.org"},
		ExpiresAt: source.Now().Add(30 * time.Minute), MaxUses: 1, Revocable: true, Required: true,
	}, EventInput{Type: "GateOpened", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("CreateGate() error = %v", err)
	}
	if gate.State != domain.GateOpen || gate.Version != 1 || string(gate.UnknownsJSON) != `["whether cache is warm"]` || len(gate.Scope) != 1 {
		t.Fatalf("open gate = %+v", gate)
	}
	gate, err = store.DecideGate(ctx, gate.ID, 1, domain.GateAllow, "user_1", "allow checksum-locked dependency once", EventInput{Type: "GateDecided", ActorType: "human", ActorID: "user_1", Payload: map[string]any{"decision": "ALLOW"}})
	if err != nil {
		t.Fatalf("DecideGate() error = %v", err)
	}
	if gate.State != domain.GateApproved || gate.Decision != domain.GateAllow || gate.DecidedBy != "user_1" || gate.DecisionReason == "" || gate.Version != 2 || gate.DecidedAt.IsZero() {
		t.Fatalf("decided gate = %+v", gate)
	}
	if _, err := store.DecideGate(ctx, gate.ID, 1, domain.GateDeny, "user_2", "stale", EventInput{Type: "stale", ActorType: "human", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrConflict) {
		t.Fatalf("stale DecideGate() error = %v, want ErrConflict", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, projectDir, clock.Real{})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	persisted, err := reopened.Gate(ctx, gate.ID)
	if err != nil {
		t.Fatalf("Gate() error = %v", err)
	}
	if persisted.State != domain.GateApproved || persisted.Decision != domain.GateAllow || persisted.ExpiresAt != gate.ExpiresAt || persisted.MaxUses != 1 {
		t.Fatalf("persisted gate = %+v", persisted)
	}
}

func TestClaimWorkRollsBackAttemptLeaseAndWorkWhenEventFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), clock.Real{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	_, work := seedReadyWork(t, store, "work_1")
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER reject_attempt_creation_event
BEFORE INSERT ON events
WHEN NEW.event_type = 'AttemptCreated'
BEGIN
    SELECT RAISE(ABORT, 'reject attempt event for claim atomicity test');
END;`); err != nil {
		t.Fatalf("create rejection trigger: %v", err)
	}
	attempt := domain.Attempt{
		ID: "attempt_1", WorkItemID: work.ID, AgentProfileID: "codex",
		State: domain.AttemptCreated, BaseTree: "base", PacketHash: "packet", Version: 1,
	}
	if _, err := store.ClaimWork(ctx, work.ID, 2, LeaseDraft{ID: "lease_1", Holder: "daemon/worker", TTL: time.Minute}, attempt, EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{}}); err == nil {
		t.Fatal("ClaimWork() unexpectedly succeeded")
	}
	persistedWork, err := store.WorkItem(ctx, work.ID)
	if err != nil {
		t.Fatalf("WorkItem() error = %v", err)
	}
	if persistedWork.State != domain.WorkReady || persistedWork.Version != 2 {
		t.Fatalf("work after failed claim = %+v", persistedWork)
	}
	if _, err := store.Attempt(ctx, attempt.ID); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("Attempt() after failed claim error = %v, want ErrNotFound", err)
	}
	if _, err := store.Lease(ctx, "lease_1"); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("Lease() after failed claim error = %v, want ErrNotFound", err)
	}
}

func TestLeaseHeartbeatExpiryGenerationAndLateWriteIsolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	source := clock.NewFake(time.Date(2026, 9, 2, 17, 0, 0, 0, time.UTC))
	store, err := Open(ctx, projectDir, source)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	_, work := seedReadyWork(t, store, "work_1")
	attempt1 := domain.Attempt{ID: "attempt_1", WorkItemID: work.ID, AgentProfileID: "codex", State: domain.AttemptCreated, BaseTree: "base", PacketHash: "packet1", Version: 1}
	lease1, err := store.ClaimWork(ctx, work.ID, 2, LeaseDraft{ID: "lease_1", Holder: "daemon/worker1", TTL: 30 * time.Second}, attempt1, EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("ClaimWork(first) error = %v", err)
	}
	source.Advance(10 * time.Second)
	lease1, err = store.HeartbeatLease(ctx, lease1.ID, lease1.Generation, 1, 30*time.Second, EventInput{Type: "LeaseHeartbeat", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("HeartbeatLease() error = %v", err)
	}
	if lease1.Version != 2 || lease1.HeartbeatAt != source.Now() || lease1.ExpiresAt != source.Now().Add(30*time.Second) {
		t.Fatalf("heartbeat lease = %+v", lease1)
	}
	if _, err := store.HeartbeatLease(ctx, lease1.ID, lease1.Generation, 1, 30*time.Second, EventInput{Type: "stale", ActorType: "kernel", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrConflict) {
		t.Fatalf("stale heartbeat error = %v, want ErrConflict", err)
	}
	source.Advance(31 * time.Second)
	expired, err := store.ExpiredLeases(ctx)
	if err != nil {
		t.Fatalf("ExpiredLeases() error = %v", err)
	}
	if len(expired) != 1 || expired[0].ID != lease1.ID {
		t.Fatalf("ExpiredLeases() = %+v", expired)
	}
	if _, err := store.HeartbeatLease(ctx, lease1.ID, lease1.Generation, 2, 30*time.Second, EventInput{Type: "late", ActorType: "kernel", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrExpired) {
		t.Fatalf("late heartbeat error = %v, want ErrExpired", err)
	}
	if _, err := store.ResolveExpiredLease(ctx, lease1.ID, lease1.Generation, 2, false, EventInput{Type: "unsafe", ActorType: "kernel", Payload: map[string]any{}}); err == nil {
		t.Fatal("ResolveExpiredLease() accepted unconfirmed worker stop")
	}
	lease1, err = store.ResolveExpiredLease(ctx, lease1.ID, lease1.Generation, 2, true, EventInput{Type: "LeaseExpired", ActorType: "kernel", Payload: map[string]any{"worker_stopped": true}})
	if err != nil {
		t.Fatalf("ResolveExpiredLease() error = %v", err)
	}
	if lease1.State != domain.LeaseExpired || lease1.Version != 3 {
		t.Fatalf("resolved lease = %+v", lease1)
	}
	interrupted, err := store.Attempt(ctx, attempt1.ID)
	if err != nil {
		t.Fatalf("Attempt(first) error = %v", err)
	}
	if interrupted.State != domain.AttemptInterrupted || interrupted.Version != 2 {
		t.Fatalf("interrupted attempt = %+v", interrupted)
	}
	reconciling, err := store.WorkItem(ctx, work.ID)
	if err != nil {
		t.Fatalf("WorkItem() error = %v", err)
	}
	if reconciling.State != domain.WorkReconciling || reconciling.Version != 4 {
		t.Fatalf("reconciling work = %+v", reconciling)
	}
	if err := store.UpdateWorkState(ctx, work.ID, 4, domain.WorkReady, EventInput{Type: "WorkRetryReady", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("return work to ready: %v", err)
	}
	attempt2 := domain.Attempt{ID: "attempt_2", WorkItemID: work.ID, AgentProfileID: "claude", State: domain.AttemptCreated, BaseTree: "base", PacketHash: "packet2", Version: 1}
	lease2, err := store.ClaimWork(ctx, work.ID, 5, LeaseDraft{ID: "lease_2", Holder: "daemon/worker2", TTL: time.Minute}, attempt2, EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("ClaimWork(second) error = %v", err)
	}
	if lease2.Generation != 2 {
		t.Fatalf("second lease generation = %d, want 2", lease2.Generation)
	}
	if err := store.UpdateAttemptStateWithLease(ctx, attempt1.ID, 2, lease1.ID, lease1.Generation, domain.AttemptPreparing, EventInput{Type: "LateWorkerWrite", ActorType: "agent", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrStaleLease) {
		t.Fatalf("late worker write error = %v, want ErrStaleLease", err)
	}
	stillInterrupted, err := store.Attempt(ctx, attempt1.ID)
	if err != nil {
		t.Fatalf("Attempt(first after late write) error = %v", err)
	}
	if stillInterrupted.State != domain.AttemptInterrupted || stillInterrupted.Version != 2 {
		t.Fatalf("late write changed old attempt = %+v", stillInterrupted)
	}
	if err := store.UpdateAttemptStateWithLease(ctx, attempt2.ID, 1, lease2.ID, lease2.Generation, domain.AttemptPreparing, EventInput{Type: "AttemptPreparing", ActorType: "agent", Payload: map[string]any{}}); err != nil {
		t.Fatalf("current worker write error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, projectDir, clock.Real{})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	persistedLease, err := reopened.Lease(ctx, lease2.ID)
	if err != nil {
		t.Fatalf("Lease(second) after reopen error = %v", err)
	}
	if persistedLease.State != domain.LeaseActive || persistedLease.Generation != 2 {
		t.Fatalf("persisted second lease = %+v", persistedLease)
	}
	persistedAttempt, err := reopened.Attempt(ctx, attempt2.ID)
	if err != nil {
		t.Fatalf("Attempt(second) after reopen error = %v", err)
	}
	if persistedAttempt.State != domain.AttemptPreparing || persistedAttempt.Version != 2 {
		t.Fatalf("persisted second attempt = %+v", persistedAttempt)
	}
}

func seedReadyWork(t *testing.T, store *Store, workID string) (domain.Goal, domain.WorkItem) {
	t.Helper()
	ctx := context.Background()
	goal := domain.Goal{ID: "goal_" + workID, State: domain.GoalDraft, Version: 1}
	if err := store.CreateGoal(ctx, goal, EventInput{Type: "GoalDrafted", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("CreateGoal() error = %v", err)
	}
	revision, err := store.FreezeGoalRevision(ctx, GoalRevisionDraft{
		ID: "goalrev_" + workID, GoalID: goal.ID, Revision: 1, RawGoal: "goal", Contract: map[string]any{"criteria": []any{"AC-1"}},
	}, 1, EventInput{Type: "GoalRevisionFrozen", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("FreezeGoalRevision() error = %v", err)
	}
	planID := "planrev_" + workID
	work := validTestWork(workID, planID)
	plan, err := store.CreatePlanRevision(ctx, PlanRevisionDraft{
		ID: planID, GoalRevisionID: revision.ID, Revision: 1, WorkItems: []domain.WorkItem{work},
	}, EventInput{Type: "PlanRevisionCreated", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("CreatePlanRevision() error = %v", err)
	}
	if _, err := store.ActivatePlanRevision(ctx, plan.ID, 1, 2, EventInput{Type: "PlanActivated", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("ActivatePlanRevision() error = %v", err)
	}
	if err := store.UpdateWorkState(ctx, work.ID, 1, domain.WorkReady, EventInput{Type: "WorkReady", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("UpdateWorkState() error = %v", err)
	}
	goal.State = domain.GoalRunning
	goal.ActiveRevisionID = revision.ID
	goal.Version = 3
	work.State = domain.WorkReady
	work.Version = 2
	return goal, work
}

func seedIndependentReadyWork(t *testing.T, store *Store) []domain.WorkItem {
	t.Helper()
	ctx := context.Background()
	goal := domain.Goal{ID: "goal_concurrent_claims", State: domain.GoalDraft, Version: 1}
	if err := store.CreateGoal(ctx, goal, EventInput{Type: "GoalDrafted", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("CreateGoal() error = %v", err)
	}
	revision, err := store.FreezeGoalRevision(ctx, GoalRevisionDraft{
		ID: "goalrev_concurrent_claims", GoalID: goal.ID, Revision: 1, RawGoal: "goal", Contract: map[string]any{"criteria": []any{"AC-1"}},
	}, 1, EventInput{Type: "GoalRevisionFrozen", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("FreezeGoalRevision() error = %v", err)
	}
	works := []domain.WorkItem{
		validTestWork("work_concurrent_a", "plan_concurrent_claims"),
		validTestWork("work_concurrent_b", "plan_concurrent_claims"),
	}
	works[0].WriteScope = []string{"/a/**"}
	works[1].WriteScope = []string{"/b/**"}
	plan, err := store.CreatePlanRevision(ctx, PlanRevisionDraft{
		ID: "plan_concurrent_claims", GoalRevisionID: revision.ID, Revision: 1, WorkItems: works,
	}, EventInput{Type: "PlanRevisionCreated", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("CreatePlanRevision() error = %v", err)
	}
	if _, err := store.ActivatePlanRevision(ctx, plan.ID, 1, 2, EventInput{Type: "PlanActivated", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("ActivatePlanRevision() error = %v", err)
	}
	ready, err := store.RefreshReadyWork(ctx, goal.ID, EventInput{Type: "WorkReady", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("RefreshReadyWork() error = %v", err)
	}
	if len(ready) != len(works) {
		t.Fatalf("ready work = %v, want %d items", ready, len(works))
	}
	return works
}
