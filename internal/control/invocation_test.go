package control_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/control"
	"github.com/monshunter/xgoal/internal/domain"
	callindex "github.com/monshunter/xgoal/internal/invocation"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestInvocationContextAndCursorThroughControlAndHTTP(t *testing.T) {
	ctx := context.Background()
	project := t.TempDir()
	root := filepath.Join(project, "state")
	state, err := sqlite.Open(ctx, root, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	service, err := control.New(state, project)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.CreateGoal(ctx, domain.Goal{ID: "goal_logs", State: domain.GoalDraft, Version: 1}, sqlite.EventInput{Type: "GoalDrafted", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	packet := []byte(`{"goal_id":"goal_logs","input":"token=packet-sentinel"}`)
	os.Mkdir(filepath.Join(root, "packets"), 0700)
	os.WriteFile(filepath.Join(root, "packets", "input.json"), packet, 0400)
	dir := "adapters/codex/invocations/invoke_logs"
	input := callindex.Input{ID: "invoke_logs", GoalID: "goal_logs", OwnerKind: "attempt", OwnerID: "attempt_logs", Generation: 1, Role: "implementer", ProfileID: "codex", Provider: "codex-cli", GoalRevisionHash: "revision", InputTree: "tree", PacketPath: "packets/input.json", PacketHash: "packet", PacketSHA256: callindex.SHA256(packet), ProviderDir: dir, SchemaSHA256: strings.Repeat("a", 64), DelegationHash: "delegation", ExecutionConfig: config.ExecutionConfig{ProfileID: "codex", Provider: "codex-cli", Role: "implementer"}}
	if _, err := state.RegisterInvocation(ctx, input); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, dir, "events"), 0700)
	put := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, dir, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	metadata, _ := json.Marshal(map[string]any{"invocation_id": input.ID, "packet_hash": input.PacketHash, "delegation_hash": input.DelegationHash, "execution_config": input.ExecutionConfig, "profile_id": input.ProfileID, "role": input.Role, "attempt_id": input.OwnerID, "base_tree": input.InputTree, "goal_revision_hash": input.GoalRevisionHash, "plan_revision_hash": input.PlanRevisionHash, "unknown_private": "hidden sentinel"})
	put("invocation.json", string(metadata))
	put("events/000001.json", `{"type":"thread.started","thread_id":"session"}`)
	put("events/000002.json", `{"type":"item.completed","item":{"type":"agent_message","text":"public token=event-sentinel"},"unknown_private":"hidden sentinel"}`)
	status, response, err := service.Query(ctx, api.Operation{Name: "invocation.context", ResourceID: input.ID})
	if err != nil || status != 200 {
		t.Fatalf("context: %d %v", status, err)
	}
	data, _ := json.Marshal(response)
	if strings.Contains(string(data), "sentinel") || !strings.Contains(string(data), `"load_observation":"unknown"`) {
		t.Fatalf("context exposure: %s", data)
	}
	status, response, err = service.Query(ctx, api.Operation{Name: "invocation.logs", ResourceID: input.ID, Query: map[string]string{"after": "1"}})
	page, ok := response.(callindex.LogPage)
	if err != nil || status != 200 || !ok || len(page.Events) != 1 || page.Next != 2 || page.Complete {
		t.Fatalf("live page: %+v %v", response, err)
	}
	data, _ = json.Marshal(page)
	if strings.Contains(string(data), "sentinel") {
		t.Fatalf("event exposure: %s", data)
	}
	for _, query := range []map[string]string{{"after": "3"}, {"after": "-1"}, {"stream": "../secret"}, {"limit": "9999"}} {
		if _, _, err := service.Query(ctx, api.Operation{Name: "invocation.logs", ResourceID: input.ID, Query: query}); err == nil {
			t.Fatalf("accepted invalid cursor/stream: %v", query)
		}
	}
	if err := state.FinishInvocation(ctx, input.ID, "session", "completed", nil); err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewHandler(service, state)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	res, err := http.Get(server.URL + "/v1/invocations/invoke_logs/logs?watch=1&after=1")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("stream: %d %s %v", res.StatusCode, body, err)
	}
	var streamed callindex.LogPage
	if err := json.Unmarshal(body, &streamed); err != nil || streamed.Next != 2 || !streamed.Complete || len(streamed.Events) != 1 {
		t.Fatalf("stream cursor: %s %v", body, err)
	}
	put("result.json", `{"protocol_version":"xgoal.agent-result/v1alpha1","status":"token=error-sentinel","summary":"bad"}`)
	_, badResult, err := service.Query(ctx, api.Operation{Name: "invocation.context", ResourceID: input.ID})
	if err != nil {
		t.Fatal(err)
	}
	badJSON, _ := json.Marshal(badResult)
	if strings.Contains(string(badJSON), "error-sentinel") || !strings.Contains(string(badJSON), "artifact_errors") {
		t.Fatalf("error exposure: %s", badJSON)
	}
	// Missing data is an unavailable artifact, never an empty successful page.
	os.Remove(filepath.Join(root, dir, "events", "000002.json"))
	if _, _, err := service.Query(ctx, api.Operation{Name: "invocation.logs", ResourceID: input.ID, Query: map[string]string{"after": "1"}}); err == nil {
		t.Fatal("missing durable event returned success")
	}
	os.Chmod(filepath.Join(root, input.PacketPath), 0600)
	os.WriteFile(filepath.Join(root, input.PacketPath), []byte(`{"modified":true}`), 0600)
	if _, _, err := service.Query(ctx, api.Operation{Name: "invocation.context", ResourceID: input.ID}); err == nil {
		t.Fatal("changed Packet accepted")
	}
}

