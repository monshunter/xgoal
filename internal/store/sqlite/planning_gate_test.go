package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
)

func TestPlanningGateMigrationRequiresKernelResolutionProof(t *testing.T) {
	for _, scenario := range []string{"kernel-resolved", "approved-published", "approved-pending", "planning-human-revoked", "permission-human-revoked"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "state")
			source := clock.NewFake(time.Now().UTC())
			migrations, err := loadMigrations()
			if err != nil {
				t.Fatal(err)
			}
			old, err := openWithMigrations(ctx, root, source, migrations[:10])
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			goal, _ := seedReadyWork(t, old, "work")
			owner := "planning"
			if scenario == "permission-human-revoked" {
				owner = "permission"
			}
			gate := createPlanningBoundaryGate(t, old, goal.ID, owner)
			event := EventInput{Type: "HumanDecision", ActorType: "human", Payload: map[string]any{}}
			if scenario == "kernel-resolved" {
				// Exact v10 clearPlanningGatesTx behavior: no user decision exists.
				err := old.withTransaction(ctx, func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `UPDATE gates SET state='REVOKED',version=version+1 WHERE id=?`, gate.ID); err != nil {
						return err
					}
					return old.planningEvent(ctx, tx, "gate", gate.ID, "PlanningGateResolved", map[string]any{"reason": "planning published"})
				})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				gate, err = old.DecideGate(ctx, gate.ID, gate.Version, domain.GateAllow, "user", "approved input", event)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "planning-human-revoked" || scenario == "permission-human-revoked" {
					if _, err := old.RevokeGate(ctx, gate.ID, gate.Version, "user", "withdraw", event); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "approved-published" {
					if err := old.withTransaction(ctx, func(tx *sql.Tx) error {
						return old.planningEvent(ctx, tx, "goal", goal.ID, "PlanningPublished", map[string]any{"planning_generation": 1})
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := old.Gate(ctx, gate.ID)
			if err != nil {
				t.Fatal(err)
			}
			eventsBefore, err := old.Events(ctx, "gate", gate.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			source.Advance(2 * time.Hour)
			current, err := Open(ctx, root, source)
			if err != nil {
				t.Fatal(err)
			}
			defer current.Close()
			after, err := current.Gate(ctx, gate.ID)
			if err != nil {
				t.Fatal(err)
			}
			retired := scenario == "kernel-resolved" || scenario == "approved-published"
			before.Required = !retired
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("migration changed decision or failed to preserve barrier: %+v", after)
			}
			eventsAfter, err := current.Events(ctx, "gate", gate.ID)
			if err != nil || !reflect.DeepEqual(eventsBefore, eventsAfter) {
				t.Fatal("migration changed historical events")
			}
			if _, err := current.NextReadyWork(ctx, goal.ID); (err == nil) != retired {
				t.Fatalf("retired=%v scheduling=%v", retired, err)
			}
		})
	}
}

func TestPlanningPublicationRetiresOnlyValidDecisions(t *testing.T) {
	for _, scenario := range []string{"allow", "deny", "revoke", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			state, request := planningFixture(t)
			source := clock.NewFake(time.Now().UTC())
			state.source = source
			planning := observePlanning(t, state, request)
			gate := createPlanningBoundaryGate(t, state, planning.Goal.ID, "planning")
			decision := domain.GateAllow
			if scenario == "deny" {
				decision = domain.GateDeny
			}
			event := EventInput{Type: "HumanDecision", ActorType: "human", Payload: map[string]any{}}
			gate, err := state.DecideGate(ctx, gate.ID, gate.Version, decision, "user", "answer", event)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "revoke" {
				if _, err := state.RevokeGate(ctx, gate.ID, gate.Version, "user", "withdraw", event); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "expired" {
				source.Advance(2 * time.Hour)
			}
			_, err = state.PublishPlanning(ctx, publication(planning))
			if (err == nil) != (scenario == "allow") {
				t.Fatalf("%s publication=%v", scenario, err)
			}
			if scenario == "allow" {
				source.Advance(2 * time.Hour)
				if _, err := state.NextReadyWork(ctx, planning.Goal.ID); err != nil {
					t.Fatalf("resolved decision expired retroactively: %v", err)
				}
			} else {
				preserved, err := state.Gate(ctx, gate.ID)
				if err != nil || !preserved.Required {
					t.Fatal("rejected decision silently retired")
				}
			}
		})
	}
}

func createPlanningBoundaryGate(t *testing.T, state *Store, goalID, owner string) domain.Gate {
	t.Helper()
	gate, err := state.CreateGate(context.Background(), GateDraft{ID: "decision", GoalID: goalID, ReasonCode: "planning_input", Facts: map[string]any{"owner": owner, "generation": 1}, Unknowns: []any{}, Options: []string{"allow", "deny"}, Recommendation: "decide", Action: domain.ActionExecCommand, Scope: []string{"goal/" + goalID + "/planning"}, ExpiresAt: state.source.Now().Add(time.Hour), MaxUses: 1, Required: true, Revocable: true}, EventInput{Type: "GateOpened", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	return gate
}
