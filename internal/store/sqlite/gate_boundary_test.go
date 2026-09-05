package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/completion"
	"github.com/monshunter/xgoal/internal/domain"
)

func TestRequiredGateDecisionAndExpiryFenceSchedulingAndCompletion(t *testing.T) {
	for _, scenario := range []string{"open", "denied", "revoked", "expired", "approved", "consumed-expired"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			source := clock.NewFake(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
			state, err := Open(ctx, filepath.Join(t.TempDir(), "state"), source)
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			goal, work := seedReadyWork(t, state, "gate_work")
			event := EventInput{Type: "GateObserved", ActorType: "human", Payload: map[string]any{}}
			gate, err := state.CreateGate(ctx, GateDraft{ID: "gate", GoalID: goal.ID, WorkItemID: work.ID, ReasonCode: "agent_blocked", Facts: map[string]any{}, Unknowns: []string{"choose"}, Options: []string{"allow", "deny"}, Recommendation: "choose", Action: domain.ActionExecCommand, Scope: []string{"goal:" + goal.ID}, ExpiresAt: source.Now().Add(time.Minute), MaxUses: 1, Revocable: true, Required: true}, event)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "open" {
				decision := domain.GateAllow
				if scenario == "denied" {
					decision = domain.GateDeny
				}
				gate, err = state.DecideGate(ctx, gate.ID, gate.Version, decision, "user", "chosen", event)
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "revoked" {
				if _, err := state.RevokeGate(ctx, gate.ID, gate.Version, "user", "withdrawn", event); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "consumed-expired" {
				if _, err := state.ConsumeAuthorization(ctx, gate.ID, AuthorizationRequest{GoalID: goal.ID, WorkItemID: work.ID, Action: gate.Action, Scope: gate.Scope}, event); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "expired" || scenario == "consumed-expired" {
				source.Advance(2 * time.Minute)
			}
			allowed := scenario == "approved" || scenario == "consumed-expired"
			if _, err := state.NextReadyWork(ctx, goal.ID); (err == nil) != allowed {
				t.Fatalf("scheduler allowed=%v err=%v", allowed, err)
			}
			if !allowed {
				if _, err := state.ClaimWork(ctx, work.ID, work.Version, LeaseDraft{ID: "lease", Holder: "kernel", TTL: time.Minute}, domain.Attempt{ID: "attempt", WorkItemID: work.ID, AgentProfileID: "fixture", State: domain.AttemptCreated, BaseTree: "base", PacketHash: "packet", Version: 1}, event); err == nil {
					t.Fatal("claim bypassed required decision")
				}
			}
			if err := state.UpdateGoalState(ctx, goal.ID, goal.Version, domain.GoalVerifying, event); err != nil {
				t.Fatal(err)
			}
			goal.Version++
			if _, err := state.SetCompletionFacts(ctx, goal.ID, CompletionFacts{IntegrationTree: "tree", ExpectedTree: "tree", FinalEvidenceSetID: "set", FinalReportHash: "report", Criteria: []completion.CriterionStatus{{ID: "AC-1", Satisfied: true, Current: true, TreeHash: "tree"}}}, event); err != nil {
				t.Fatal(err)
			}
			result, err := state.CompleteGoal(ctx, goal.ID, goal.Version, event)
			if err != nil {
				t.Fatal(err)
			}
			gateBlocked := strings.Contains(strings.Join(result.Reasons, ";"), "required gates")
			if gateBlocked == allowed {
				t.Fatalf("completion gate allowed=%v reasons=%v", allowed, result.Reasons)
			}
		})
	}
}
