package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestFreezeGoalRevisionPersistsImmutableCanonicalContract(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	store, err := Open(ctx, projectDir, clock.NewFake(time.Date(2026, 9, 2, 13, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	goal := domain.Goal{ID: "goal_1", State: domain.GoalDraft, Version: 1}
	if err := store.CreateGoal(ctx, goal, EventInput{Type: "GoalDrafted", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("CreateGoal() error = %v", err)
	}

	revision, err := store.FreezeGoalRevision(ctx, GoalRevisionDraft{
		ID:       "goalrev_1",
		GoalID:   goal.ID,
		Revision: 1,
		RawGoal:  "implement durable control loop",
		Contract: map[string]any{"summary": "durable", "criteria": []any{"store", "restart"}},
	}, 1, EventInput{Type: "GoalRevisionFrozen", ActorType: "kernel", Payload: map[string]any{"revision": 1}})
	if err != nil {
		t.Fatalf("FreezeGoalRevision() error = %v", err)
	}
	if string(revision.ContractJSON) != `{"criteria":["store","restart"],"summary":"durable"}` || revision.Hash == "" || revision.FrozenAt.IsZero() {
		t.Fatalf("frozen revision = %+v contract=%s", revision, revision.ContractJSON)
	}
	gotGoal, err := store.Goal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("Goal() error = %v", err)
	}
	if gotGoal.ActiveRevisionID != revision.ID || gotGoal.State != domain.GoalReady || gotGoal.Version != 2 {
		t.Fatalf("Goal() after freeze = %+v", gotGoal)
	}
	if _, err := store.FreezeGoalRevision(ctx, GoalRevisionDraft{
		ID: revision.ID, GoalID: goal.ID, Revision: 1, RawGoal: "mutated", Contract: map[string]any{"summary": "mutated"},
	}, 2, EventInput{Type: "Mutation", ActorType: "kernel", Payload: map[string]any{}}); err == nil {
		t.Fatal("immutable revision overwrite unexpectedly succeeded")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, projectDir, clock.Real{})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	persisted, err := reopened.GoalRevision(ctx, revision.ID)
	if err != nil {
		t.Fatalf("GoalRevision() error = %v", err)
	}
	if persisted.Hash != revision.Hash || string(persisted.ContractJSON) != string(revision.ContractJSON) {
		t.Fatalf("persisted revision = %+v", persisted)
	}
}

func TestPlanRevisionDAGActivationAndWorkCASPersist(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	store, err := Open(ctx, projectDir, clock.NewFake(time.Date(2026, 9, 2, 14, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	goal := domain.Goal{ID: "goal_1", State: domain.GoalDraft, Version: 1}
	if err := store.CreateGoal(ctx, goal, EventInput{Type: "GoalDrafted", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("CreateGoal() error = %v", err)
	}
	revision, err := store.FreezeGoalRevision(ctx, GoalRevisionDraft{
		ID: "goalrev_1", GoalID: goal.ID, Revision: 1, RawGoal: "goal", Contract: map[string]any{"criteria": []any{"AC-1"}},
	}, 1, EventInput{Type: "GoalRevisionFrozen", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("FreezeGoalRevision() error = %v", err)
	}
	workStore := domain.WorkItem{
		ID: "work_store", PlanRevisionID: "planrev_1", State: domain.WorkPending,
		Title: "Store", Objective: "persist state", ReadScope: []string{"/**"}, WriteScope: []string{"/internal/store/**"},
		AcceptanceCriteria: []string{"AC-1"}, ValidatorIDs: []string{"go-test"}, RecommendedRole: domain.RoleImplementer,
		Required: true, Version: 1,
	}
	workRestart := domain.WorkItem{
		ID: "work_restart", PlanRevisionID: "planrev_1", State: domain.WorkPending,
		Title: "Restart", Objective: "recover state", ReadScope: []string{"/**"}, WriteScope: []string{"/internal/kernel/**"},
		AcceptanceCriteria: []string{"AC-1"}, ValidatorIDs: []string{"go-test"}, RecommendedRole: domain.RoleImplementer,
		Required: true, Version: 1,
	}
	plan, err := store.CreatePlanRevision(ctx, PlanRevisionDraft{
		ID: "planrev_1", GoalRevisionID: revision.ID, Revision: 1,
		WorkItems:    []domain.WorkItem{workRestart, workStore},
		Dependencies: []domain.WorkDependency{{FromID: workStore.ID, ToID: workRestart.ID, Type: domain.DependencyHard}},
	}, EventInput{Type: "PlanRevisionCreated", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("CreatePlanRevision() error = %v", err)
	}
	if plan.Status != domain.PlanDraft || plan.Version != 1 || plan.GraphHash == "" {
		t.Fatalf("created plan = %+v", plan)
	}
	_, _, reorderedHash, err := validateAndHashPlan(PlanRevisionDraft{
		ID: "planrev_1", GoalRevisionID: revision.ID, Revision: 1,
		WorkItems:    []domain.WorkItem{workStore, workRestart},
		Dependencies: []domain.WorkDependency{{FromID: workStore.ID, ToID: workRestart.ID, Type: domain.DependencyHard}},
	})
	if err != nil {
		t.Fatalf("validate reordered plan: %v", err)
	}
	if reorderedHash != plan.GraphHash {
		t.Fatalf("reordered graph hash = %q, want %q", reorderedHash, plan.GraphHash)
	}
	items, err := store.WorkItems(ctx, plan.ID)
	if err != nil {
		t.Fatalf("WorkItems() error = %v", err)
	}
	if len(items) != 2 || items[0].ID != "work_restart" || items[1].ID != "work_store" {
		t.Fatalf("WorkItems() = %+v", items)
	}
	if len(items[1].WriteScope) != 1 || items[1].WriteScope[0] != "/internal/store/**" {
		t.Fatalf("persisted work scope = %+v", items[1])
	}

	plan, err = store.ActivatePlanRevision(ctx, plan.ID, 1, 2, EventInput{Type: "PlanActivated", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("ActivatePlanRevision() error = %v", err)
	}
	if plan.Status != domain.PlanActive || plan.Version != 2 {
		t.Fatalf("active plan = %+v", plan)
	}
	gotGoal, err := store.Goal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("Goal() error = %v", err)
	}
	if gotGoal.State != domain.GoalRunning || gotGoal.Version != 3 {
		t.Fatalf("Goal() after plan activation = %+v", gotGoal)
	}
	if err := store.UpdateWorkState(ctx, workRestart.ID, 1, domain.WorkReady, EventInput{Type: "TooEarly", ActorType: "kernel", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrConflict) {
		t.Fatalf("dependent work became ready early, error = %v, want ErrConflict", err)
	}
	if err := store.UpdateWorkState(ctx, workStore.ID, 1, domain.WorkReady, EventInput{Type: "WorkReady", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("UpdateWorkState() error = %v", err)
	}
	if err := store.UpdateWorkState(ctx, workStore.ID, 1, domain.WorkClaimed, EventInput{Type: "stale", ActorType: "kernel", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrConflict) {
		t.Fatalf("stale UpdateWorkState() error = %v, want ErrConflict", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, projectDir, clock.Real{})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	persistedPlan, err := reopened.PlanRevision(ctx, plan.ID)
	if err != nil {
		t.Fatalf("PlanRevision() error = %v", err)
	}
	if persistedPlan.Status != domain.PlanActive || persistedPlan.GraphHash != plan.GraphHash {
		t.Fatalf("persisted plan = %+v", persistedPlan)
	}
	persistedWork, err := reopened.WorkItem(ctx, workStore.ID)
	if err != nil {
		t.Fatalf("WorkItem() error = %v", err)
	}
	if persistedWork.State != domain.WorkReady || persistedWork.Version != 2 {
		t.Fatalf("persisted work = %+v", persistedWork)
	}
}

func TestPlanRevisionRollsBackWholeGraphWhenWorkEventFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), clock.Real{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	goal := domain.Goal{ID: "goal_1", State: domain.GoalDraft, Version: 1}
	if err := store.CreateGoal(ctx, goal, EventInput{Type: "GoalDrafted", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("CreateGoal() error = %v", err)
	}
	revision, err := store.FreezeGoalRevision(ctx, GoalRevisionDraft{
		ID: "goalrev_1", GoalID: goal.ID, Revision: 1, RawGoal: "goal", Contract: map[string]any{"criteria": []any{"AC-1"}},
	}, 1, EventInput{Type: "GoalRevisionFrozen", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("FreezeGoalRevision() error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER reject_work_creation_event
BEFORE INSERT ON events
WHEN NEW.event_type = 'WorkItemCreated'
BEGIN
    SELECT RAISE(ABORT, 'reject work event for graph atomicity test');
END;`); err != nil {
		t.Fatalf("create rejection trigger: %v", err)
	}
	work := validTestWork("work_1", "planrev_1")
	_, err = store.CreatePlanRevision(ctx, PlanRevisionDraft{
		ID: "planrev_1", GoalRevisionID: revision.ID, Revision: 1, WorkItems: []domain.WorkItem{work},
	}, EventInput{Type: "PlanRevisionCreated", ActorType: "kernel", Payload: map[string]any{}})
	if err == nil {
		t.Fatal("CreatePlanRevision() unexpectedly succeeded")
	}
	if _, err := store.PlanRevision(ctx, "planrev_1"); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("PlanRevision() after rollback error = %v, want ErrNotFound", err)
	}
	if _, err := store.WorkItem(ctx, work.ID); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("WorkItem() after rollback error = %v, want ErrNotFound", err)
	}
}

func TestPlanRevisionRejectsCycleWithoutPartialRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), clock.Real{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	goal := domain.Goal{ID: "goal_1", State: domain.GoalDraft, Version: 1}
	if err := store.CreateGoal(ctx, goal, EventInput{Type: "GoalDrafted", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("CreateGoal() error = %v", err)
	}
	revision, err := store.FreezeGoalRevision(ctx, GoalRevisionDraft{
		ID: "goalrev_1", GoalID: goal.ID, Revision: 1, RawGoal: "goal", Contract: map[string]any{"criteria": []any{"AC-1"}},
	}, 1, EventInput{Type: "GoalRevisionFrozen", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("FreezeGoalRevision() error = %v", err)
	}
	workA := validTestWork("work_a", "planrev_cycle")
	workB := validTestWork("work_b", "planrev_cycle")
	_, err = store.CreatePlanRevision(ctx, PlanRevisionDraft{
		ID: "planrev_cycle", GoalRevisionID: revision.ID, Revision: 1,
		WorkItems: []domain.WorkItem{workA, workB},
		Dependencies: []domain.WorkDependency{
			{FromID: workA.ID, ToID: workB.ID, Type: domain.DependencyHard},
			{FromID: workB.ID, ToID: workA.ID, Type: domain.DependencyHard},
		},
	}, EventInput{Type: "PlanRevisionCreated", ActorType: "kernel", Payload: map[string]any{}})
	if err == nil {
		t.Fatal("cyclic plan unexpectedly succeeded")
	}
	if _, err := store.PlanRevision(ctx, "planrev_cycle"); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("PlanRevision(cycle) error = %v, want ErrNotFound", err)
	}
	if _, err := store.WorkItem(ctx, workA.ID); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("WorkItem(cycle) error = %v, want ErrNotFound", err)
	}
}

func validTestWork(id, planID string) domain.WorkItem {
	return domain.WorkItem{
		ID: id, PlanRevisionID: planID, State: domain.WorkPending,
		Title: id, Objective: "objective", ReadScope: []string{"/**"}, WriteScope: []string{"/internal/**"},
		AcceptanceCriteria: []string{"AC-1"}, ValidatorIDs: []string{"go-test"}, RecommendedRole: domain.RoleImplementer,
		Required: true, Version: 1,
	}
}
