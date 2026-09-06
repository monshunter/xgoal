package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/acceptance"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestAcceptanceGateContinuationPreservesHistoricalClaimAndRequiresFreshInvocation(t *testing.T) {
	ctx := context.Background()
	s, r := acceptanceFixture(t)
	e, _, err := s.BeginAcceptance(ctx, r, acceptanceEvent())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGoalState(ctx, r.Packet.GoalID, r.GoalVersion, domain.GoalWaiting, acceptanceEvent()); err != nil {
		t.Fatal(err)
	}
	ended, valid, err := s.ObserveAcceptance(ctx, e.ID, e.Version, e.RequestHash, r.Packet.ID, acceptance.Observation{Result: &protocol.AgentResult{ProtocolVersion: protocol.AgentResultVersion, Status: protocol.ResultBlocked, Summary: "late account question", Blockers: []string{"account?"}}, ExecutionStopped: true}, false, acceptanceEvent())
	if err != nil || valid {
		t.Fatalf("late result current=%v %v", valid, err)
	}
	var old acceptance.Observation
	if err := json.Unmarshal(ended.ObservationJSON, &old); err != nil || !old.Historical {
		t.Fatalf("missing historical observation: %+v %v", old, err)
	}
	goal, _ := s.Goal(ctx, r.Packet.GoalID)
	if err := s.UpdateGoalState(ctx, goal.ID, goal.Version, domain.GoalRunning, acceptanceEvent()); err != nil {
		t.Fatal(err)
	}
	goal, _ = s.Goal(ctx, goal.ID)
	if err := s.UpdateGoalState(ctx, goal.ID, goal.Version, domain.GoalVerifying, acceptanceEvent()); err != nil {
		t.Fatal(err)
	}
	if err := s.RequireAcceptanceReplay(ctx, ended.ID); err != nil {
		t.Fatal(err)
	}
	gate, err := s.Gate(ctx, "gate_"+ended.ID)
	if err != nil {
		t.Fatal(err)
	}
	gate, err = s.DecideGate(ctx, gate.ID, gate.Version, domain.GateAllow, "human", "use fixture account", acceptanceEvent())
	if err != nil {
		t.Fatal(err)
	}
	goal, _ = s.Goal(ctx, goal.ID)
	checkout, err := s.Checkout(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := GateContinuation{GateID: gate.ID, GateVersion: gate.Version, OwnerVersion: goal.Version}
	if _, err := s.ResumeAcceptanceGate(ctx, goal.ID, c, r.Packet.ConfigHash, checkout.Identity, strings.Repeat("c", 40)); !errors.Is(err, ErrCheckoutConflict) {
		t.Fatalf("changed scene=%v", err)
	}
	goal, err = s.ResumeAcceptanceGate(ctx, goal.ID, c, r.Packet.ConfigHash, checkout.Identity, checkout.AcceptedTree)
	if err != nil {
		t.Fatal(err)
	}
	if gate, err := s.Gate(ctx, gate.ID); err != nil || gate.Used != 0 {
		t.Fatalf("resume consumed without new invocation: %+v %v", gate, err)
	}
	if err := s.UpdateGoalState(ctx, goal.ID, goal.Version, domain.GoalVerifying, acceptanceEvent()); err != nil {
		t.Fatal(err)
	}
	goal, _ = s.Goal(ctx, goal.ID)
	next := r
	next.GoalVersion = goal.Version
	next.PreviousInvocationID = r.Packet.ID
	next.Packet.ID = "accept_after_pause"
	next.Packet.Prior = &acceptance.PriorContext{InvocationID: r.Packet.ID, ObservationHash: ended.ObservationHash, Observation: old}
	next.Packet.Decisions = []protocol.PacketDecision{{GateID: gate.ID, GateVersion: gate.Version, Answer: gate.DecisionReason}}
	next = prepareAcceptanceRequest(t, s, next)
	fresh, created, err := s.BeginAcceptance(ctx, next, acceptanceEvent())
	if err != nil || !created || fresh.ID == ended.ID {
		t.Fatalf("fresh invocation=%+v %v %v", fresh, created, err)
	}
	oldEffect, err := s.Effect(ctx, ended.ID)
	if err != nil || string(oldEffect.ObservationJSON) != string(ended.ObservationJSON) {
		t.Fatalf("historical claim rewritten: %v", err)
	}
	consumed, err := s.Gate(ctx, gate.ID)
	if err != nil || consumed.Used != 1 {
		t.Fatalf("answer not consumed once: %+v %v", consumed, err)
	}
}

