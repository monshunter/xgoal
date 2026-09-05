package sqlite

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/planner"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/supervisor"
)

func beginFixturePlanning(t *testing.T, s *Store, p PlanningRecord) PlanningRecord {
	t.Helper()
	identity := testCheckoutIdentity(t)
	result, err := s.BeginPlanning(context.Background(), PlanningClaim{GoalID: p.Goal.ID, EffectID: p.Effect.ID, Generation: p.Generation, ExpectedGoalVersion: p.Goal.Version, ExpectedEffectVersion: p.Effect.Version, CurrentConfigHash: p.Request.ConfigHash, InvocationID: "invocation", InputTree: identity.HeadTree, CheckoutIdentity: identity})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPlanningConcurrentAcceptanceHasOneGoalAndOneEffect(t *testing.T) {
	s, r := planningFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	type outcome struct {
		created bool
		err     error
	}
	results := make(chan outcome, 12)
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, err := s.AcceptPlanningGoal(ctx, "scope", "same", map[string]any{"goal_id": r.GoalID}, r)
			results <- outcome{created, err}
		}()
	}
	wg.Wait()
	close(results)
	created := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("creators=%d", created)
	}
	for _, table := range []string{"goals", "effects", "idempotency_records"} {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s=%d err=%v", table, count, err)
		}
	}
}