func TestTerminalInvocationGapCannotBecomeComplete(t *testing.T) {
	ctx := context.Background()
	project := t.TempDir()
	root := filepath.Join(project, "state")
	state, err := sqlite.Open(ctx, root, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	service, err := control.New(state, project)
	if err != nil {
		t.Fatal(err)
	}
	state.CreateGoal(ctx, domain.Goal{ID: "goal_gap", State: domain.GoalDraft, Version: 1}, sqlite.EventInput{Type: "GoalDrafted", ActorType: "kernel", Payload: map[string]any{}})
	input := callindex.Input{ID: "invoke_gap", GoalID: "goal_gap", OwnerKind: "attempt", OwnerID: "attempt", Generation: 1, Role: "implementer", ProfileID: "p", Provider: "codex-cli", GoalRevisionHash: "revision", InputTree: "tree", PacketPath: "packet.json", PacketHash: "packet", PacketSHA256: strings.Repeat("a", 64), ProviderDir: "adapters/codex/invocations/invoke_gap", SchemaSHA256: strings.Repeat("b", 64), DelegationHash: "delegation", ExecutionConfig: config.ExecutionConfig{ProfileID: "p", Provider: "codex-cli", Role: "implementer"}}
	if _, err := state.RegisterInvocation(ctx, input); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, input.ProviderDir, "events"), 0700)
	for _, name := range []string{"000001.json", "000003.json"} {
		os.WriteFile(filepath.Join(root, input.ProviderDir, "events", name), []byte(`{"type":"message","text":"public"}`), 0600)
	}
	if err := state.FinishInvocation(ctx, input.ID, "", "completed", nil); err != nil {
		t.Fatal(err)
	}
	for _, watch := range []string{"0", "1"} {
		handler, _ := api.NewHandler(service, state)
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/v1/invocations/invoke_gap/logs?watch="+watch, nil)
		handler.ServeHTTP(response, request)
		if response.Code != 503 || !strings.Contains(response.Body.String(), "gap") {
			t.Fatalf("gap completed: %d %s", response.Code, response.Body.String())
		}
	}
}
