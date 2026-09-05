package sqlite

import (
	"context"
	"testing"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/goalcompile"
	"github.com/monshunter/xgoal/internal/planner"
)

func legacyReadyFixture(t *testing.T) (*Store, planner.Request) {
	s, r := planningFixture(t)
	ctx := context.Background()
	ev := EventInput{Type: "LegacyCreated", ActorType: "user", Payload: map[string]any{}}
	if err := s.CreateGoal(ctx, domain.Goal{ID: r.GoalID, State: domain.GoalDraft, Version: 1}, ev); err != nil {
		t.Fatal(err)
	}
	compiled, err := compilePlanning(r, planningProposal())
	if err != nil {
		t.Fatal(err)
	}
	revisionID, planID := r.GoalID+"_revision_1", r.GoalID+"_plan_1"
	_, err = s.FreezeGoalRevision(ctx, GoalRevisionDraft{ID: revisionID, GoalID: r.GoalID, Revision: 1, RawGoal: r.RawGoal, Contract: map[string]any{"protocol_version": goalcompile.ContractVersion, "contract": compiled.Contract, "config_hash": r.ConfigHash, "created_by": r.CreatedBy, "mode": r.Mode}}, 1, EventInput{Type: "GoalRevisionFrozen", ActorType: "planner", Payload: map[string]any{"proposal_hash": compiled.ContractHash}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreatePlanRevision(ctx, PlanRevisionDraft{ID: planID, GoalRevisionID: revisionID, Revision: 1, WorkItems: compiled.WorkItems, Dependencies: compiled.Dependencies}, EventInput{Type: "PlanRevisionCreated", ActorType: "planner", Payload: map[string]any{"proposal_hash": compiled.PlanHash}})
	if err != nil {
		t.Fatal(err)
	}
	return s, r
}

func TestLegacyReadyActivates(t *testing.T) {
	s, r := legacyReadyFixture(t)
	ctx := context.Background()
	if err := s.RecoverPlanning(ctx, PlanningRecoveryOptions{CurrentConfigHash: r.ConfigHash}); err != nil {
		t.Fatal(err)
	}
	goal, _ := s.Goal(ctx, r.GoalID)
	work, err := s.NextReadyWork(ctx, r.GoalID)
	if goal.State != domain.GoalRunning || err != nil {
		t.Fatalf("goal=%+v work=%+v error=%v", goal, work, err)
	}
	if err := s.RecoverPlanning(ctx, PlanningRecoveryOptions{CurrentConfigHash: r.ConfigHash}); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyReadyConfigGateCleared(t *testing.T) {
	s, r := legacyReadyFixture(t)
	ctx := context.Background()
	if err := s.RecoverPlanning(ctx, PlanningRecoveryOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverPlanning(ctx, PlanningRecoveryOptions{CurrentConfigHash: r.ConfigHash}); err != nil {
		t.Fatal(err)
	}
	goal, _ := s.Goal(ctx, r.GoalID)
	gates, _ := s.Gates(ctx, r.GoalID, true)
	work, err := s.NextReadyWork(ctx, r.GoalID)
	if len(gates) != 0 || err != nil {
		t.Fatalf("after configuration restored: goal=%+v open_gates=%+v work=%+v error=%v", goal, gates, work, err)
	}
}

func TestPausedCrashResumeDoesNotConsumeBudget(t *testing.T) {
	s, r := planningFixture(t)
	ctx := context.Background()
	p := beginFixturePlanning(t, s, acceptPlanning(t, s, r))
	paused, err := s.SetPlanningPaused(ctx, r.GoalID, p.Goal.Version, true, "pause invocation")
	if err != nil {
		t.Fatal(err)
	}
	// Daemon dies before FailPlanning can save planner_paused; startup process recovery has stopped all processes.
	if err := s.RecoverPlanning(ctx, PlanningRecoveryOptions{CurrentConfigHash: r.ConfigHash, NoProgressLimit: 1}); err != nil {
		t.Fatal(err)
	}
	_, err = s.SetPlanningPaused(ctx, r.GoalID, paused.Goal.Version, false, "resume after restart")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverPlanning(ctx, PlanningRecoveryOptions{CurrentConfigHash: r.ConfigHash, NoProgressLimit: 1}); err != nil {
		t.Fatal(err)
	}
	p, err = s.Planning(ctx, r.GoalID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Generation != 2 || p.State != "QUEUED" {
		t.Fatalf("pause consumed crash recovery budget: generation=%d state=%s code=%s", p.Generation, p.State, p.Observation.FailureCode)
	}
}

func TestLegacyReadyRejectsUnboundDraftAfterFailedReplan(t *testing.T) {
	s, r := planningFixture(t)
	ctx := context.Background()
	ev := EventInput{Type: "GoalCreated", ActorType: "user", Payload: map[string]any{}}
	if err := s.CreateGoal(ctx, domain.Goal{ID: r.GoalID, State: domain.GoalDraft, Version: 1}, ev); err != nil {
		t.Fatal(err)
	}
	compiled, err := compilePlanning(r, planningProposal())
	if err != nil {
		t.Fatal(err)
	}
	revisionID, planID := r.GoalID+"_revision_1", r.GoalID+"_plan_1"
	_, err = s.FreezeGoalRevision(ctx, GoalRevisionDraft{ID: revisionID, GoalID: r.GoalID, Revision: 1, RawGoal: r.RawGoal, Contract: map[string]any{"protocol_version": goalcompile.ContractVersion, "contract": compiled.Contract, "config_hash": r.ConfigHash, "created_by": r.CreatedBy, "mode": r.Mode}}, 1, EventInput{Type: "GoalRevisionFrozen", ActorType: "planner", Payload: map[string]any{"proposal_hash": compiled.ContractHash}})
	if err != nil {
		t.Fatal(err)
	}
	// Old goal.create stopped after FreezeGoalRevision; goal.replan creates a
	// structurally valid draft and then rejects activation because Goal is READY.
	compiled.WorkItems[0].AcceptanceCriteria = []string{"AC-NOT-IN-CONTRACT"}
	plan, err := s.CreatePlanRevision(ctx, PlanRevisionDraft{ID: planID, GoalRevisionID: revisionID, Revision: 1, WorkItems: compiled.WorkItems, Dependencies: compiled.Dependencies}, EventInput{Type: "ReplanProposed", ActorType: "human", Payload: map[string]any{"reason": "attempted repair", "impact_analysis": "repair interrupted initial plan"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ActivateReplan(ctx, plan.ID, plan.Version, 2, EventInput{Type: "PlanReplaced", ActorType: "kernel", Payload: map[string]any{}})
	if err == nil {
		t.Fatal("expected existing goal.replan READY rejection")
	}
	if err := s.RecoverPlanning(ctx, PlanningRecoveryOptions{CurrentConfigHash: r.ConfigHash}); err != nil {
		t.Fatal(err)
	}
	goal, _ := s.Goal(ctx, r.GoalID)
	work, workErr := s.NextReadyWork(ctx, r.GoalID)
	if goal.State == domain.GoalRunning || workErr == nil {
		t.Fatalf("semantically invalid draft activated after failed replan: goal=%+v work=%+v error=%v", goal, work, workErr)
	}
}
