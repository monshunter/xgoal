package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	baseadapter "github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
)

type fixture struct {
	runtimeRoot, projectRoot, binary, argsPath, stdinPath, mode string
}

func newFixture(t *testing.T, mode string) fixture {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	project, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "claude")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then echo '2.1.235 (Claude Code)'; exit 0; fi
if [ "${1:-}" = "--help" ]; then echo '--print --output-format --json-schema --permission-mode --tools --allowedTools --resume'; exit 0; fi
if [ "${1:-}" = "auth" ] && [ "${2:-}" = "status" ]; then echo '{"loggedIn": true, "authMethod": "oauth_token"}'; exit 0; fi
printf '%s\n' "$*" > "$XGOAL_ARGS"
read_input=$(cat)
printf '%s' "$read_input" > "$XGOAL_STDIN"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"claude-session-1"}'
case "$XGOAL_MODE" in
  blocking) sleep 60 ;;
  truncated) printf '%s' '{"type":"result"'; exit 0 ;;
  invalid) printf '%s\n' '{"type":"result","is_error":false,"session_id":"claude-session-1","structured_output":{"protocol_version":"bad","status":"completed","summary":"bad"}}'; exit 0 ;;
  nonzero) exit 9 ;;
esac
printf '%s\n' '{"type":"future.event","secret":"sk-super-secret-value"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"claude-session-1","total_cost_usd":0.000010,"usage":{"input_tokens":7,"output_tokens":3},"structured_output":{"protocol_version":"xgoal.agent-result/v1alpha1","status":"completed","summary":"fixture done","changed_files_claimed":[],"checks_claimed":[],"blockers":[],"assumptions":[],"recommended_next_action":""}}'
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return fixture{runtimeRoot: filepath.Join(root, "runtime"), projectRoot: project, binary: binary, argsPath: filepath.Join(root, "args"), stdinPath: filepath.Join(root, "stdin"), mode: mode}
}

