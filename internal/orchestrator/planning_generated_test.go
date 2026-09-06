package orchestrator

import (
	"context"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/planner"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/validationplan"
	"strings"
	"testing"
)

func TestGeneratedAcceptancePolicyAndExactApprovalResume(t *testing.T) {
	for _, policy := range []string{"allow", "human-gate", "deny"} {
		t.Run(policy, func(t *testing.T) {
			ctx := context.Background()
			engine, request, provider := planningFixture(t)
			request.GeneratedValidationPolicy = policy
			if policy != "deny" {
				request.TrustedValidatorIDs = nil
				request.ValidationCapabilities = nil
			}
			if _, err := engine.store.RetryPlanning(ctx, request.GoalID, 1, request, "configure generation policy"); err != nil {
				t.Fatal(err)
			}
			provider.plan = func(context.Context, planner.Invocation) (planner.Execution, error) {
				p := planningProposal()
				p.Contract.GeneratedValidators = []validationplan.Generated{{ID: "behavior", Description: "output bytes", Runtime: "sh", Script: "test -s output.txt", TimeoutSeconds: 5}}
				p.Contract.AcceptanceCriteria[0].Validators = []string{"behavior"}
				p.Plan.WorkItems[0].Validators = []string{"behavior"}
				return planner.Execution{Proposal: p, SessionID: "planner"}, nil
			}
			if err := engine.runPlanning(ctx, request.GoalID); err != nil {
				t.Fatal(err)
			}
			p, _ := engine.store.Planning(ctx, request.GoalID)
			if policy == "allow" {
				if p.Goal.State != domain.GoalRunning {
					t.Fatalf("allow failed: %+v", p)
				}
				return
			}
			if p.State != "WAITING" || p.Goal.ActiveRevisionID != "" {
				t.Fatalf("policy bypassed: %+v", p)
			}
			if policy == "deny" {
				if !strings.Contains(p.Blocker, "denies") {
					t.Fatal(p.Blocker)
				}
				return
			}
			gates, err := engine.store.Gates(ctx, request.GoalID, true)
			if err != nil || len(gates) != 1 || gates[0].ReasonCode != "generated_validation_approval" {
				t.Fatalf("missing exact gate: %+v %v", gates, err)
			}
			prepared, preparedHash, err := engine.store.PreparedAcceptance(ctx, gates[0])
			if err != nil || preparedHash == "" || prepared.Contract.GeneratedValidators[0].Script != p.Observation.Proposal.Contract.GeneratedValidators[0].Script {
				t.Fatalf("approval cannot inspect exact proposal: %v", err)
			}
			g, err := engine.store.DecideGate(ctx, gates[0].ID, gates[0].Version, domain.GateAllow, "operator", "reviewed exact proposed checks", event("GateDecided", "human", map[string]any{}))
			if err != nil {
				t.Fatal(err)
			}
			c := sqlite.GateContinuation{GateID: g.ID, GateVersion: g.Version, OwnerVersion: p.Goal.Version}
			next, err := engine.store.ResumePlanningGate(ctx, p.Goal.ID, c, request.ConfigHash, p.Observation.CheckoutIdentity, p.Observation.InputTree)
			if err != nil || next.Request.Proposal == nil || next.Request.ApprovedValidationHash == "" {
				t.Fatalf("lost approved proposal: %+v %v", next, err)
			}
			if err := engine.runPlanning(ctx, request.GoalID); err != nil {
				t.Fatal(err)
			}
			p, _ = engine.store.Planning(ctx, request.GoalID)
			if p.Goal.State != domain.GoalRunning || provider.calls != 1 {
				t.Fatalf("approval regenerated or stalled: calls=%d %+v", provider.calls, p)
			}
			_, retainedHash, err := engine.store.PreparedAcceptance(ctx, gates[0])
			if err != nil || retainedHash != preparedHash {
				t.Fatalf("old Gate lost exact proposal after resume: %s %v", retainedHash, err)
			}
			if _, err := engine.store.ResumePlanningGate(ctx, p.Goal.ID, c, request.ConfigHash, next.Request.Prior.Observation.CheckoutIdentity, next.Request.Prior.Observation.InputTree); err == nil {
				t.Fatal("approval reused")
			}
		})
	}
}
