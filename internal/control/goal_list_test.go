package control_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/control"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestGoalListHTTPQuery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := sqlite.Open(ctx, filepath.Join(root, "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	service, err := control.New(s, root)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.Execute(ctx, api.Operation{Name: "goal.create", Body: raw(map[string]any{"goal_id": "goal_list", "raw_goal": "game API_KEY=private-value", "mode": "standard"})})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Events(ctx, "goal", "goal_list")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewHandler(service, s)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/goals", "/v1/goals?state=DRAFT&limit=1"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		var page sqlite.GoalPage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != 200 || len(page.Items) != 1 {
			t.Fatalf("%s: %d %s %v", path, response.Code, response.Body, err)
		}
		item := page.Items[0]
		if item.GoalID != "goal_list" || item.State != "DRAFT" || item.PlanningState != "WAITING" || item.Summary != "game API_KEY=[REDACTED]" {
			t.Fatalf("item=%+v", item)
		}
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=abc", "limit=", "state=FAILED", "after=bad%0A"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/goals?"+query, nil))
		if response.Code != 400 {
			t.Fatalf("%s accepted: %d %s", query, response.Code, response.Body)
		}
	}
	after, err := s.Events(ctx, "goal", "goal_list")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("query changed event history")
	}
}
