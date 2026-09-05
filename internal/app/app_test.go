package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/app"
	"github.com/monshunter/xgoal/internal/domain"
)

func TestRealUnixAPIWritesAndReplaysGoal(t *testing.T) {
	temporary, err := os.MkdirTemp("/tmp", "xgoal-app-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	appGit(t, temporary, "init", "-b", "main")
	paths, err := app.ResolvePaths(temporary, filepath.Join(temporary, "state"), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- app.Serve(ctx, paths) }()
	waitForServeSocket(t, paths.SocketPath, result)
	client, err := api.NewProjectClient(paths.SocketPath, time.Second, app.ExpectedIdentity(paths))
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"goal_id": "goal_runtime", "raw_goal": "bounded goal", "mode": "standard"}
	for range 2 {
		status, response, err := client.Do(context.Background(), http.MethodPost, "/v1/goals", "same-request", body)
		if err != nil || status != http.StatusCreated || !strings.Contains(string(response), `"goal_id":"goal_runtime"`) {
			t.Fatalf("create status=%d body=%s err=%v", status, response, err)
		}
	}
	status, response, err := client.Do(context.Background(), http.MethodPost, "/v1/goals", "same-request", map[string]any{"goal_id": "different", "raw_goal": "different", "mode": "standard"})
	if err != nil || status != http.StatusConflict || !strings.Contains(string(response), `"code":"CONFLICT"`) {
		t.Fatalf("idempotency mismatch status=%d body=%s err=%v", status, response, err)
	}
	status, response, err = client.Do(context.Background(), http.MethodGet, "/v1/doctor", "", nil)
	if err != nil || status != http.StatusOK || !strings.Contains(string(response), `"project_network_policy":"deny"`) {
		t.Fatalf("doctor status=%d body=%s err=%v", status, response, err)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestRealUnixAPIRunsGoalToVerifiedFinalReport(t *testing.T) {
	temporary, err := os.MkdirTemp("/tmp", "xgoal-app-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	project := filepath.Join(temporary, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	codex := appFixtureCodex(t, temporary)
	claude := appFixtureClaude(t, temporary)
	configuration := fmt.Sprintf(`apiVersion: xgoal.dev/v1alpha1
kind: Project
metadata: {name: unix-e2e}
project: {baseBranch: main, trustedRepository: true}
orchestration: {defaultMode: standard, maxParallel: 1, leaseTTL: 400ms, heartbeatInterval: 50ms, noProgressLimit: 2, integrationBranchPrefix: xgoal/}
agents:
  - {id: codex-implementer, adapter: codex-cli, command: %q, roles: [planner, implementer], timeout: 10s, sandbox: workspace-write, providerTransport: allow, credentialSource: cli-session, activeProbe: disabled}
  - {id: claude-reviewer, adapter: claude-cli, command: %q, roles: [reviewer], timeout: 10s, permissionMode: dontAsk, providerTransport: allow, credentialSource: cli-session, activeProbe: disabled}
workspace: {provider: git-worktree, keepFailed: true, cleanupCompletedAfter: 1h}
runtime: {provider: local-process, isolationLevelRequired: L0, projectNetwork: deny, projectSecrets: deny}
scopePolicy: {deny: ["/.git/**", "/.env"], validatorChanges: human-gate}
validators:
  - {id: output-check, type: command, phases: [change, final], argv: [sh, -c, "test -f output.txt"], timeout: 5s, required: true}
review: {requiredInStandard: true, blockSeverities: [blocker, high], requireIndependentSession: true, preferDifferentProvider: true}
policy: {gitPush: deny, publishArtifact: deny, production: deny, destructiveCommands: human-gate, expandScope: human-gate}
report: {formats: [markdown, json], includeAgentRawLogs: false, includeReproductionCommands: true}
`, codex, claude)
	if err := os.WriteFile(filepath.Join(project, "xgoal.yaml"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	appGit(t, project, "init", "-b", "main")
	appGit(t, project, "add", "xgoal.yaml")
	appGit(t, project, "-c", "user.name=xgoal", "-c", "user.email=xgoal@example.invalid", "commit", "--no-verify", "-m", "fixture")
	paths, err := app.ResolvePaths(project, filepath.Join(temporary, "state"), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveResult := make(chan error, 1)
	go func() { serveResult <- app.Serve(ctx, paths) }()
	waitForServeSocket(t, paths.SocketPath, serveResult)
	client, err := api.NewProjectClient(paths.SocketPath, time.Second, app.ExpectedIdentity(paths))
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"goal_id": "goal_unix_e2e", "raw_goal": "create output.txt", "mode": "standard"}
	status, body, err := client.Do(context.Background(), http.MethodPost, "/v1/goals", "create-unix-e2e", request)
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create status=%d body=%s err=%v", status, body, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var observed map[string]any
	for time.Now().Before(deadline) {
		status, body, err = client.Do(context.Background(), http.MethodGet, "/v1/goals/goal_unix_e2e", "", nil)
		if err == nil && status == http.StatusOK && json.Unmarshal(body, &observed) == nil && observed["state"] == string(domain.GoalCompleted) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if observed["state"] != string(domain.GoalCompleted) {
		t.Fatalf("goal did not complete: status=%d body=%s err=%v", status, body, err)
	}
	status, body, err = client.Do(context.Background(), http.MethodGet, "/v1/goals/goal_unix_e2e/report", "", nil)
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), `"AC-UNIX"`) || !strings.Contains(string(body), `"output-check"`) {
		t.Fatalf("report status=%d body=%s err=%v", status, body, err)
	}
	cancel()
	if err := <-serveResult; err != nil {
		t.Fatal(err)
	}
}

func waitForServeSocket(t *testing.T, path string, result <-chan error) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
			if dialErr == nil {
				_ = connection.Close()
				return
			}
		}
		select {
		case err := <-result:
			t.Fatalf("daemon exited before socket became ready: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket not ready; exists error=%v", func() error { _, err := os.Stat(path); return err }())
}

func appFixtureCodex(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "codex-fixture")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then echo 'codex-cli 1.0.0'; exit 0; fi
if [ "${1:-}" = "exec" ] && [ "${2:-}" = "--help" ]; then echo '--json --output-schema --sandbox'; exit 0; fi
if [ "${1:-}" = "exec" ] && [ "${2:-}" = "resume" ] && [ "${3:-}" = "--help" ]; then echo '--json --output-schema'; exit 0; fi
if [ "${1:-}" = "login" ]; then echo 'Logged in'; exit 0; fi
case " $* " in
  *" --sandbox read-only "*)
    printf '%s\n' '{"type":"thread.started","thread_id":"codex-planner-session"}'
    printf '%s\n' '{"type":"turn.started"}'
    printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"protocol_version\":\"xgoal.planner-proposal/v1alpha1\",\"contract\":{\"summary\":\"create output\",\"rationale\":\"exercise the public control path\",\"in_scope\":[\"output.txt\"],\"out_of_scope\":[\"remote publication\"],\"constraints\":[\"no network\"],\"acceptance_criteria\":[{\"id\":\"AC-UNIX\",\"statement\":\"output exists\",\"validators\":[\"output-check\"],\"human_acceptance\":false}],\"quality_attributes\":[\"deterministic validation\"],\"human_gates\":[\"scope expansion\"],\"completion_policy\":{\"require_all_required_items\":true,\"require_no_blocking_findings\":true,\"require_final_validation\":true}},\"plan\":{\"summary\":\"one bounded change\",\"work_items\":[{\"client_key\":\"output\",\"title\":\"create output\",\"objective\":\"create output.txt\",\"depends_on\":[],\"read_scope\":[\"/**\"],\"write_scope\":[\"/output.txt\"],\"acceptance_criteria\":[\"AC-UNIX\"],\"validators\":[\"output-check\"],\"recommended_role\":\"implementer\",\"required\":true}]},\"ambiguities\":[]}"}}'
    printf '%s\n' '{"type":"turn.completed"}'
    exit 0
    ;;
esac
printf 'done\n' > output.txt
session="codex-session-$$"
printf '%s\n' "{\"type\":\"thread.started\",\"thread_id\":\"$session\"}"
printf '%s\n' '{"type":"turn.started"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"protocol_version\":\"xgoal.agent-result/v1alpha1\",\"status\":\"completed\",\"summary\":\"created output\",\"changed_files_claimed\":[\"output.txt\"],\"checks_claimed\":[],\"blockers\":[],\"assumptions\":[],\"recommended_next_action\":\"\"}"}}'
printf '%s\n' '{"type":"turn.completed"}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func appFixtureClaude(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "claude-fixture")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then echo '1.0 (Claude Code)'; exit 0; fi
if [ "${1:-}" = "--help" ]; then echo '--print --output-format --json-schema --permission-mode --tools --allowedTools --resume'; exit 0; fi
if [ "${1:-}" = "auth" ]; then echo '{"loggedIn": true}'; exit 0; fi
sleep 0.8
printf '%s\n' '{"type":"system","subtype":"init","session_id":"claude-review-fixture"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"claude-review-fixture","structured_output":{"protocol_version":"xgoal.review-result/v1alpha1","review_status":"approved","findings":[],"suggested_validators":[]}}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func appGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