func TestPlanningGateContinuationBindsAnswerFailureAndScene(t *testing.T) {
	ctx := context.Background()
	s, r := planningFixture(t)
	p := observePlanning(t, s, r)
	p, err := s.FailPlanning(ctx, PlanningFailure{GoalID: r.GoalID, EffectID: p.Effect.ID, Generation: p.Generation, ExpectedEffectVersion: p.Effect.Version, Code: "planner_failed", Reason: "which locale should be used?", ExecutionStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := s.Gate(ctx, "planning_gate_"+r.GoalID+"_1_planner_failed")
	if err != nil {
		t.Fatal(err)
	}
	gate, err = s.DecideGate(ctx, gate.ID, gate.Version, domain.GateAllow, "operator", "use zh-CN", EventInput{Type: "GateDecided", ActorType: "human", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	c := GateContinuation{GateID: gate.ID, GateVersion: gate.Version, OwnerVersion: p.Goal.Version}
	for _, bad := range []GateContinuation{{GateID: gate.ID, GateVersion: gate.Version - 1, OwnerVersion: p.Goal.Version}, {GateID: gate.ID, GateVersion: gate.Version, OwnerVersion: p.Goal.Version + 1}} {
		if _, err := s.ResumePlanningGate(ctx, r.GoalID, bad, r.ConfigHash, p.Observation.CheckoutIdentity, p.Observation.InputTree); !errors.Is(err, basestore.ErrConflict) {
			t.Fatalf("stale CAS=%v", err)
		}
	}
	if _, err := s.ResumePlanningGate(ctx, r.GoalID, c, r.ConfigHash, p.Observation.CheckoutIdentity, strings.Repeat("c", 40)); !errors.Is(err, ErrCheckoutConflict) {
		t.Fatalf("changed scene=%v", err)
	}
	before, _ := s.Gate(ctx, gate.ID)
	if before.Used != 0 || before.Version != gate.Version {
		t.Fatalf("failed continuation consumed %+v", before)
	}
	permission := func(id string) domain.Gate {
		t.Helper()
		g, err := s.CreateGate(ctx, GateDraft{ID: id, GoalID: r.GoalID, ReasonCode: "project_permission", Facts: map[string]any{}, Unknowns: []any{}, Options: []string{"allow", "deny"}, Recommendation: "inspect permission", Action: domain.ActionAccessProjectNetwork, Scope: []string{"example.invalid"}, ExpiresAt: s.source.Now().Add(time.Hour), MaxUses: 1, Required: true}, acceptanceEvent())
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	firstPermission := permission("independent_permission")
	if _, err := s.ResumePlanningGate(ctx, r.GoalID, c, r.ConfigHash, p.Observation.CheckoutIdentity, p.Observation.InputTree); !errors.Is(err, basestore.ErrAuthorizationDenied) {
		t.Fatalf("open permission Gate bypassed: %v", err)
	}
	if after, _ := s.Gate(ctx, gate.ID); after.Used != 0 || after.Version != gate.Version {
		t.Fatal("blocked resume consumed answer")
	}
	if _, err := s.DecideGate(ctx, firstPermission.ID, firstPermission.Version, domain.GateAllow, "human", "permit configured endpoint", acceptanceEvent()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_next_planning BEFORE INSERT ON effects BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResumePlanningGate(ctx, r.GoalID, c, r.ConfigHash, p.Observation.CheckoutIdentity, p.Observation.InputTree); err == nil {
		t.Fatal("injected commit succeeded")
	}
	if after, _ := s.Gate(ctx, gate.ID); after.Used != 0 || after.Version != gate.Version {
		t.Fatal("failed insert consumed answer")
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_next_planning`); err != nil {
		t.Fatal(err)
	}
	next, err := s.ResumePlanningGate(ctx, r.GoalID, c, r.ConfigHash, p.Observation.CheckoutIdentity, p.Observation.InputTree)
	if err != nil {
		t.Fatal(err)
	}
	prior := next.Request.Prior
	if next.Generation != 2 || prior == nil || prior.EffectID != p.Effect.ID || prior.RequestHash != p.Effect.RequestHash || prior.Observation.FailureReason != "which locale should be used?" || prior.Decision.Answer != "use zh-CN" || prior.Decision.GateVersion != gate.Version {
		t.Fatalf("missing bound prior: %+v", next.Request)
	}
	consumed, _ := s.Gate(ctx, gate.ID)
	if consumed.Used != 1 || consumed.Required {
		t.Fatalf("consumption=%+v", consumed)
	}
	if _, err := s.ResumePlanningGate(ctx, r.GoalID, c, r.ConfigHash, p.Observation.CheckoutIdentity, p.Observation.InputTree); err == nil {
		t.Fatal("replayed continuation")
	}
	claim := PlanningClaim{GoalID: r.GoalID, EffectID: next.Effect.ID, Generation: next.Generation, ExpectedGoalVersion: next.Goal.Version, ExpectedEffectVersion: next.Effect.Version, CurrentConfigHash: r.ConfigHash, InvocationID: "next", InputTree: strings.Repeat("c", 40), CheckoutIdentity: p.Observation.CheckoutIdentity}
	if _, err := s.BeginPlanning(ctx, claim); !errors.Is(err, ErrCheckoutConflict) {
		t.Fatalf("changed tree reached provider: %v", err)
	}
	claim.InputTree = p.Observation.InputTree
	latePermission := permission("late_permission")
	if _, err := s.BeginPlanning(ctx, claim); !errors.Is(err, basestore.ErrAuthorizationDenied) {
		t.Fatalf("late permission Gate bypassed: %v", err)
	}
	if _, err := s.DecideGate(ctx, latePermission.ID, latePermission.Version, domain.GateAllow, "human", "permit configured endpoint", acceptanceEvent()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginPlanning(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverPlanning(ctx, PlanningRecoveryOptions{CurrentConfigHash: r.ConfigHash, NoProgressLimit: 3}); err != nil {
		t.Fatal(err)
	}
	after, err := s.Planning(ctx, r.GoalID)
	if err != nil || after.Generation != 2 || after.State != "WAITING" {
		t.Fatalf("finite answer replayed on crash: %+v %v", after, err)
	}
}
