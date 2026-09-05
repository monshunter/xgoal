package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/goalcompile"
	"github.com/monshunter/xgoal/internal/planner"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func planningFixture(t *testing.T) (*Store, planner.Request) {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, planner.Request{ProtocolVersion: planner.RequestVersion, GoalID: "goal_planning", RawGoal: "implement one change", Mode: "standard", CreatedBy: "user", ConfigHash: strings.Repeat("a", 64), ProfileID: "planner", Generation: 1, TrustedValidatorIDs: []string{"check"}}
}
func planningProposal() planner.Proposal {
	return planner.Proposal{ProtocolVersion: planner.ProposalVersion, Ambiguities: []string{}, Contract: goalcompile.Contract{Summary: "one change", Rationale: "required behavior", InScope: []string{"source"}, OutOfScope: []string{"deployment"}, Constraints: []string{"local only"}, AcceptanceCriteria: []goalcompile.AcceptanceCriterion{{ID: "AC1", Statement: "check passes", Validators: []string{"check"}}}, QualityAttributes: []string{"reliable"}, HumanGates: []string{"external publication"}, CompletionPolicy: goalcompile.CompletionPolicy{RequireAllRequiredItems: true, RequireNoBlockingFindings: true, RequireFinalValidation: true}}, Plan: goalcompile.Plan{Summary: "one work", WorkItems: []goalcompile.PlanWork{{ClientKey: "one", Title: "change", Objective: "implement", ReadScope: []string{"/**"}, WriteScope: []string{"/source"}, AcceptanceCriteria: []string{"AC1"}, Validators: []string{"check"}, RecommendedRole: domain.RoleImplementer, Required: true}}}}
}
func acceptPlanning(t *testing.T, s *Store, r planner.Request) PlanningRecord {
	t.Helper()
	if _, _, err := s.AcceptPlanningGoal(context.Background(), "POST /v1/goals", "once", map[string]any{"goal_id": r.GoalID, "raw_goal": r.RawGoal}, r); err != nil {
		t.Fatal(err)
	}
	p, err := s.Planning(context.Background(), r.GoalID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func observePlanning(t *testing.T, s *Store, r planner.Request) PlanningRecord {
	t.Helper()
	ctx := context.Background()
	p := acceptPlanning(t, s, r)
	identity := testCheckoutIdentity(t)
	p, err := s.BeginPlanning(ctx, PlanningClaim{GoalID: r.GoalID, EffectID: p.Effect.ID, Generation: p.Generation, ExpectedGoalVersion: p.Goal.Version, ExpectedEffectVersion: p.Effect.Version, CurrentConfigHash: r.ConfigHash, InvocationID: "invocation", InputTree: identity.HeadTree, CheckoutIdentity: identity})
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.PersistPlanningProposal(ctx, PlanningResult{GoalID: r.GoalID, EffectID: p.Effect.ID, Generation: p.Generation, ExpectedEffectVersion: p.Effect.Version, InvocationID: "invocation", Proposal: planningProposal()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func publication(p PlanningRecord) PlanningPublish {
	return PlanningPublish{GoalID: p.Goal.ID, EffectID: p.Effect.ID, Generation: p.Generation, ExpectedGoalVersion: p.Goal.Version, ExpectedEffectVersion: p.Effect.Version, CurrentConfigHash: p.Request.ConfigHash, ObservationHash: p.Effect.ObservationHash}
}
func TestPlanningAcceptanceIsAtomicAndReplayPrecedesCurrentValidation(t *testing.T) {
	s, r := planningFixture(t)
	ctx := context.Background()
	raw := map[string]any{"goal_id": r.GoalID}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_planner BEFORE INSERT ON effects BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AcceptPlanningGoal(ctx, "scope", "key", raw, r); err == nil {
		t.Fatal("expected atomic failure")
	}
	for _, table := range []string{"goals", "effects", "events", "idempotency_records"} {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial %s=%d %v", table, count, err)
		}
	}
	s.db.Exec(`DROP TRIGGER reject_planner`)
	first, created, err := s.AcceptPlanningGoal(ctx, "scope", "key", raw, r)
	if err != nil || !created || first.ResponseStatus != 201 {
		t.Fatalf("accept=%+v %v %v", first, created, err)
	}
	r.Proposal = &planner.Proposal{}
	r.TrustedValidatorIDs = nil
	replay, created, err := s.AcceptPlanningGoal(ctx, "scope", "key", raw, r)
	if err != nil || created || string(first.ResponseJSON) != string(replay.ResponseJSON) {
		t.Fatalf("replay=%+v %v %v", replay, created, err)
	}
	if _, _, err := s.AcceptPlanningGoal(ctx, "scope", "key", map[string]any{"goal_id": "different"}, r); !errors.Is(err, basestore.ErrIdempotencyConflict) {
		t.Fatalf("conflict=%v", err)
	}
	r.Proposal = nil
	if _, _, err := s.AcceptPlanningGoal(ctx, "scope", "other", raw, r); !errors.Is(err, basestore.ErrAlreadyExists) {
		t.Fatalf("duplicate Goal=%v", err)
	}
}
func TestPlanningPublishRollsBackEntireGraphAndCanRetry(t *testing.T) {
	s, r := planningFixture(t)
	ctx := context.Background()
	p := observePlanning(t, s, r)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_work BEFORE INSERT ON work_items BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPlanning(ctx, publication(p)); err == nil {
		t.Fatal("expected publish failure")
	}
	for _, table := range []string{"goal_revisions", "plan_revisions", "work_items"} {
		var count int
		s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count)
		if count != 0 {
			t.Fatalf("partial %s=%d", table, count)
		}
	}
	after, err := s.Planning(ctx, r.GoalID)
	if err != nil || after.Goal.State != domain.GoalDraft || after.Effect.State != domain.EffectObserving {
		t.Fatalf("partial planning=%+v %v", after, err)
	}
	s.db.Exec(`DROP TRIGGER reject_work`)
	done, err := s.PublishPlanning(ctx, publication(p))
	if err != nil || done.Goal.State != domain.GoalRunning || done.Effect.State != domain.EffectSucceeded {
		t.Fatalf("publish=%+v %v", done, err)
	}
	var ready int
	s.db.QueryRow(`SELECT count(*) FROM work_items WHERE state='READY'`).Scan(&ready)
	if ready != 1 {
		t.Fatalf("ready=%d", ready)
	}
}
