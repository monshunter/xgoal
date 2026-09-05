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
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/planner"
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
  planner) printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"claude-session-1","structured_output":{"protocol_version":"xgoal.planner-proposal/v1alpha1","contract":{"summary":"fixture plan","rationale":"test","in_scope":["output.txt"],"out_of_scope":["production"],"constraints":["no push"],"acceptance_criteria":[{"id":"AC-1","statement":"output exists","validators":["fixture"],"human_acceptance":false}],"quality_attributes":["correctness"],"human_gates":["scope expansion"],"completion_policy":{"require_all_required_items":true,"require_no_blocking_findings":true,"require_final_validation":true}},"plan":{"summary":"one item","work_items":[{"client_key":"implement","title":"implement","objective":"create output","depends_on":[],"read_scope":["/**"],"write_scope":["/output.txt"],"acceptance_criteria":["AC-1"],"validators":["fixture"],"recommended_role":"implementer","required":true}]},"ambiguities":[]}}'; exit 0 ;;
esac
printf '%s\n' '{"type":"future.event","secret":"sk-super-secret-value"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"claude-session-1","usage":{"input_tokens":101,"output_tokens":17},"total_cost_usd":0.42,"structured_output":{"protocol_version":"xgoal.agent-result/v1alpha1","status":"completed","summary":"fixture done","changed_files_claimed":[],"checks_claimed":[],"blockers":[],"assumptions":[],"recommended_next_action":""}}'
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
	resultArtifact, err := os.ReadFile(filepath.Join(f.runtimeRoot, "adapters", "claude", "invocations", inv.InvocationID, "events", "000003.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(resultArtifact), `"usage"`) || strings.Contains(string(resultArtifact), "total_cost_usd") {
		t.Fatalf("persisted Claude artifact retained Provider model accounting: %s", resultArtifact)
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
	start = explicitInvocation(t, start)
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
	resume = explicitInvocation(t, resume)
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
	for _, field := range []string{"legacy", "inherited", "model", "effort", "version"} {
		changed := explicitInvocation(t, resume)
		changed.InvocationID = "invoke_rejected_" + field
		e := *changed.ExecutionConfig
		changed.ExecutionConfig = &e
		switch field {
		case "legacy":
			changed.ExecutionConfig = nil
		case "inherited":
			e.Model = ""
			e.ModelSource = "native-inheritance"
		case "model":
			e.Model = "different-model"
		case "effort":
			e.ReasoningEffort = "low"
		case "version":
			e.CLIVersion = "different-version"
		}
		if _, err := restarted.Resume(context.Background(), changed, session, nil); !errors.Is(err, baseadapter.ErrSessionMismatch) {
			t.Fatalf("%s resumed: %v", field, err)
		}
	}
	drifted := f.invocation(t, "invoke_drift", baseadapter.SessionResumeCompatible)
	drifted = explicitInvocation(t, drifted)
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

func TestActiveProbeRequiresTransportAndTimeout(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "valid")
	runtime := f.adapter(t)
	if _, err := runtime.Probe(context.Background(), baseadapter.ProbeSpec{Mode: baseadapter.ProbeActiveContract, ProfileID: "claude-fixture"}); err == nil {
		t.Fatal("active probe accepted missing transport and timeout")
	}
	capabilities, err := runtime.Probe(context.Background(), baseadapter.ProbeSpec{Mode: baseadapter.ProbeActiveContract, ProfileID: "claude-fixture", ProviderTransport: true, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.ProbeMode != baseadapter.ProbeActiveContract || capabilities.ProviderTransport != "available" || capabilities.ProbeRef == "" {
		t.Fatalf("active capabilities = %+v", capabilities)
	}
}

func TestPlannerUsesReadOnlyToolsAndStructuredContract(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "planner")
	runtime := f.adapter(t)
	packetPath, packetHash, err := planner.Prepare(f.runtimeRoot, planner.Packet{
		ProtocolVersion: planner.PacketVersion, GoalID: "goal_plan", RawGoal: "create output", Mode: "standard",
		ConfigHash: strings.Repeat("a", 64), TrustedValidators: []string{"fixture"}, ProjectRoot: f.projectRoot,
		ProjectNetwork: "deny", ProjectSecrets: "deny",
	})
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := planner.Schema()
	effective, err := (config.Agent{ID: "claude-planner", Adapter: "claude-cli", Roles: []string{"planner"}, Model: "fixture-plan-model", ReasoningEffort: "medium"}).Effective("planner", "fixture-version")
	if err != nil {
		t.Fatal(err)
	}
	execution, err := runtime.Plan(context.Background(), planner.Invocation{
		ExecutionConfig: &effective, RequestHash: strings.Repeat("b", 64), InputTree: strings.Repeat("c", 40), Generation: 1,
		InvocationID: "planner_fixture", ProfileID: "claude-planner", WorkDir: f.projectRoot,
		PacketPath: packetPath, PacketHash: packetHash, Prompt: "plan the fixture", OutputSchema: schema,
		Environment: map[string]string{"XGOAL_ARGS": f.argsPath, "XGOAL_STDIN": f.stdinPath, "XGOAL_MODE": f.mode}, Timeout: 30 * time.Second, MaxOutputBytes: 4 << 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Proposal.Contract.Summary != "fixture plan" || len(execution.Proposal.Plan.WorkItems) != 1 || execution.SessionID != "claude-session-1" {
		t.Fatalf("planner execution = %+v", execution)
	}
	arguments, _ := os.ReadFile(f.argsPath)
	if !strings.Contains(string(arguments), "--tools Glob,Grep,Read") || !strings.Contains(string(arguments), "--model fixture-plan-model --effort medium") || strings.Contains(string(arguments), "Edit") || strings.Contains(string(arguments), "Write") {
		t.Fatalf("planner tools are not read-only: %s", arguments)
	}
}

func explicitInvocation(t *testing.T, inv baseadapter.Invocation) baseadapter.Invocation {
	t.Helper()
	e, err := (config.Agent{ID: inv.ProfileID, Adapter: "claude-cli", Roles: []string{string(inv.Role)}, Model: "fixture-model", ReasoningEffort: "high"}).Effective(string(inv.Role), "2.1.235 (Claude Code)")
	if err != nil {
		t.Fatal(err)
	}
	inv.ExecutionConfig = &e
	inv.SandboxPolicy = e.Sandbox
	inv.PermissionMode = e.PermissionMode
	inv.ToolPolicy = e.Tools
	return inv
}

func TestExplicitExecutionConfigReachesProviderAndArtifacts(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "valid")
	runtime := f.adapter(t)
	invocation := explicitInvocation(t, f.invocation(t, "invoke_explicit", baseadapter.SessionFresh))
	handle, err := runtime.Start(context.Background(), invocation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Wait(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(f.argsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"--model fixture-model", "--effort high"} {
		if !strings.Contains(string(argv), value) {
			t.Fatalf("missing %q in argv %s", value, argv)
		}
	}
	artifact, err := os.ReadFile(filepath.Join(f.runtimeRoot, "adapters", "claude", "invocations", invocation.InvocationID, "invocation.json"))
	if err != nil || !strings.Contains(string(artifact), `"model":"fixture-model"`) || !strings.Contains(string(artifact), `"cli_version":"2.1.235 (Claude Code)"`) {
		t.Fatalf("effective identity artifact %s, %v", artifact, err)
	}
}

func TestExecutionConfigIsFrozenBeforeStartReturns(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "valid")
	release := filepath.Join(filepath.Dir(f.runtimeRoot), "release")
	script, err := os.ReadFile(f.binary)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), `read_input=$(cat)`, `while [ ! -f "$XGOAL_RELEASE" ]; do sleep 0.01; done
read_input=$(cat)`, 1))
	if err := os.WriteFile(f.binary, script, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := f.adapter(t)
	in := explicitInvocation(t, f.invocation(t, "invoke_frozen", baseadapter.SessionFresh))
	in.Environment["XGOAL_RELEASE"] = release
	handle, err := runtime.Start(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Cancel(context.Background(), handle) })
	in.ExecutionConfig.Model = "mutated-after-start"
	in.ExecutionConfig.ReasoningEffort = "low"
	if len(in.ExecutionConfig.Tools) > 0 {
		in.ExecutionConfig.Tools[0] = "Bash"
		in.ExecutionConfig.AllowedTools[0] = "Bash"
	}
	if err := os.WriteFile(release, []byte("continue"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Wait(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	session, err := runtime.SessionID(handle)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := readSession(runtime.root, session)
	if err != nil {
		t.Fatal(err)
	}
	e := binding.Invocation.ExecutionConfig
	if e == nil || e.Model != "fixture-model" || e.ReasoningEffort != "high" || strings.Contains(strings.Join(e.Tools, ","), "Bash") {
		t.Fatalf("mutable config changed executed identity: %+v", e)
	}
}