func (f fixture) adapter(t *testing.T) *Adapter {
	t.Helper()
	runtime, err := New(Config{Binary: f.binary, RuntimeRoot: f.runtimeRoot, ProjectRoot: f.projectRoot, Environment: map[string]string{"XGOAL_ARGS": f.argsPath, "XGOAL_STDIN": f.stdinPath, "XGOAL_MODE": f.mode}})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func (f fixture) invocation(t *testing.T, id string, policy baseadapter.SessionPolicy) baseadapter.Invocation {
	t.Helper()
	packet := protocol.WorkPacket{ProtocolVersion: protocol.WorkPacketVersion, Project: protocol.PacketProject{Name: "fixture", BaseTree: strings.Repeat("a", 40), Workspace: f.projectRoot}, Goal: protocol.PacketGoal{ID: "goal_fixture", Revision: 1, Summary: "fixture goal", ContractHash: strings.Repeat("b", 64)}, WorkItem: protocol.PacketWorkItem{ID: "work_fixture", Title: "fixture", Objective: "exercise adapter", ReadScope: []string{"/**"}, WriteScope: []string{"/output/**"}, AcceptanceCriteria: []string{"AC-1"}, ValidatorIDs: []string{"fixture"}}, Role: domain.RoleImplementer, Constraints: protocol.PacketConstraints{ProjectNetwork: "deny", ProjectSecrets: "deny"}, RequiredOutputSchema: protocol.AgentResultVersion}
	content, err := canonical.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	packetPath := filepath.Join(f.runtimeRoot, "packet.json")
	if err := os.MkdirAll(f.runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(packetPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(packetPath, content, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := packet.Hash()
	if err != nil {
		t.Fatal(err)
	}
	schema, err := protocol.Schema(protocol.SchemaAgentResult)
	if err != nil {
		t.Fatal(err)
	}
	return baseadapter.Invocation{InvocationID: id, AttemptID: "attempt_fixture", WorkItemID: packet.WorkItem.ID, ProfileID: "claude-implementer", GoalRevisionHash: packet.Goal.ContractHash, PlanRevisionHash: strings.Repeat("c", 64), BaseTree: packet.Project.BaseTree, PacketHash: hash, Role: domain.RoleImplementer, WorkDir: f.projectRoot, PacketPath: packetPath, Prompt: "do the bounded fixture work", OutputSchema: schema, Environment: map[string]string{"XGOAL_ARGS": f.argsPath, "XGOAL_STDIN": f.stdinPath, "XGOAL_MODE": f.mode}, PermissionMode: "dontAsk", ToolPolicy: []string{"Read", "Glob", "Grep", "Edit", "Write"}, Timeout: 30 * time.Second, MaxOutputBytes: 4 << 20, SessionPolicy: policy}
}

func TestPassiveProbeAndFixtureContract(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "valid")
	runtime := f.adapter(t)
	capabilities, err := runtime.Probe(context.Background(), baseadapter.ProbeSpec{Mode: baseadapter.ProbePassive, ProfileID: "claude-fixture", Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.Version != "2.1.235 (Claude Code)" || !capabilities.StructuredOutput || !capabilities.ToolAllowlist || capabilities.CredentialStatus != "available" {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	inv := f.invocation(t, "invoke_valid", baseadapter.SessionFresh)
	var mu sync.Mutex
	var events []protocol.AgentEvent
	handle, err := runtime.Start(context.Background(), inv, func(event protocol.AgentEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Wait(context.Background(), handle)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.ResultCompleted || result.Summary != "fixture done" {
		t.Fatalf("result = %+v", result)
	}
	session, err := runtime.SessionID(handle)
	if err != nil || session != "claude-session-1" {
		t.Fatalf("session = %q, %v", session, err)
	}
	arguments, err := os.ReadFile(f.argsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"-p --input-format text", "--output-format stream-json", "--json-schema", "--permission-mode dontAsk", "--tools Read,Glob,Grep,Edit,Write", "--allowedTools Read,Glob,Grep,Edit,Write"} {
		if !strings.Contains(string(arguments), required) {
			t.Fatalf("arguments missing %q: %s", required, arguments)
		}
	}
	prompt, err := os.ReadFile(f.stdinPath)
	if err != nil || string(prompt) != inv.Prompt {
		t.Fatalf("stdin = %q, %v", prompt, err)
	}
	artifacts, err := os.ReadFile(filepath.Join(f.runtimeRoot, "adapters", "claude", "invocations", inv.InvocationID, "events", "000002.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(artifacts), "super-secret-value") {
		t.Fatal("event artifact contains secret")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) < 3 || events[1].Type != "unknown" {
		t.Fatalf("events = %+v", events)
	}
}

func TestResumeRequiresExactBinding(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "valid")
	first := f.adapter(t)
	start := f.invocation(t, "invoke_first", baseadapter.SessionFresh)
	handle, err := first.Start(context.Background(), start, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Wait(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	session, _ := first.SessionID(handle)
	restarted := f.adapter(t)
	resume := f.invocation(t, "invoke_resume", baseadapter.SessionResumeCompatible)
	resumed, err := restarted.Resume(context.Background(), resume, session, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Wait(context.Background(), resumed); err != nil {
		t.Fatal(err)
	}
	arguments, _ := os.ReadFile(f.argsPath)
	if !strings.Contains(string(arguments), "--resume "+session) {
		t.Fatalf("resume arguments = %s", arguments)
	}
	drifted := f.invocation(t, "invoke_drift", baseadapter.SessionResumeCompatible)
	drifted.GoalRevisionHash = strings.Repeat("d", 64)
	if _, err := restarted.Resume(context.Background(), drifted, session, nil); !errors.Is(err, baseadapter.ErrSessionMismatch) {
		t.Fatalf("drift error = %v", err)
	}
}

func TestFailureCancellationAndPermissions(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"truncated", "invalid", "nonzero"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, mode)
			runtime := f.adapter(t)
			handle, err := runtime.Start(context.Background(), f.invocation(t, "invoke_"+mode, baseadapter.SessionFresh), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.Wait(context.Background(), handle); err == nil {
				t.Fatal("invalid fixture succeeded")
			}
			if _, err := os.Stat(sessionPath(filepath.Join(f.runtimeRoot, "adapters", "claude"), "claude-session-1")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed call persisted session: %v", err)
			}
		})
	}
	f := newFixture(t, "valid")
	runtime := f.adapter(t)
	invalid := f.invocation(t, "invoke_permission", baseadapter.SessionFresh)
	invalid.ToolPolicy = []string{"Read", "Bash"}
	if _, err := runtime.Start(context.Background(), invalid, nil); err == nil {
		t.Fatal("write-capable/unexpected tools accepted")
	}
	blocking := newFixture(t, "blocking")
	blockedRuntime := blocking.adapter(t)
	handle, err := blockedRuntime.Start(context.Background(), blocking.invocation(t, "invoke_cancel", baseadapter.SessionFresh), nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := blockedRuntime.Cancel(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if _, err := blockedRuntime.Wait(context.Background(), handle); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestActiveProbeBudget(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "valid")
	runtime := f.adapter(t)
	if _, err := runtime.Probe(context.Background(), baseadapter.ProbeSpec{Mode: baseadapter.ProbeActiveContract, ProfileID: "claude-fixture"}); err == nil {
		t.Fatal("active probe accepted missing budget")
	}
	capabilities, err := runtime.Probe(context.Background(), baseadapter.ProbeSpec{Mode: baseadapter.ProbeActiveContract, ProfileID: "claude-fixture", ProviderTransport: true, Timeout: 30 * time.Second, Budget: baseadapter.ProbeBudget{MaxWallTime: 30 * time.Second, MaxTokens: 20, MaxCostMicros: 20}})
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.Usage == nil || capabilities.Usage.CostMicros == nil || *capabilities.Usage.CostMicros != 10 {
		t.Fatalf("active usage = %+v", capabilities.Usage)
	}
}