func TestPlanningRecoveryRequiresStoppedProcessAndBoundsRepeatedInterruptions(t *testing.T) {
	s, r := planningFixture(t)
	ctx := context.Background()
	p := beginFixturePlanning(t, s, acceptPlanning(t, s, r))
	intent := supervisor.ProcessIntent{ID: "live_planner", Owner: supervisor.Owner{Kind: "planning", ID: p.Effect.ID, GoalID: p.Goal.ID, Generation: p.Generation}}
	if err := s.BeginProcess(ctx, intent); err != nil {
		t.Fatal(err)
	}
	options := PlanningRecoveryOptions{CurrentConfigHash: r.ConfigHash, NoProgressLimit: 2}
	if err := s.RecoverPlanning(ctx, options); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := s.Planning(ctx, r.GoalID)
	if unchanged.Generation != 1 || unchanged.Effect.State != domain.EffectExecuting {
		t.Fatalf("live invocation was retried: %+v", unchanged)
	}
	if err := s.FinishProcess(ctx, intent.ID, supervisor.ProcessTerminated, "startup was never released"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverPlanning(ctx, options); err != nil {
		t.Fatal(err)
	}
	next, err := s.Planning(ctx, r.GoalID)
	if err != nil || next.Generation != 2 || next.State != "QUEUED" {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	previous, err := s.Effect(ctx, p.Effect.ID)
	if err != nil || previous.State != domain.EffectFailed || string(previous.RequestJSON) != string(p.Effect.RequestJSON) {
		t.Fatalf("old request changed: %+v %v", previous, err)
	}
	beginFixturePlanning(t, s, next)
	if err := s.RecoverPlanning(ctx, options); err != nil {
		t.Fatal(err)
	}
	blocked, _ := s.Planning(ctx, r.GoalID)
	if blocked.Generation != 2 || blocked.State != "WAITING" {
		t.Fatalf("unbounded recovery: %+v", blocked)
	}
	if err := s.RecoverPlanning(ctx, options); err != nil {
		t.Fatal(err)
	}
	repeated, _ := s.Planning(ctx, r.GoalID)
	if repeated.Effect.Version != blocked.Effect.Version || repeated.Goal.Version != blocked.Goal.Version {
		t.Fatal("recovery repeated side effects")
	}
}

func TestPlanningPauseRetainsObservationAndCancellationFencesPublication(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		s, r := planningFixture(t)
		ctx := context.Background()
		p := observePlanning(t, s, r)
		if cancelled {
			if err := s.UpdateGoalState(ctx, r.GoalID, p.Goal.Version, domain.GoalCancelled, EventInput{Type: "Cancelled", ActorType: "human", Payload: map[string]any{}}); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := s.SetPlanningPaused(ctx, r.GoalID, p.Goal.Version, true, "inspect"); err != nil {
				t.Fatal(err)
			}
		}
		current, _ := s.Planning(ctx, r.GoalID)
		if _, err := s.PublishPlanning(ctx, publication(current)); !errors.Is(err, ErrPlanningBlocked) {
			t.Fatalf("late publish=%v", err)
		}
		if err := s.RecoverPlanning(ctx, PlanningRecoveryOptions{CurrentConfigHash: r.ConfigHash}); err != nil {
			t.Fatal(err)
		}
		after, _ := s.Planning(ctx, r.GoalID)
		if after.Goal.ActiveRevisionID != "" || after.Observation == nil || after.Observation.Proposal == nil {
			t.Fatalf("lost proposal or frozen control: %+v", after)
		}
		if !cancelled {
			after, err := s.SetPlanningPaused(ctx, r.GoalID, after.Goal.Version, false, "continue")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.PublishPlanning(ctx, publication(after)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPlanningConfigurationRetryPreservesHistoryAndRejectsStaleCAS(t *testing.T) {
	s, r := planningFixture(t)
	ctx := context.Background()
	p := acceptPlanning(t, s, r)
	changed := strings.Repeat("b", 64)
	if err := s.RecoverPlanning(ctx, PlanningRecoveryOptions{CurrentConfigHash: changed}); err != nil {
		t.Fatal(err)
	}
	waiting, _ := s.Planning(ctx, r.GoalID)
	if waiting.State != "WAITING" {
		t.Fatalf("configuration drift queued: %+v", waiting)
	}
	r.ConfigHash = changed
	r.Proposal = ptrPlanningProposal()
	if _, err := s.RetryPlanning(ctx, r.GoalID, waiting.Goal.Version+1, r, "bind reviewed config"); !errors.Is(err, basestore.ErrConflict) {
		t.Fatalf("stale retry=%v", err)
	}
	next, err := s.RetryPlanning(ctx, r.GoalID, waiting.Goal.Version, r, "bind reviewed config")
	if err != nil || next.Generation != 2 || next.State != "QUEUED" || next.Request.ConfigHash != changed {
		t.Fatalf("retry=%+v %v", next, err)
	}
	old, err := s.Effect(ctx, p.Effect.ID)
	if err != nil || old.RequestHash != p.Effect.RequestHash {
		t.Fatal("retry overwrote historical request")
	}
	gates, err := s.Gates(ctx, r.GoalID, true)
	if err != nil || len(gates) != 0 {
		t.Fatalf("planner gates not resolved: %+v %v", gates, err)
	}
}
func ptrPlanningProposal() *planner.Proposal { p := planningProposal(); return &p }

func TestLegacyInterruptedRequestBecomesDeterministicWithoutGuessingGoalOwnership(t *testing.T) {
	s, r := planningFixture(t)
	ctx := context.Background()
	raw := map[string]any{"goal_id": r.GoalID, "raw_goal": r.RawGoal, "mode": r.Mode}
	request, _, err := s.BeginIdempotentRequest(ctx, "POST /v1/goals", "legacy", raw)
	if err != nil {
		t.Fatal(err)
	}
	// Historical GoalCreated only recorded raw_goal/mode. It does not prove
	// the original key, actor, proposal or request hash.
	if err := s.CreateGoal(ctx, domain.Goal{ID: r.GoalID, State: domain.GoalDraft, Version: 1}, EventInput{Type: "GoalCreated", ActorType: "user", Payload: map[string]any{"raw_goal": r.RawGoal, "mode": r.Mode}}); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err := s.RecoverPlanning(ctx, PlanningRecoveryOptions{CurrentConfigHash: r.ConfigHash}); err != nil {
			t.Fatal(err)
		}
	}
	after, err := s.LookupIdempotentRequest(ctx, "POST /v1/goals", "legacy", raw)
	if err != nil || after.State != domain.IdempotencyCompleted || after.ResponseStatus != 409 || !strings.Contains(string(after.ResponseJSON), "REQUEST_INTERRUPTED") || after.RequestHash != request.RequestHash || string(after.RequestJSON) != string(request.RequestJSON) {
		t.Fatalf("recovered=%+v %v", after, err)
	}
	p, err := s.Planning(ctx, r.GoalID)
	if err != nil || p.Effect.ID != "" || p.Goal.State != domain.GoalDraft {
		t.Fatalf("invented a runnable request: %+v %v", p, err)
	}
	gates, err := s.Gates(ctx, r.GoalID, true)
	if err != nil || len(gates) != 1 {
		t.Fatalf("repeated recovery gates: %+v %v", gates, err)
	}
}

func TestPlanningPauseCancellationResumesWithoutConsumingFailureBudget(t *testing.T) {
	s, r := planningFixture(t)
	ctx := context.Background()
	p := beginFixturePlanning(t, s, acceptPlanning(t, s, r))
	paused, err := s.SetPlanningPaused(ctx, r.GoalID, p.Goal.Version, true, "pause running planner")
	if err != nil {
		t.Fatal(err)
	}
	// Resume may race with the old cancelled invocation finishing. The durable
	// control event still attributes that interruption to the operator pause.
	if _, err := s.SetPlanningPaused(ctx, r.GoalID, paused.Goal.Version, false, "resume"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailPlanning(ctx, PlanningFailure{GoalID: r.GoalID, EffectID: p.Effect.ID, Generation: p.Generation, ExpectedEffectVersion: p.Effect.Version, Code: "planner_interrupted", Reason: "context cancelled", ExecutionStopped: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverPlanning(ctx, PlanningRecoveryOptions{CurrentConfigHash: r.ConfigHash, NoProgressLimit: 1}); err != nil {
		t.Fatal(err)
	}
	next, err := s.Planning(ctx, r.GoalID)
	if err != nil || next.Generation != 2 || next.State != "QUEUED" {
		t.Fatalf("pause consumed failure budget: %+v %v", next, err)
	}
}
