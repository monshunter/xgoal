package control_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/control"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestIdentifierPrefixesAreUniqueBeforeMutationAndKeepCAS(t *testing.T) {
	ctx := context.Background()
	project := t.TempDir()
	s, err := sqlite.Open(ctx, filepath.Join(project, "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	service, err := control.New(s, project)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"goal_alpha", "goal_alpine", "goal_beta"} {
		if err := s.CreateGoal(ctx, domain.Goal{ID: id, State: domain.GoalDraft, Version: 1}, sqlite.EventInput{Type: "GoalCreated", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	status, result, err := service.Query(ctx, api.Operation{Name: "identifiers", Query: map[string]string{"kind": "goal", "prefix": "goal_a"}})
	data, _ := json.Marshal(result)
	if err != nil || status != http.StatusOK || !strings.Contains(string(data), "goal_alpha") || !strings.Contains(string(data), "goal_alpine") || strings.Contains(string(data), "goal_beta") {
		t.Fatalf("list=%s status=%d err=%v", data, status, err)
	}
	_, _, err = service.Execute(ctx, api.Operation{Name: "goal.cancel", ResourceID: "goal_al", Body: json.RawMessage(`{"expected_version":1}`)})
	var detail *api.APIError
	if !errors.As(err, &detail) || detail.Code != "AMBIGUOUS_ID" || !strings.Contains(detail.Message, "goal_alpha") {
		t.Fatalf("ambiguous=%v", err)
	}
	for _, id := range []string{"goal_alpha", "goal_alpine"} {
		goal, _ := s.Goal(ctx, id)
		if goal.Version != 1 || goal.State != domain.GoalDraft {
			t.Fatalf("ambiguous mutation changed %+v", goal)
		}
	}
	status, _, err = service.Execute(ctx, api.Operation{Name: "goal.cancel", ResourceID: "goal_b", Body: json.RawMessage(`{"expected_version":1}`)})
	if err != nil || status != http.StatusOK {
		t.Fatalf("unique mutation=%d %v", status, err)
	}
	goal, _ := s.Goal(ctx, "goal_beta")
	if goal.State != domain.GoalCancelled || goal.Version != 2 {
		t.Fatalf("resolved=%+v", goal)
	}
	if _, _, err := service.Execute(ctx, api.Operation{Name: "goal.cancel", ResourceID: "goal_b", Body: json.RawMessage(`{"expected_version":1}`)}); err == nil {
		t.Fatal("prefix bypassed CAS")
	}
	handler, err := api.NewHandler(service, s)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/goals/goal_al/cancel", strings.NewReader(`{"expected_version":1}`))
	request.Header.Set("Idempotency-Key", "ambiguous")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("HTTP ambiguity=%d %s", response.Code, response.Body)
	}
	if _, err := s.LookupIdempotentRequest(ctx, "POST /v1/goals/goal_al/cancel", "ambiguous", map[string]any{"expected_version": 1}); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("ambiguous request wrote idempotency state: %v", err)
	}
	create := func(id string) {
		t.Helper()
		if err := s.CreateGoal(ctx, domain.Goal{ID: id, State: domain.GoalDraft, Version: 1}, sqlite.EventInput{Type: "GoalCreated", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	post := func(path, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"expected_version":1}`))
		req.Header.Set("Idempotency-Key", key)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	create("goal_al")
	if out := post("/v1/goals/goal_al/cancel", "exact"); out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"goal_id":"goal_al"`) {
		t.Fatalf("exact preference=%d %s", out.Code, out.Body)
	}
	create("goal_gamma")
	first := post("/v1/goals/goal_g/cancel", "replay")
	if first.Code != http.StatusOK {
		t.Fatalf("first=%d %s", first.Code, first.Body)
	}
	create("goal_garden")
	replay := post("/v1/goals/goal_g/cancel", "replay")
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() || replay.Header().Get("Idempotent-Replay") != "true" {
		t.Fatalf("prefix replay=%d %s", replay.Code, replay.Body)
	}
}
