package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestLegacyPendingPromotionDoesNotOwnNewCheckoutAfterRecordedExit(t *testing.T) {
	for _, stopped := range []bool{true, false} {
		name := "exited"
		if !stopped {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, request, _ := seedPromotionFixture(t, "migration_"+name)
			defer store.Close()
			before, _, err := store.Ensure(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			beforeEffect, err := store.Effect(ctx, request.EffectID)
			if err != nil {
				t.Fatal(err)
			}
			// This is the exact persisted Goal classification assigned by migration 0008.
			if _, err := store.db.ExecContext(ctx, `UPDATE goals SET execution_model='git-worktree' WHERE id=?`, request.GoalID); err != nil {
				t.Fatal(err)
			}
			if stopped {
				worker, err := store.RecordWorker(ctx, WorkerProcess{AttemptID: request.AttemptID, PID: 12345, PGID: 12345, StartIdentity: "fixture process identity", State: WorkerRunning, Version: 1}, EventInput{Type: "WorkerStarted", ActorType: "kernel", Payload: map[string]any{}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.MarkWorkerExited(ctx, worker.AttemptID, worker.Version, EventInput{Type: "WorkerExited", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.ReconcileLegacyExecution(ctx); err != nil {
				t.Fatal(err)
			}
			if err := store.ReconcileLegacyExecution(ctx); err != nil {
				t.Fatal(err)
			}
			after, err := readPromotion(ctx, store.db, request.ID)
			if err != nil || !reflect.DeepEqual(before, after.Record) {
				t.Fatalf("legacy promotion changed: before=%+v after=%+v err=%v", before, after.Record, err)
			}
			afterEffect, err := store.Effect(ctx, request.EffectID)
			if err != nil || !reflect.DeepEqual(beforeEffect, afterEffect) {
				t.Fatalf("legacy effect changed: before=%+v after=%+v err=%v", beforeEffect, afterEffect, err)
			}
			lease, err := store.Lease(ctx, request.LeaseID)
			if err != nil {
				t.Fatal(err)
			}
			wantLease := domain.LeaseActive
			if stopped {
				wantLease = domain.LeaseRevoked
			}
			if lease.State != wantLease {
				t.Fatalf("lease=%s want=%s", lease.State, wantLease)
			}
			if pending, err := store.RecoverablePromotions(ctx); err != nil || len(pending) != 0 {
				t.Fatalf("legacy promotion remains executable: %+v %v", pending, err)
			}
			goal, _ := seedReadyWork(t, store, "new_"+name)
			identity := testCheckoutIdentity(t)
			_, err = store.AdmitCheckout(ctx, goal.ID, identity, identity.HeadTree)
			if stopped && err != nil {
				t.Fatalf("stopped legacy execution blocked clean new Goal: %v", err)
			}
			if !stopped && !errors.Is(err, ErrCheckoutBusy) {
				t.Fatalf("unobserved old process ownership was discarded: %v", err)
			}
		})
	}
}

func TestCheckoutReplanAndRetryRespectUnresolvedSceneAndActivePlan(t *testing.T) {
	for _, ownedScene := range []bool{true, false} {
		name := "owned_scene"
		if !ownedScene {
			name = "superseded_retry"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(ctx, t.TempDir(), clock.Real{})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			goal, work := seedReadyWork(t, store, name)
			identity := testCheckoutIdentity(t)
			if _, err := store.AdmitCheckout(ctx, goal.ID, identity, identity.HeadTree); err != nil {
				t.Fatal(err)
			}
			if ownedScene {
				if err := store.ClaimCheckoutWork(ctx, goal.ID, work.ID); err != nil {
					t.Fatal(err)
				}
				if err := store.ObserveCheckoutFailure(ctx, goal.ID, work.ID, identity, identity.HeadTree); err != nil {
					t.Fatal(err)
				}
			}
			event := EventInput{Type: "FixtureWaiting", ActorType: "kernel", Payload: map[string]any{}}
			if err := store.UpdateWorkState(ctx, work.ID, work.Version, domain.WorkWaiting, event); err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateGoalState(ctx, goal.ID, goal.Version, domain.GoalWaiting, event); err != nil {
				t.Fatal(err)
			}
			planID := "replacement_" + name
			plan, err := store.CreatePlanRevision(ctx, PlanRevisionDraft{ID: planID, GoalRevisionID: goal.ActiveRevisionID, Revision: 2, WorkItems: []domain.WorkItem{validTestWork("new_work_"+name, planID)}}, EventInput{Type: "PlanRevisionCreated", ActorType: "kernel", Payload: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.ActivateReplan(ctx, plan.ID, plan.Version, goal.Version+1, EventInput{Type: "PlanReplanned", ActorType: "human", Payload: map[string]any{}})
			if ownedScene {
				if !errors.Is(err, ErrCheckoutConflict) {
					t.Fatalf("replan accepted unresolved scene: %v", err)
				}
				oldPlan, readErr := store.PlanRevision(ctx, work.PlanRevisionID)
				if readErr != nil || oldPlan.Status != domain.PlanActive {
					t.Fatalf("rejected replan superseded old plan: %+v %v", oldPlan, readErr)
				}
				current, readErr := store.Checkout(ctx)
				if readErr != nil || current.WorkID != work.ID {
					t.Fatalf("rejected replan lost scene: %+v %v", current, readErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("clean unowned checkout could not replan: %v", err)
			}
			_, err = store.RetryCheckoutWork(ctx, work.ID, work.Version+1, identity, identity.HeadTree, EventInput{Type: "WorkRetryReady", ActorType: "human", Payload: map[string]any{}})
			if !errors.Is(err, basestore.ErrConflict) {
				t.Fatalf("superseded Work retry accepted: %v", err)
			}
			currentWork, err := store.WorkItem(ctx, work.ID)
			if err != nil || currentWork.State != domain.WorkWaiting {
				t.Fatalf("rejected retry changed old Work: %+v %v", currentWork, err)
			}
			current, err := store.Checkout(ctx)
			if err != nil || current.WorkID != "" || current.RetryAuthorized {
				t.Fatalf("rejected retry claimed checkout: %+v %v", current, err)
			}
		})
	}
}
