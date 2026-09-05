package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/planner"
)

func TestPlannerExecutionConfigAndInitialInputProvenance(t *testing.T) {
	f := newFixture(t, "planner")
	result := `{"protocol_version":"xgoal.planner-proposal/v1alpha1","contract":{"summary":"fixture plan"},"plan":{},"ambiguities":["fixture question"]}`
	message, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": result}})
	events := filepath.Join(filepath.Dir(f.runtimeRoot), "planner-events.jsonl")
	if err := os.WriteFile(events, append([]byte("{\"type\":\"thread.started\",\"thread_id\":\"planner-session\"}\n"), append(message, '\n')...), 0o600); err != nil {
		t.Fatal(err)
	}
	f.environment["XGOAL_PLANNER_EVENTS"] = events
	script := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" > \"$XGOAL_FIXTURE_ARGS\"\ncat > \"$XGOAL_FIXTURE_STDIN\"\ncat \"$XGOAL_PLANNER_EVENTS\"\n"
	if err := os.WriteFile(f.binaryPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := f.adapter(t)
	packetPath, hash, err := planner.Prepare(f.runtimeRoot, planner.Packet{ProtocolVersion: planner.PacketVersion, GoalID: "goal_planner", RawGoal: "bounded fixture", Mode: "standard", ConfigHash: strings.Repeat("a", 64), TrustedValidators: []string{"fixture"}, ProjectRoot: f.projectRoot, ProjectNetwork: "deny", ProjectSecrets: "deny"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := (config.Agent{ID: "codex-planner", Adapter: "codex-cli", Roles: []string{"planner"}, Model: "fixture-plan-model", ReasoningEffort: "medium"}).Effective("planner", "fixture-version")
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := planner.Schema()
	in := planner.Invocation{InvocationID: "planner_config", ProfileID: e.ProfileID, ExecutionConfig: &e, RequestHash: strings.Repeat("b", 64), InputTree: strings.Repeat("c", 40), Generation: 2, WorkDir: f.projectRoot, PacketPath: packetPath, PacketHash: hash, Prompt: "plan the fixture", OutputSchema: schema, Environment: f.environment, Timeout: 30 * time.Second, MaxOutputBytes: 4 << 20}
	out, err := runtime.Plan(context.Background(), in, nil)
	if err != nil || out.SessionID != "planner-session" {
		t.Fatalf("planner %+v %v", out, err)
	}
	argv := mustRead(t, f.argumentsPath)
	for _, want := range []string{"developer_instructions=", "Kernel owns", "--sandbox read-only", "--model fixture-plan-model", `model_reasoning_effort="medium"`} {
		if !strings.Contains(argv, want) {
			t.Fatalf("missing %s in %s", want, argv)
		}
	}
	record := mustRead(t, filepath.Join(f.runtimeRoot, "adapters", "codex", "plans", in.InvocationID, "invocation.json"))
	for _, want := range []string{`"generation":2`, `"request_hash":"` + in.RequestHash + `"`, `"input_tree":"` + in.InputTree + `"`, `"model":"fixture-plan-model"`} {
		if !strings.Contains(record, want) {
			t.Fatalf("missing %s in %s", want, record)
		}
	}
	if strings.Contains(record, "goal_revision") {
		t.Fatal("initial Planner invented a Goal Revision")
	}
}
