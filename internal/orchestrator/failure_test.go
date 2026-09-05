package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/reconcile"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestFailureAfterLeaseExpiryReconcilesBeforeSceneDecision(t *testing.T) {
	engine, source, goal, work, revision, lease := failureFixture(t)
	source.Advance(2 * time.Second)
	if err := engine.failAttempt(context.Background(), goal, work, revision, lease, reconcile.AgentTimeout, errors.New("provider deadline"), "fixture", ""); err != nil {
		t.Fatalf("failure after expiry: %v", err)
	}
	status, err := engine.store.GoalStatus(context.Background(), goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Goal.State != domain.GoalWaiting || status.Attempts[0].State != domain.AttemptInterrupted || status.Leases[0].State != domain.LeaseExpired || status.WorkItems[0].State != domain.WorkWaiting {
		t.Fatalf("expired failure was stranded: %+v", status)
	}
}

func TestFailureWithUnconfirmedShutdownPreservesExecutionOwnership(t *testing.T) {
	engine, _, goal, work, revision, lease := failureFixture(t)
	if err := engine.failAttempt(context.Background(), goal, work, revision, lease, reconcile.AgentInterrupted, errExecutionStillRunning, "fixture", ""); err != nil {
		t.Fatal(err)
	}
	status, err := engine.store.GoalStatus(context.Background(), goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Goal.State != domain.GoalWaiting || status.Attempts[0].State != domain.AttemptCreated || status.Leases[0].State != domain.LeaseActive || status.WorkItems[0].State != domain.WorkClaimed {
		t.Fatalf("uncertain live worker lost ownership: %+v", status)
	}
}

func failureFixture(t *testing.T) (*Engine, *clock.Fake, domain.Goal, domain.WorkItem, domain.GoalRevision, domain.Lease) {
	t.Helper()
	ctx := context.Background()
	source := clock.NewFake(time.Now().UTC())
	state, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "state"), source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	goal := domain.Goal{ID: "failure_goal", State: domain.GoalDraft, Version: 1}
	if err := state.CreateGoal(ctx, goal, event("GoalCreated", "human", map[string]any{})); err != nil {
		t.Fatal(err)
	}
	revision, err := state.FreezeGoalRevision(ctx, sqlite.GoalRevisionDraft{ID: "failure_revision", GoalID: goal.ID, Revision: 1, RawGoal: "failure fixture", Contract: map[string]any{"criteria": []string{"AC-1"}}}, 1, event("GoalFrozen", "kernel", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	work := domain.WorkItem{ID: "failure_work", PlanRevisionID: "failure_plan", State: domain.WorkPending, Title: "failure", Objective: "preserve scene", ReadScope: []string{"/**"}, WriteScope: []string{"/owned.txt"}, AcceptanceCriteria: []string{"AC-1"}, ValidatorIDs: []string{"check"}, RecommendedRole: domain.RoleImplementer, Required: true, Version: 1}
	plan, err := state.CreatePlanRevision(ctx, sqlite.PlanRevisionDraft{ID: work.PlanRevisionID, GoalRevisionID: revision.ID, Revision: 1, WorkItems: []domain.WorkItem{work}}, event("PlanCreated", "kernel", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.ActivatePlanRevision(ctx, plan.ID, plan.Version, 2, event("PlanActivated", "kernel", map[string]any{})); err != nil {
		t.Fatal(err)
	}
	if _, err := state.RefreshReadyWork(ctx, goal.ID, event("WorkReady", "kernel", map[string]any{})); err != nil {
		t.Fatal(err)
	}
	work, err = state.WorkItem(ctx, work.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := state.ClaimWork(ctx, work.ID, work.Version, sqlite.LeaseDraft{ID: "failure_lease", Holder: "fixture", TTL: time.Second}, domain.Attempt{ID: "failure_attempt", WorkItemID: work.ID, AgentProfileID: "fixture", State: domain.AttemptCreated, BaseTree: strings.Repeat("a", 40), PacketHash: strings.Repeat("b", 64), Version: 1}, event("WorkClaimed", "kernel", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	goal, err = state.Goal(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{store: state, configHash: strings.Repeat("a", 64)}, source, goal, work, revision, lease
}
