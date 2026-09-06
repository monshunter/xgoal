package control_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/control"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestAcceptGoalIsDurableReplayableAndSupportsDraftPause(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	configuration, err := os.ReadFile("../../xgoal.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), configuration, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	service, err := control.New(state, root)
	if err != nil {
		t.Fatal(err)
	}
	model := map[string]any{"goal_id": "goal_durable", "raw_goal": "do a bounded change"}
	operation := api.Operation{Name: "goal.create", Body: raw(model)}
	record, created, err := service.AcceptGoal(ctx, operation, "POST /v1/goals", "accept-once", model)
	if err != nil || !created || record.State != domain.IdempotencyCompleted || record.ResponseStatus != http.StatusCreated {
		t.Fatalf("accept=%+v created=%v err=%v", record, created, err)
	}
	replay, created, err := service.AcceptGoal(ctx, operation, "POST /v1/goals", "accept-once", model)
	if err != nil || created || string(replay.ResponseJSON) != string(record.ResponseJSON) {
		t.Fatalf("replay=%+v created=%v err=%v", replay, created, err)
	}
	var response map[string]any
	if err := json.Unmarshal(record.ResponseJSON, &response); err != nil {
		t.Fatal(err)
	}
	if response["planning_state"] != "QUEUED" || response["state"] != "DRAFT" {
		t.Fatalf("acceptance response=%v", response)
	}
	goal, _ := state.Goal(ctx, "goal_durable")
	for _, step := range []struct{ operation, state string }{{"goal.pause", "PAUSED"}, {"goal.resume", "QUEUED"}} {
		code, _, err := service.Execute(ctx, api.Operation{Name: step.operation, ResourceID: goal.ID, Body: raw(map[string]any{"expected_version": goal.Version, "reason": "operator intent"})})
		if err != nil || code != http.StatusOK {
			t.Fatalf("%s=%d,%v", step.operation, code, err)
		}
		_, view, err := service.Query(ctx, api.Operation{Name: "goal.get", ResourceID: goal.ID})
		if err != nil || view.(map[string]any)["planning_state"] != step.state {
			t.Fatalf("%s view=%v err=%v", step.operation, view, err)
		}
		goal, _ = state.Goal(ctx, goal.ID)
		if goal.State != domain.GoalDraft || goal.ActiveRevisionID != "" {
			t.Fatalf("draft control fabricated revision: %+v", goal)
		}
	}
}

func TestPlanningRetryRefreshesConfiguredAcceptanceButPreservesCLIInputs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	base, err := os.ReadFile("../../xgoal.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	writeConfig := func(file string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), append(append([]byte{}, base...), []byte("\nplanning:\n  acceptanceFiles: ["+file+"]\n")...), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig("missing.md")
	state, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	service, err := control.New(state, root)
	if err != nil {
		t.Fatal(err)
	}
	model := map[string]any{"goal_id": "goal_inputs", "raw_goal": "implement game", "acceptance_files": []string{"explicit.md"}}
	if _, _, err := service.AcceptGoal(ctx, api.Operation{Name: "goal.create", Body: raw(model)}, "POST /v1/goals", "inputs-once", model); err != nil {
		t.Fatal(err)
	}
	writeConfig("acceptance.md")
	restarted, err := control.New(state, root)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := state.Goal(ctx, "goal_inputs")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := restarted.Execute(ctx, api.Operation{Name: "goal.plan", ResourceID: goal.ID, Body: raw(map[string]any{"expected_version": goal.Version, "reason": "correct missing configured path"})}); err != nil {
		t.Fatal(err)
	}
	record, err := state.Planning(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := record.Request.AcceptanceFiles
	if len(got) != 2 || got[0] != "acceptance.md" || got[1] != "explicit.md" {
		t.Fatalf("stale configuration retained or CLI input lost: %v", got)
	}
}
