package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
)

func TestPassiveProbeAndFixtureContract(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t, "valid")
	runtime := fixture.adapter(t)
	capabilities, err := runtime.Probe(context.Background(), adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: "codex-fixture", Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.Version != "codex-cli 0.145.0" || !capabilities.StructuredOutput || !capabilities.StreamingEvents ||
		!capabilities.ResumeSession || capabilities.ToolAllowlist || capabilities.CredentialStatus != "available" ||
		capabilities.ProviderTransport != "unknown" {
		t.Fatalf("passive capabilities = %+v", capabilities)
	}
	arguments := mustRead(t, fixture.argumentsPath)
	if strings.Contains(arguments, "--output-schema") {
		t.Fatalf("passive probe started a model invocation: %s", arguments)
	}

	invocation := fixture.invocation(t, "invocation_start", adapter.SessionFresh)
	var mu sync.Mutex
	var events []protocol.AgentEvent
	handle, err := runtime.Start(context.Background(), invocation, func(event protocol.AgentEvent) error {
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
	if result.Status != protocol.ResultCompleted || result.Summary != "fixture done" || result.Authority() != domain.AuthorityClaim {
		t.Fatalf("fixture result = %+v", result)
	}
	sessionID, err := runtime.SessionID(handle)
	if err != nil || sessionID != "thread-fixture-1" {
		t.Fatalf("session id = %q, %v", sessionID, err)
	}
	mu.Lock()
	capturedEvents := append([]protocol.AgentEvent(nil), events...)
	mu.Unlock()
	if !eventTypesInclude(capturedEvents, "session", "turn", "unknown", "command", "file_change", "message", "usage", "result") {
		t.Fatalf("normalized events = %+v", capturedEvents)
	}
	if prompt := mustRead(t, fixture.stdinPath); prompt != invocation.Prompt {
		t.Fatalf("fixture stdin = %q, want %q", prompt, invocation.Prompt)
	}
	arguments = mustRead(t, fixture.argumentsPath)
	for _, required := range []string{"--ask-for-approval never", "--sandbox workspace-write", "--cd " + fixture.projectRoot, "exec --json", "--output-schema"} {
		if !strings.Contains(arguments, required) {
			t.Fatalf("fixture arguments missing %q: %s", required, arguments)
		}
	}
	invocationDir := filepath.Join(fixture.runtimeRoot, "adapters", "codex", "invocations", invocation.InvocationID)
	for _, filename := range []string{"invocation.json", "result.json", "stderr.log", filepath.Join("events", "000003.json")} {
		info, err := os.Lstat(filepath.Join(invocationDir, filename))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("artifact %s = %v, %v", filename, info, err)
		}
	}
	allArtifacts := readTree(t, invocationDir)
	for _, secret := range []string{"super-secret-value", "fixture-api-value"} {
		if strings.Contains(allArtifacts, secret) {
			t.Fatalf("persisted Codex artifacts contain %q", secret)
		}
	}
}

func TestActiveProbeRequiresBudgetAndPersistsObservedUsage(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t, "valid")
	runtime := fixture.adapter(t)
	if _, err := runtime.Probe(context.Background(), adapter.ProbeSpec{
		Mode: adapter.ProbeActiveContract, ProfileID: "codex-fixture", Timeout: 30 * time.Second,
	}); err == nil {
		t.Fatal("active probe accepted missing provider transport and wall-time budget")
	}
	if _, err := runtime.Probe(context.Background(), adapter.ProbeSpec{
		Mode: adapter.ProbeActiveContract, ProfileID: "codex-fixture", ProviderTransport: true,
		Timeout: 30 * time.Second, Budget: adapter.ProbeBudget{MaxWallTime: 30 * time.Second, MaxTokens: -1},
	}); err == nil {
		t.Fatal("active probe accepted a negative token budget")
	}
	capabilities, err := runtime.Probe(context.Background(), adapter.ProbeSpec{
		Mode: adapter.ProbeActiveContract, ProfileID: "codex-fixture", ProviderTransport: true,
		Timeout: 30 * time.Second, Budget: adapter.ProbeBudget{MaxWallTime: 30 * time.Second, MaxTokens: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.ProbeMode != adapter.ProbeActiveContract || capabilities.ProviderTransport != "available" ||
		capabilities.Usage == nil || capabilities.Usage.InputTokens == nil || *capabilities.Usage.InputTokens != 7 ||
		capabilities.CostReporting || capabilities.ProbeRef == "" {
		t.Fatalf("active capabilities = %+v", capabilities)
	}
	probePath := filepath.Join(fixture.runtimeRoot, "adapters", "codex", filepath.FromSlash(capabilities.ProbeRef))
	info, err := os.Lstat(probePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("probe artifact = %v, %v", info, err)
	}
}

func TestActiveProbeEnforcesReportedCostBudget(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t, "cost")
	runtime := fixture.adapter(t)
	_, err := runtime.Probe(context.Background(), adapter.ProbeSpec{
		Mode: adapter.ProbeActiveContract, ProfileID: "codex-fixture", ProviderTransport: true,
		Timeout: 30 * time.Second, Budget: adapter.ProbeBudget{MaxWallTime: 30 * time.Second, MaxCostMicros: 5},
	})
	if err == nil || !strings.Contains(err.Error(), "cost 10 micros exceeded budget 5") {
		t.Fatalf("active probe cost error = %v", err)
	}
}

func TestResumeRequiresExactPersistentSessionBinding(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t, "valid")
	first := fixture.adapter(t)
	start := fixture.invocation(t, "invocation_original", adapter.SessionFresh)
	handle, err := first.Start(context.Background(), start, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Wait(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	sessionID, err := first.SessionID(handle)
	if err != nil {
		t.Fatal(err)
	}

	restarted := fixture.adapter(t)
	resume := fixture.invocation(t, "invocation_resume", adapter.SessionResumeCompatible)
	resumed, err := restarted.Resume(context.Background(), resume, sessionID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := restarted.Wait(context.Background(), resumed); err != nil || result.Status != protocol.ResultCompleted {
		t.Fatalf("resumed result = %+v, %v", result, err)
	}
	arguments := mustRead(t, fixture.argumentsPath)
	if !strings.Contains(arguments, "exec resume --json") || !strings.Contains(arguments, sessionID+" -") {
		t.Fatalf("resume arguments = %s", arguments)
	}

	drifted := fixture.invocation(t, "invocation_drifted", adapter.SessionResumeCompatible)
	drifted.GoalRevisionHash = strings.Repeat("9", 64)
	if _, err := restarted.Resume(context.Background(), drifted, sessionID, nil); !errors.Is(err, adapter.ErrSessionMismatch) {
		t.Fatalf("Resume() drift error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.runtimeRoot, "adapters", "codex", "invocations", drifted.InvocationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drifted resume created invocation artifacts: %v", err)
	}
}

func TestInvalidOutputAndNonzeroExitFailClosed(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"truncated", "invalid-result", "exit-nonzero"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture := newFixture(t, mode)
			runtime := fixture.adapter(t)
			handle, err := runtime.Start(context.Background(), fixture.invocation(t, "invocation_"+mode, adapter.SessionFresh), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = runtime.Wait(context.Background(), handle)
			if mode == "exit-nonzero" {
				if err == nil || errors.Is(err, adapter.ErrInvalidOutput) {
					t.Fatalf("Wait() nonzero error = %v", err)
				}
			} else if !errors.Is(err, adapter.ErrInvalidOutput) {
				t.Fatalf("Wait() invalid output error = %v", err)
			}
			if _, statErr := os.Lstat(sessionFilename(filepath.Join(fixture.runtimeRoot, "adapters", "codex"), "thread-fixture-1")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed invocation persisted resumable session: %v", statErr)
			}
		})
	}
}

func TestCancelTerminatesFixtureInvocation(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t, "blocking")
	runtime := fixture.adapter(t)
	sessionSeen := make(chan struct{}, 1)
	handle, err := runtime.Start(context.Background(), fixture.invocation(t, "invocation_cancel", adapter.SessionFresh), func(event protocol.AgentEvent) error {
		if event.Type == "session" {
			select {
			case sessionSeen <- struct{}{}:
			default:
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-sessionSeen:
	case <-time.After(30 * time.Second):
		t.Fatal("fixture did not publish session event")
	}
	if err := runtime.Cancel(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Wait(context.Background(), handle); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() cancellation error = %v", err)
	}
}

func TestEventSinkFailureIsNotHiddenByInternalCancellation(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t, "valid")
	runtime := fixture.adapter(t)
	sinkFailure := errors.New("fixture sink rejected event")
	handle, err := runtime.Start(context.Background(), fixture.invocation(t, "invocation_sink_failure", adapter.SessionFresh), func(protocol.AgentEvent) error {
		return sinkFailure
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Wait(context.Background(), handle); !errors.Is(err, sinkFailure) {
		t.Fatalf("Wait() sink error = %v", err)
	}
}

func TestCodexOutputSchemaUsesStrictProviderSubset(t *testing.T) {
	t.Parallel()

	schema, err := protocol.Schema(protocol.SchemaAgentResult)
	if err != nil {
		t.Fatal(err)
	}
	providerSchema, hash, err := validateOutputSchema(schema)
	if err != nil || hash == "" {
		t.Fatalf("validateOutputSchema() = %q, %q, %v", providerSchema, hash, err)
	}
	model, err := decodeJSONModel(providerSchema)
	if err != nil {
		t.Fatal(err)
	}
	root := model.(map[string]any)
	if _, exists := root["$schema"]; exists {
		t.Fatal("provider schema retained $schema")
	}
	properties := root["properties"].(map[string]any)
	if len(root["required"].([]any)) != len(properties) {
		t.Fatalf("provider required = %v, properties = %v", root["required"], properties)
	}
	version := properties["protocol_version"].(map[string]any)
	status := properties["status"].(map[string]any)
	if version["type"] != "string" || status["type"] != "string" {
		t.Fatalf("provider const/enum types = %v/%v", version["type"], status["type"])
	}
	checks := properties["checks_claimed"].(map[string]any)["items"].(map[string]any)
	if len(checks["required"].([]any)) != len(checks["properties"].(map[string]any)) {
		t.Fatalf("nested provider required = %v", checks["required"])
	}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("a", 4095) + "界"
	truncated := truncate(value, 4096)
	if truncated != strings.Repeat("a", 4095) {
		t.Fatalf("truncate split UTF-8: %q", truncated[len(truncated)-4:])
	}
}

type fixtureRuntime struct {
	binaryPath    string
	projectRoot   string
	runtimeRoot   string
	argumentsPath string
	stdinPath     string
	environment   map[string]string
}

func newFixture(t *testing.T, mode string) fixtureRuntime {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Join(root, "project")
	if err := os.Mkdir(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(root, "codex-fixture")
	if err := os.WriteFile(binaryPath, []byte(fixtureScript), 0o700); err != nil {
		t.Fatal(err)
	}
	argumentsPath := filepath.Join(root, "arguments.log")
	stdinPath := filepath.Join(root, "stdin.log")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return fixtureRuntime{
		binaryPath: binaryPath, projectRoot: projectRoot, runtimeRoot: filepath.Join(root, "runtime"),
		argumentsPath: argumentsPath, stdinPath: stdinPath,
		environment: map[string]string{
			"PATH": os.Getenv("PATH"), "HOME": home,
			"XGOAL_FIXTURE_MODE": mode, "XGOAL_FIXTURE_ARGS": argumentsPath, "XGOAL_FIXTURE_STDIN": stdinPath,
		},
	}
}

func (fixture fixtureRuntime) adapter(t *testing.T) *Adapter {
	t.Helper()
	runtime, err := New(Config{
		Binary: fixture.binaryPath, RuntimeRoot: fixture.runtimeRoot, ProjectRoot: fixture.projectRoot,
		Environment: fixture.environment, Clock: clock.NewFake(time.Date(2026, 9, 2, 20, 0, 0, 0, time.UTC)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func (fixture fixtureRuntime) invocation(t *testing.T, id string, sessionPolicy adapter.SessionPolicy) adapter.Invocation {
	t.Helper()
	packet := protocol.WorkPacket{
		ProtocolVersion: protocol.WorkPacketVersion,
		Project:         protocol.PacketProject{Name: "fixture", BaseTree: strings.Repeat("a", 40), Workspace: fixture.projectRoot},
		Goal:            protocol.PacketGoal{ID: "goal_fixture", Revision: 1, Summary: "fixture goal", ContractHash: strings.Repeat("b", 64)},
		WorkItem: protocol.PacketWorkItem{
			ID: "work_fixture", Title: "fixture work", Objective: "exercise Codex adapter",
			ReadScope: []string{"/**"}, WriteScope: []string{"/**"}, AcceptanceCriteria: []string{"AC-FIXTURE"}, ValidatorIDs: []string{"fixture"},
		},
		Role:                 domain.RoleImplementer,
		Constraints:          protocol.PacketConstraints{ProjectNetwork: "deny", ProjectSecrets: "deny"},
		RequiredOutputSchema: protocol.AgentResultVersion,
	}
	packetHash, err := packet.Hash()
	if err != nil {
		t.Fatal(err)
	}
	packetContent, err := canonical.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	packetPath := filepath.Join(filepath.Dir(fixture.runtimeRoot), "packet.json")
	if _, err := os.Stat(packetPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(packetPath, packetContent, 0o400); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(packetPath, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := protocol.Schema(protocol.SchemaAgentResult)
	if err != nil {
		t.Fatal(err)
	}
	return adapter.Invocation{
		InvocationID: id, AttemptID: "attempt_fixture", WorkItemID: packet.WorkItem.ID, ProfileID: "codex-fixture",
		GoalRevisionHash: packet.Goal.ContractHash, PlanRevisionHash: strings.Repeat("c", 64), BaseTree: packet.Project.BaseTree,
		PacketHash: packetHash, Role: packet.Role, WorkDir: fixture.projectRoot, PacketPath: packetPath,
		Prompt: "Read the packet and return the required result.", OutputSchema: schema,
		Environment: fixture.environment, SandboxPolicy: "workspace-write", Timeout: 30 * time.Second,
		MaxOutputBytes: 1 << 20, SessionPolicy: sessionPolicy,
	}
}

func eventTypesInclude(events []protocol.AgentEvent, required ...string) bool {
	seen := make(map[string]bool, len(events))
	for _, event := range events {
		seen[event.Type] = true
	}
	for _, eventType := range required {
		if !seen[eventType] {
			return false
		}
	}
	return true
}

func mustRead(t *testing.T, filename string) string {
	t.Helper()
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func readTree(t *testing.T, root string) string {
	t.Helper()
	var result strings.Builder
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result.Write(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.String()
}

const fixtureScript = `#!/bin/sh
printf '%s' "$*" >> "$XGOAL_FIXTURE_ARGS"
printf '\n' >> "$XGOAL_FIXTURE_ARGS"

if [ "$1" = "--version" ]; then
  printf '%s\n' 'codex-cli 0.145.0'
  exit 0
fi
if [ "$1" = "exec" ] && [ "$2" = "--help" ]; then
  printf '%s\n' '--json --output-schema --sandbox workspace-write read-only'
  exit 0
fi
if [ "$1" = "exec" ] && [ "$2" = "resume" ] && [ "$3" = "--help" ]; then
  printf '%s\n' '--json --output-schema'
  exit 0
fi
if [ "$1" = "login" ] && [ "$2" = "status" ]; then
  printf '%s\n' 'Logged in using fixture'
  exit 0
fi

cat > "$XGOAL_FIXTURE_STDIN"
printf '%s\n' '{"type":"thread.started","thread_id":"thread-fixture-1"}'
if [ "$XGOAL_FIXTURE_MODE" = "blocking" ]; then
  sleep 60 &
  wait
  exit 0
fi
printf '%s\n' '{"type":"turn.started"}'
printf '%s\n' '{"type":"future.event","message":"Authorization: Bearer super-secret-value","ratio":0.5}'
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution","command":"printf API_TOKEN=fixture-api-value","exit_code":0}}'
printf '%s\n' '{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"result.txt","kind":"add"}]}}'
if [ "$XGOAL_FIXTURE_MODE" = "invalid-result" ]; then
  printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"status\":\"completed\",\"summary\":\"missing protocol\"}"}}'
else
  printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"protocol_version\":\"xgoal.agent-result/v1alpha1\",\"status\":\"completed\",\"summary\":\"fixture done\",\"changed_files_claimed\":[\"result.txt\"],\"checks_claimed\":[],\"blockers\":[],\"assumptions\":[],\"recommended_next_action\":\"validate\"}"}}'
fi
if [ "$XGOAL_FIXTURE_MODE" = "cost" ]; then
  printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":7,"output_tokens":3,"cost_micros":10,"currency":"USD"}}'
elif [ "$XGOAL_FIXTURE_MODE" = "truncated" ]; then
  printf '%s' '{"type":"turn.completed","usage":{"input_tokens":7,"output_tokens":3}}'
else
  printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":7,"output_tokens":3}}'
fi
printf '%s\n' 'Authorization: Bearer super-secret-value' >&2
if [ "$XGOAL_FIXTURE_MODE" = "exit-nonzero" ]; then
  exit 7
fi
exit 0
`
