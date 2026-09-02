package store_test

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/store"
)

func TestMemoryStoreCASAndEventAreAtomic(t *testing.T) {
	memory := store.NewMemory(clock.NewFake(time.Unix(100, 0).UTC()))
	goal := domain.Goal{ID: "goal_1", State: domain.GoalDraft, Version: 1}
	if err := memory.CreateGoal(goal, "GoalDrafted"); err != nil {
		t.Fatal(err)
	}
	if err := memory.UpdateGoalState(goal.ID, 1, domain.GoalReady, "GoalRevisionFrozen"); err != nil {
		t.Fatal(err)
	}
	if err := memory.UpdateGoalState(goal.ID, 1, domain.GoalRunning, "stale"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale UpdateGoalState() error = %v, want conflict", err)
	}
	got, err := memory.Goal(goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.GoalReady || got.Version != 2 {
		t.Fatalf("Goal() = %+v", got)
	}
	if events := memory.Events("goal", goal.ID); len(events) != 2 || events[1].Sequence != 2 {
		t.Fatalf("Events() = %+v", events)
	}
}

func TestMemoryStoreAllowsOnlyOneActiveLease(t *testing.T) {
	memory := store.NewMemory(clock.NewFake(time.Unix(100, 0).UTC()))
	work := domain.WorkItem{ID: "work_1", State: domain.WorkReady, Version: 1, Required: true}
	if err := memory.CreateWorkItem(work, "WorkReady"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errorsByAttempt := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			attemptID := "att_1"
			leaseID := "lease_1"
			if index == 2 {
				attemptID, leaseID = "att_2", "lease_2"
			}
			errorsByAttempt <- memory.ClaimWork(
				work.ID,
				1,
				domain.Lease{ID: leaseID, WorkItemID: work.ID, AttemptID: attemptID, Holder: "worker", Generation: int64(index), ExpiresAt: time.Unix(200, 0), Active: true},
				domain.Attempt{ID: attemptID, WorkItemID: work.ID, State: domain.AttemptCreated, Version: 1},
			)
		}(i)
	}
	wg.Wait()
	close(errorsByAttempt)

	succeeded := 0
	for err := range errorsByAttempt {
		if err == nil {
			succeeded++
			continue
		}
		if !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrActiveLease) {
			t.Fatalf("ClaimWork() error = %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful claims = %d, want 1", succeeded)
	}
}

func TestMemoryStoreLeaseClaimRemainsUniqueUnderContention(t *testing.T) {
	const contenders = 32
	memory := store.NewMemory(clock.NewFake(time.Unix(100, 0).UTC()))
	work := domain.WorkItem{ID: "work_contended", State: domain.WorkReady, Version: 1, Required: true}
	if err := memory.CreateWorkItem(work, "WorkReady"); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, contenders)
	var wg sync.WaitGroup
	for index := 1; index <= contenders; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			attemptID := "att_" + strconv.Itoa(index)
			results <- memory.ClaimWork(
				work.ID,
				1,
				domain.Lease{ID: "lease_" + attemptID, WorkItemID: work.ID, AttemptID: attemptID, Holder: "worker", Generation: int64(index), ExpiresAt: time.Unix(200, 0), Active: true},
				domain.Attempt{ID: attemptID, WorkItemID: work.ID, State: domain.AttemptCreated, Version: 1},
			)
		}(index)
	}
	close(start)
	wg.Wait()
	close(results)

	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		if !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrActiveLease) {
			t.Fatalf("ClaimWork() error = %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful claims = %d, want 1", succeeded)
	}
}
