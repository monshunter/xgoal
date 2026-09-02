package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestGoalRepositoryCommitsStateAndEventTogether(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	store, err := Open(ctx, projectDir, clock.NewFake(time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	goal := domain.Goal{ID: "goal_1", State: domain.GoalDraft, Version: 1}
	if err := store.CreateGoal(ctx, goal, EventInput{
		Type:      "GoalDrafted",
		ActorType: "kernel",
		ActorID:   "kernel_1",
		Payload:   map[string]any{"goal_id": goal.ID},
	}); err != nil {
		t.Fatalf("CreateGoal() error = %v", err)
	}
	if err := store.UpdateGoalState(ctx, goal.ID, 1, domain.GoalReady, EventInput{
		Type:          "GoalRevisionFrozen",
		ActorType:     "kernel",
		ActorID:       "kernel_1",
		CorrelationID: "request_1",
		Payload:       map[string]any{"revision": 1},
	}); err != nil {
		t.Fatalf("UpdateGoalState() error = %v", err)
	}

	got, err := store.Goal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("Goal() error = %v", err)
	}
	if got.State != domain.GoalReady || got.Version != 2 {
		t.Fatalf("Goal() = %+v", got)
	}
	events, err := store.Events(ctx, "goal", goal.ID)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("Events() = %+v", events)
	}
	if events[1].EventType != "GoalRevisionFrozen" || events[1].CorrelationID != "request_1" || string(events[1].Payload) != `{"revision":1}` {
		t.Fatalf("second event = %+v payload=%s", events[1], events[1].Payload)
	}
}

func TestGoalRepositoryRejectsDuplicateAndStaleWritesWithoutEvents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), clock.Real{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	event := EventInput{Type: "GoalDrafted", ActorType: "kernel", Payload: map[string]any{}}
	goal := domain.Goal{ID: "goal_1", State: domain.GoalDraft, Version: 1}
	if err := store.CreateGoal(ctx, goal, event); err != nil {
		t.Fatalf("CreateGoal() error = %v", err)
	}
	if err := store.CreateGoal(ctx, goal, event); !errors.Is(err, basestore.ErrAlreadyExists) {
		t.Fatalf("duplicate CreateGoal() error = %v, want ErrAlreadyExists", err)
	}
	if err := store.UpdateGoalState(ctx, goal.ID, 2, domain.GoalReady, EventInput{Type: "stale", ActorType: "kernel", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrConflict) {
		t.Fatalf("stale UpdateGoalState() error = %v, want ErrConflict", err)
	}
	events, err := store.Events(ctx, "goal", goal.ID)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
}

func TestGoalRepositoryRejectsForgedInitialAndFinalTuples(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), clock.Real{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	event := EventInput{Type: "GoalDrafted", ActorType: "kernel", Payload: map[string]any{}}
	if err := store.CreateGoal(ctx, domain.Goal{ID: "goal_ready", State: domain.GoalReady, Version: 1}, event); err == nil {
		t.Fatal("CreateGoal() accepted non-DRAFT initial state")
	}
	if err := store.CreateGoal(ctx, domain.Goal{ID: "goal_forged", State: domain.GoalDraft, FinalTree: "tree", FinalEvidenceSetID: "set", FinalReportHash: "report", Version: 1}, event); err == nil {
		t.Fatal("CreateGoal() accepted a forged final tuple")
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO goals(
    id, state, active_revision_id, final_tree, final_evidence_set_id,
    final_report_hash, version, created_at, updated_at
)
VALUES ('goal_invalid_tuple', 'COMPLETED', '', '', '', '', 1, 'now', 'now')`); err == nil {
		t.Fatal("SQLite accepted COMPLETED without a final tuple")
	}
}

func TestGoalRepositoryRollsBackStateWhenEventInsertFails(t *testing.T) {
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
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER reject_test_event
BEFORE INSERT ON events
WHEN NEW.event_type = 'RejectForTest'
BEGIN
    SELECT RAISE(ABORT, 'rejected for atomicity test');
END;`); err != nil {
		t.Fatalf("create rejection trigger: %v", err)
	}

	err = store.UpdateGoalState(ctx, goal.ID, 1, domain.GoalReady, EventInput{Type: "RejectForTest", ActorType: "kernel", Payload: map[string]any{}})
	if err == nil {
		t.Fatal("UpdateGoalState() unexpectedly succeeded")
	}
	got, err := store.Goal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("Goal() error = %v", err)
	}
	if got.State != domain.GoalDraft || got.Version != 1 {
		t.Fatalf("Goal() after rejected event = %+v, want original state", got)
	}
	events, err := store.Events(ctx, "goal", goal.ID)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count after rejected event = %d, want 1", len(events))
	}
}

func TestGoalRepositoryCASAllowsOneConcurrentWinnerAndReopens(t *testing.T) {
	t.Parallel()

	const contenders = 32
	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	store, err := Open(ctx, projectDir, clock.Real{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	goal := domain.Goal{ID: "goal_contended", State: domain.GoalDraft, Version: 1}
	if err := store.CreateGoal(ctx, goal, EventInput{Type: "GoalDrafted", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatalf("CreateGoal() error = %v", err)
	}

	start := make(chan struct{})
	results := make(chan error, contenders)
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results <- store.UpdateGoalState(ctx, goal.ID, 1, domain.GoalReady, EventInput{
				Type:      "GoalRevisionFrozen",
				ActorType: "kernel",
				ActorID:   "worker_" + strconv.Itoa(index),
				Payload:   map[string]any{"winner": index},
			})
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
		if !errors.Is(err, basestore.ErrConflict) {
			t.Fatalf("concurrent update error = %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("CAS winners = %d, want 1", winners)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, projectDir, clock.Real{})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	got, err := reopened.Goal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("reopened Goal() error = %v", err)
	}
	if got.State != domain.GoalReady || got.Version != 2 {
		t.Fatalf("reopened Goal() = %+v", got)
	}
	events, err := reopened.Events(ctx, "goal", goal.ID)
	if err != nil {
		t.Fatalf("reopened Events() error = %v", err)
	}
	if len(events) != 2 || events[1].Sequence != 2 {
		t.Fatalf("reopened Events() = %+v", events)
	}
}
