package control_test

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/control"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestGoalLifecycleThroughControlService(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), clock.NewFake(time.Date(2026, 9, 2, 22, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := control.New(store, root)
	if err != nil {
		t.Fatal(err)
	}
	status, response, err := service.Execute(ctx, api.Operation{Name: "goal.create", Body: raw(map[string]any{"goal_id": "goal_api", "raw_goal": "implement bounded change", "mode": "standard"})})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create status=%d response=%+v err=%v", status, response, err)
	}
	status, response, err = service.Query(ctx, api.Operation{Name: "goal.get", ResourceID: "goal_api"})
	if err != nil || status != http.StatusOK {
		t.Fatalf("get status=%d response=%+v err=%v", status, response, err)
	}
	events, err := service.Events(ctx, "goal_api", "", 10)
	if err != nil || len(events) != 1 || events[0].EventType != "GoalCreated" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestStrictRequestRejectsUnknownFields(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, _ := control.New(store, root)
	_, _, err = service.Execute(ctx, api.Operation{Name: "goal.create", Body: raw(map[string]any{"goal_id": "goal_api", "raw_goal": "x", "unexpected": true})})
	if err == nil {
		t.Fatal("unknown request field was accepted")
	}
}

func raw(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
