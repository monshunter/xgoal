package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/reconcile"
	"github.com/monshunter/xgoal/internal/supervisor"
)

func TestAutomaticRetryRejectsUnsafeOrUnapprovedScenes(t *testing.T) {
	for _, name := range []string{"safe", "changed-tree", "stale-version", "wrong-config", "blocked", "repeat", "limit", "open-gate", "denied-gate", "unknown-process", "cancelled", "human-actor", "manual-safe", "manual-denied-gate", "manual-expired-gate", "manual-expired-retry", "manual-approved-gate", "manual-cancelled", "manual-unknown-process", "manual-changed-tree", "manual-stale-version"} {
		t.Run(name, func(t *testing.T) {
			manual := strings.HasPrefix(name, "manual-")
			scenario := strings.TrimPrefix(name, "manual-")
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "state")
			source := clock.NewFake(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
			state, err := Open(ctx, root, source)
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			goal, work := seedReadyWorkWithContract(t, state, "auto_work", map[string]any{"criteria": []any{"AC-1"}, "config_hash": "config"})
			revision, err := state.GoalRevision(ctx, goal.ActiveRevisionID)
			if err != nil {
				t.Fatal(err)
			}
			identity := testCheckoutIdentity(t)
			tree := strings.Repeat("e", 40)
			if _, err := state.AdmitCheckout(ctx, goal.ID, identity, identity.HeadTree); err != nil {
				t.Fatal(err)
			}
			event := EventInput{Type: "WorkAutomaticallyRetried", ActorType: "kernel", Payload: map[string]any{}}
			lease, err := state.ClaimWork(ctx, work.ID, work.Version, LeaseDraft{ID: "lease", Holder: "kernel", TTL: time.Minute}, domain.Attempt{ID: "attempt", WorkItemID: work.ID, AgentProfileID: "profile", State: domain.AttemptCreated, BaseTree: identity.HeadTree, PacketHash: strings.Repeat("a", 64), Version: 1}, event)
			if err != nil {
				t.Fatal(err)
			}
			work.Version++
			if err := state.UpdateAttemptStateWithLease(ctx, "attempt", 1, lease.ID, lease.Generation, domain.AttemptFailed, event); err != nil {
				t.Fatal(err)
			}
			if err := state.UpdateWorkState(ctx, work.ID, work.Version, domain.WorkReconciling, event); err != nil {
				t.Fatal(err)
			}
			work.Version++
			if _, err := state.ReleaseLease(ctx, lease.ID, lease.Generation, lease.Version, event); err != nil {
				t.Fatal(err)
			}
			if err := state.ObserveCheckoutFailure(ctx, goal.ID, work.ID, identity, tree); err != nil {
				t.Fatal(err)
			}
			failure := reconcile.Failure{Class: reconcile.AgentFailed, PrimaryError: "execution failed", ValidatorDefinitionHash: "none", BaseTree: identity.HeadTree, ResultTree: tree, GoalRevisionHash: revision.Hash, RelevantConfigHash: "config"}
			if scenario == "blocked" {
				failure.Class = reconcile.AgentBlocked
			}
			draft := FailureDraft{ID: "failure_1", GoalID: goal.ID, WorkItemID: work.ID, AttemptID: "attempt", Failure: failure, Strategy: "profile"}
			if _, err := state.RecordFailure(ctx, draft, event); err != nil {
				t.Fatal(err)
			}
			authorization := AutomaticRetry{FailureID: draft.ID, ConfigHash: "config", Limit: 1}
			switch scenario {
			case "changed-tree":
				tree = strings.Repeat("f", 40)
			case "stale-version":
				work.Version++
			case "wrong-config":
				authorization.ConfigHash = "other-config"
			case "human-actor":
				event.ActorType = "human"
			case "repeat":
				draft.ID = "failure_2"
				if _, err := state.RecordFailure(ctx, draft, event); err != nil {
					t.Fatal(err)
				}
				authorization.FailureID = draft.ID
			case "limit":
				if _, err := state.db.ExecContext(ctx, `UPDATE work_items SET auto_retry_count=1 WHERE id=?`, work.ID); err != nil {
					t.Fatal(err)
				}
			case "open-gate", "denied-gate", "expired-gate", "expired-retry", "approved-gate":
				reason := "agent_blocked"
				if scenario == "expired-retry" {
					reason = "checkout_retry_required"
				}
				gate, err := state.CreateGate(ctx, GateDraft{ID: "decision", GoalID: goal.ID, WorkItemID: work.ID, AttemptID: "attempt", ReasonCode: reason, Facts: map[string]any{}, Unknowns: []string{"condition"}, Options: []string{"allow", "deny"}, Recommendation: "decide", Action: domain.ActionExecCommand, Scope: []string{"work:" + work.ID}, ExpiresAt: source.Now().Add(time.Hour), MaxUses: 1, Required: true}, event)
				if err != nil {
					t.Fatal(err)
				}
				if scenario != "open-gate" && scenario != "expired-retry" {
					decision := domain.GateAllow
					if scenario == "denied-gate" {
						decision = domain.GateDeny
					}
					if _, err := state.DecideGate(ctx, gate.ID, gate.Version, decision, "user", "use local cache", EventInput{Type: "GateDecided", ActorType: "human", Payload: map[string]any{}}); err != nil {
						t.Fatal(err)
					}
				}
				if strings.HasPrefix(scenario, "expired-") {
					source.Advance(2 * time.Hour)
				}
			case "unknown-process":
				if err := state.BeginProcess(ctx, supervisor.ProcessIntent{ID: "pending_process", Owner: supervisor.Owner{Kind: "probe", ID: "probe_1", GoalID: goal.ID, Generation: 1}}); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				if err := state.UpdateGoalState(ctx, goal.ID, goal.Version, domain.GoalCancelled, event); err != nil {
					t.Fatal(err)
				}
			}
			if manual {
				event.ActorType = "human"
				_, err = state.RetryCheckoutWork(ctx, work.ID, work.Version, identity, tree, event)
			} else {
				_, err = state.AutomaticRetryCheckoutWork(ctx, work.ID, work.Version, identity, tree, authorization, event)
			}
			if (err == nil) != (scenario == "safe" || scenario == "approved-gate") {
				t.Fatalf("%s retry: %v", scenario, err)
			}
			var used int
			if err := state.db.QueryRowContext(ctx, `SELECT auto_retry_count FROM work_items WHERE id=?`, work.ID).Scan(&used); err != nil {
				t.Fatal(err)
			}
			want := 0
			if !manual && (scenario == "safe" || scenario == "limit") {
				want = 1
			}
			if used != want {
				t.Fatalf("rejected retry changed authorization count: %d want %d", used, want)
			}
			if scenario == "safe" {
				if err := state.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := Open(ctx, root, clock.Real{})
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				if err := reopened.db.QueryRowContext(ctx, `SELECT auto_retry_count FROM work_items WHERE id=?`, work.ID).Scan(&used); err != nil || used != want {
					t.Fatalf("retry limit lost on restart: %d %v", used, err)
				}
				gateEvents, err := reopened.Gates(ctx, goal.ID, false)
				if err != nil || len(gateEvents) != 0 {
					t.Fatal("automatic repair fabricated human authorization")
				}
			}
			if scenario == "approved-gate" {
				decisions, err := state.RetryDecisions(ctx, work.ID)
				if err != nil || len(decisions) != 1 || decisions[0].Answer != "use local cache" || decisions[0].GateVersion != 3 {
					t.Fatalf("answer was not consumed and projected: %+v %v", decisions, err)
				}
			}
		})
	}
}
