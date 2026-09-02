package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/finalize"
	"github.com/monshunter/xgoal/internal/goalcompile"
	"github.com/monshunter/xgoal/internal/orchestrator"
	finalreport "github.com/monshunter/xgoal/internal/report"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/workpacket"
)

func TestEngineRunsTwoWorkItemsThroughReviewPromotionAndFinalReport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	project, _ = filepath.EvalSymlinks(project)
	codex := fixtureCodex(t, root)
	claude := fixtureClaude(t, root)
	configurationText := fmt.Sprintf(`apiVersion: xgoal.dev/v1alpha1
kind: Project
metadata: {name: fixture}
project: {baseBranch: main, trustedRepository: true}
orchestration:
  defaultMode: standard
  maxParallel: 1
  leaseTTL: 5s
  heartbeatInterval: 1s
  noProgressLimit: 2
  integrationBranchPrefix: xgoal/
agents:
  - id: codex-implementer
    adapter: codex-cli
    command: %q
    roles: [planner, implementer]
    timeout: 10s
    sandbox: workspace-write
    providerTransport: allow
    credentialSource: cli-session
    activeProbe: disabled
  - id: claude-reviewer
    adapter: claude-cli
    command: %q
    roles: [reviewer]
    timeout: 10s
    permissionMode: dontAsk
    providerTransport: allow
    credentialSource: cli-session
    activeProbe: disabled
workspace: {provider: git-worktree, keepFailed: true, cleanupCompletedAfter: 1h}
runtime: {provider: local-process, isolationLevelRequired: L0, projectNetwork: deny, projectSecrets: deny}
scopePolicy:
  deny: ["/.git/**", "/.env"]
  validatorChanges: human-gate
validators:
  - id: one-check
    type: command
    phases: [change, final]
    argv: [sh, -c, "test -f one.txt"]
    timeout: 5s
    required: true
  - id: two-check
    type: command
    phases: [change, final]
    argv: [sh, -c, "test -f two.txt"]
    timeout: 5s
    required: true
review:
  requiredInStandard: true
  blockSeverities: [blocker, high]
  requireIndependentSession: true
  preferDifferentProvider: true
policy: {gitPush: deny, publishArtifact: deny, production: deny, destructiveCommands: human-gate, expandScope: human-gate}
report: {formats: [markdown, json], includeAgentRawLogs: false, includeReproductionCommands: true}
`, codex, claude)
	if err := os.WriteFile(filepath.Join(project, "xgoal.yaml"), []byte(configurationText), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, project, "init", "-b", "main")
	git(t, project, "add", "xgoal.yaml")
	git(t, project, "-c", "user.name=xgoal", "-c", "user.email=xgoal@example.invalid", "commit", "--no-verify", "-m", "fixture")
	configuration, err := config.LoadFile(filepath.Join(project, "xgoal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	store, err := sqlite.Open(ctx, state, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	goalID := "goal_e2e"
	contract := goalcompile.Contract{
		Summary: "create two files", Rationale: "exercise the full loop",
		InScope: []string{"one.txt", "two.txt"}, OutOfScope: []string{"remote publication"}, Constraints: []string{"no network"},
		AcceptanceCriteria: []goalcompile.AcceptanceCriterion{
			{ID: "AC-1", Statement: "one.txt exists", Validators: []string{"one-check"}},
			{ID: "AC-2", Statement: "two.txt exists", Validators: []string{"two-check"}},
		},
		QualityAttributes: []string{"deterministic validation"}, HumanGates: []string{"scope expansion"},
		CompletionPolicy: goalcompile.CompletionPolicy{RequireAllRequiredItems: true, RequireNoBlockingFindings: true, RequireFinalValidation: true},
	}
	plan := goalcompile.Plan{Summary: "two serial changes", WorkItems: []goalcompile.PlanWork{
		{ClientKey: "one", Title: "create one", Objective: "create one.txt", ReadScope: []string{"/**"}, WriteScope: []string{"/one.txt"}, AcceptanceCriteria: []string{"AC-1"}, Validators: []string{"one-check"}, RecommendedRole: domain.RoleImplementer, Required: true},
		{ClientKey: "two", Title: "create two", Objective: "create two.txt", DependsOn: []string{"one"}, ReadScope: []string{"/**"}, WriteScope: []string{"/two.txt"}, AcceptanceCriteria: []string{"AC-2"}, Validators: []string{"two-check"}, RecommendedRole: domain.RoleImplementer, Required: true},
	}}
	compiled, err := goalcompile.Compile(goalID, goalID+"_revision_1", goalID+"_plan_1", contract, plan, map[string]bool{"one-check": true, "two-check": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateGoal(ctx, domain.Goal{ID: goalID, State: domain.GoalDraft, Version: 1}, sqlite.EventInput{Type: "GoalCreated", ActorType: "human", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	configHash, _ := configuration.Hash()
	revision, err := store.FreezeGoalRevision(ctx, sqlite.GoalRevisionDraft{ID: goalID + "_revision_1", GoalID: goalID, Revision: 1, RawGoal: "create two files", Contract: map[string]any{"protocol_version": goalcompile.ContractVersion, "contract": compiled.Contract, "config_hash": configHash, "created_by": "fixture", "mode": "standard"}}, 1, sqlite.EventInput{Type: "GoalRevisionFrozen", ActorType: "planner", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	planRevision, err := store.CreatePlanRevision(ctx, sqlite.PlanRevisionDraft{ID: goalID + "_plan_1", GoalRevisionID: revision.ID, Revision: 1, WorkItems: compiled.WorkItems, Dependencies: compiled.Dependencies}, sqlite.EventInput{Type: "PlanRevisionCreated", ActorType: "planner", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivatePlanRevision(ctx, planRevision.ID, planRevision.Version, 2, sqlite.EventInput{Type: "PlanActivated", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RefreshReadyWork(ctx, goalID, sqlite.EventInput{Type: "WorkReady", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	engine, err := orchestrator.New(ctx, store, project, configuration)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.RunGoal(ctx, goalID); err != nil {
		status, _ := store.GoalStatus(context.Background(), goalID)
		t.Fatalf("RunGoal() error = %v; status = %+v", err, status)
	}
	status, err := store.GoalStatus(ctx, goalID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Goal.State != domain.GoalCompleted || len(status.WorkItems) != 2 || len(status.Attempts) != 3 {
		var diagnostics []domain.Event
		for _, attempt := range status.Attempts {
			events, _ := store.Events(ctx, "attempt", attempt.ID)
			diagnostics = append(diagnostics, events...)
		}
		t.Fatalf("final status = %+v\nattempt events = %+v", status, diagnostics)
	}
	for _, work := range status.WorkItems {
		if work.State != domain.WorkCompleted {
			t.Fatalf("work %s state = %s", work.ID, work.State)
		}
	}
	packets, err := workpacket.NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	priorAttempts := 0
	for _, attempt := range status.Attempts {
		artifact, loadErr := packets.Load(attempt.ID)
		if loadErr != nil {
			t.Fatalf("load packet %s: %v", attempt.ID, loadErr)
		}
		if artifact.Packet.Environment.OS == "" || artifact.Packet.Environment.Arch == "" || artifact.Packet.Environment.GitCommit == "" || artifact.Packet.Environment.ToolVersions["codex-cli"] == "" {
			t.Fatalf("packet %s lacks attributable environment facts: %+v", attempt.ID, artifact.Packet.Environment)
		}
		if artifact.Packet.PriorAttempt != nil {
			priorAttempts++
			if artifact.Packet.PriorAttempt.FailureClass != "AGENT_PROTOCOL_INVALID" || artifact.Packet.PriorAttempt.FailureFingerprint == "" {
				t.Fatalf("packet %s has invalid prior failure context: %+v", attempt.ID, artifact.Packet.PriorAttempt)
			}
		}
	}
	if priorAttempts != 1 {
		t.Fatalf("prior failure context appeared in %d packets, want 1", priorAttempts)
	}
	for _, lease := range status.Leases {
		if lease.State == domain.LeaseActive {
			t.Fatalf("active lease remains: %+v", lease)
		}
	}
	files, err := finalreport.NewFileManager(state)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := finalize.New(store, files)
	if err != nil {
		t.Fatal(err)
	}
	record, jsonBytes, markdown, err := manager.Read(ctx, goalID)
	if err != nil {
		t.Fatal(err)
	}
	if record.TreeHash != status.Goal.FinalTree || !strings.Contains(string(jsonBytes), `"AC-1"`) || !strings.Contains(string(jsonBytes), `"AC-2"`) || !strings.Contains(string(jsonBytes), `"state":"INVALID_OUTPUT"`) || !strings.Contains(string(markdown), "one-check") || !strings.Contains(string(markdown), "two-check") {
		t.Fatalf("report bindings missing: record=%+v json=%s markdown=%s", record, jsonBytes, markdown)
	}
}

func fixtureCodex(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "codex-fixture")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then echo 'codex-cli 1.0.0'; exit 0; fi
if [ "${1:-}" = "exec" ] && [ "${2:-}" = "--help" ]; then echo '--json --output-schema --sandbox'; exit 0; fi
if [ "${1:-}" = "exec" ] && [ "${2:-}" = "resume" ] && [ "${3:-}" = "--help" ]; then echo '--json --output-schema'; exit 0; fi
if [ "${1:-}" = "login" ]; then echo 'Logged in'; exit 0; fi
counter="$(dirname "$0")/codex-execution-count"
if [ ! -f "$counter" ]; then
  : > "$counter"
  printf '%s\n' '{"type":"thread.started","thread_id":"codex-invalid-session"}'
  printf '%s\n' '{"type":"turn.started"}'
  printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"protocol_version\":\"bad\",\"status\":\"completed\",\"summary\":\"invalid first attempt\"}"}}'
  printf '%s\n' '{"type":"turn.completed"}'
  exit 0
fi
if [ ! -f one.txt ]; then printf 'one\n' > one.txt; changed=one.txt; else printf 'two\n' > two.txt; changed=two.txt; fi
sleep 0.1
session="codex-session-$$"
printf '%s\n' "{\"type\":\"thread.started\",\"thread_id\":\"$session\"}"
printf '%s\n' '{"type":"turn.started"}'
printf '%s\n' "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"{\\\"protocol_version\\\":\\\"xgoal.agent-result/v1alpha1\\\",\\\"status\\\":\\\"completed\\\",\\\"summary\\\":\\\"created $changed\\\",\\\"changed_files_claimed\\\":[\\\"$changed\\\"],\\\"checks_claimed\\\":[],\\\"blockers\\\":[],\\\"assumptions\\\":[],\\\"recommended_next_action\\\":\\\"\\\"}\"}}"
printf '%s\n' '{"type":"turn.completed"}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func fixtureClaude(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "claude-fixture")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then echo '1.0 (Claude Code)'; exit 0; fi
if [ "${1:-}" = "--help" ]; then echo '--print --output-format --json-schema --permission-mode --tools --allowedTools --resume'; exit 0; fi
if [ "${1:-}" = "auth" ]; then echo '{"loggedIn": true}'; exit 0; fi
printf '%s\n' '{"type":"system","subtype":"init","session_id":"claude-review-fixture"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"claude-review-fixture","structured_output":{"protocol_version":"xgoal.review-result/v1alpha1","review_status":"approved","findings":[],"suggested_validators":[]}}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func git(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
