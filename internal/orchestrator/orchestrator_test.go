package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/goalcompile"
	"github.com/monshunter/xgoal/internal/orchestrator"
	finalreport "github.com/monshunter/xgoal/internal/report"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/workpacket"
)

func TestEngineRunsTwoWorkItemsThroughReviewPromotionAndFinalReport(t *testing.T) {
	runCurrentDirectoryFixture(t, "complete")
}

func TestEngineRejectsFinalValidationPrivateRefMutation(t *testing.T) {
	runCurrentDirectoryFixture(t, "final-ref")
}

func TestEnginePreservesLegalBlockedAndFailedResults(t *testing.T) {
	for _, status := range []string{"blocked", "failed"} {
		t.Run(status, func(t *testing.T) { runCurrentDirectoryFixture(t, status) })
	}
}

func TestEngineAutomaticallyRepairsOnlyWithinConfiguredLimit(t *testing.T) {
	for _, behavior := range []string{"auto", "auto-exhaust"} {
		t.Run(behavior, func(t *testing.T) { runCurrentDirectoryFixture(t, behavior) })
	}
}

func TestEngineContinuesBlockedResultWithConsumedAnswerAfterRestart(t *testing.T) {
	runCurrentDirectoryFixture(t, "blocked-answer")
}

func TestEngineRejectsValidatorAndReviewerSourceMutations(t *testing.T) {
	for _, phase := range []string{"validator", "reviewer", "trust"} {
		t.Run(phase, func(t *testing.T) { runCurrentDirectoryFixture(t, phase) })
	}
}

func runCurrentDirectoryFixture(t *testing.T, behavior string) {
	t.Helper()
	// Two Work Items plus independent review and final validation include
	// multiple supervised process exits; race instrumentation adds exit delay.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	project, _ = filepath.EvalSymlinks(project)
	codex := fixtureCodex(t, root)
	claude := fixtureClaude(t, root)
	if strings.HasPrefix(behavior, "blocked") || behavior == "failed" {
		if err := os.WriteFile(filepath.Join(root, "result-status"), []byte(strings.TrimSuffix(behavior, "-answer")), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if behavior == "auto-exhaust" {
		if err := os.WriteFile(filepath.Join(root, "result-status"), []byte("failed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
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
workspace: {provider: current-directory, keepFailed: true, cleanupCompletedAfter: 1h}
runtime: {provider: local-process, isolationLevelRequired: L0, projectNetwork: deny, projectSecrets: deny}
scopePolicy:
  deny: ["/.git/**", "/.env"]
  validatorChanges: human-gate
validators:
  - id: one-check
    type: command
    phases: [change, final]
    trustedFiles: [xgoal.yaml]
    argv: [sh, -c, "test -f one.txt"]
    timeout: 5s
    required: true
  - id: two-check
    type: command
    phases: [change, final]
    trustedFiles: [xgoal.yaml]
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
	if behavior == "auto" || behavior == "auto-exhaust" {
		configurationText = strings.Replace(configurationText, "  noProgressLimit: 2\n", "  noProgressLimit: 2\n  autoRetryLimit: 1\n", 1)
	}
	if behavior == "final-ref" {
		configurationText = strings.Replace(configurationText, "test -f one.txt", "test -f one.txt; if test -f two.txt; then git update-ref --no-deref refs/xgoal/goals/goal_e2e/integration HEAD; fi", 1)
	}
	if behavior == "validator" {
		configurationText = strings.Replace(configurationText, "test -f one.txt", "test -f one.txt; printf validator-mutation > one.txt", 1)
	}
	if behavior == "reviewer" {
		if err := os.WriteFile(filepath.Join(root, "review-mutation"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if behavior == "trust" {
		configurationText = strings.Replace(configurationText, "trustedFiles: [xgoal.yaml]", "trustedFiles: [xgoal.yaml, scripts/check.sh]", 1)
		configurationText = strings.Replace(configurationText, "test -f one.txt", "sh scripts/check.sh", 1)
		if err := os.Mkdir(filepath.Join(project, "scripts"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(project, "scripts/check.sh"), []byte("test -f one.txt\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "trust-mutation"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, "xgoal.yaml"), []byte(configurationText), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, project, "init", "-b", "main")
	git(t, project, "add", "xgoal.yaml")
	if behavior == "trust" {
		git(t, project, "add", "scripts/check.sh")
	}
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
	if behavior == "trust" {
		plan.WorkItems[0].WriteScope = append(plan.WorkItems[0].WriteScope, "/scripts/**")
	}
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
	repository, err := gitrepo.Open(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	originalIdentity, err := repository.ReadCheckoutIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.RunGoal(ctx, goalID); err != nil {
		status, _ := store.GoalStatus(context.Background(), goalID)
		t.Fatalf("RunGoal() error = %v; status = %+v", err, status)
	}
	failed, err := store.GoalStatus(ctx, goalID)
	if err != nil {
		t.Fatal(err)
	}
	if behavior == "auto" || behavior == "auto-exhaust" {
		if behavior == "auto-exhaust" {
			if failed.Goal.State != domain.GoalWaiting || len(failed.Attempts) != 2 || len(failed.Failures) != 2 || failed.Failures[0].Fingerprint == failed.Failures[1].Fingerprint {
				t.Fatalf("changing failures escaped total retry limit: state=%s attempts=%d failures=%+v", failed.Goal.State, len(failed.Attempts), failed.Failures)
			}
		} else {
			if failed.Goal.State != domain.GoalCompleted || len(failed.Attempts) != 3 || len(failed.Gates) != 0 {
				t.Fatalf("safe configured repair did not finish: state=%s attempts=%d gates=%d", failed.Goal.State, len(failed.Attempts), len(failed.Gates))
			}
			packets, err := workpacket.NewStore(state)
			if err != nil {
				t.Fatal(err)
			}
			feedback := false
			for _, attempt := range failed.Attempts {
				artifact, err := packets.Load(attempt.ID)
				if err != nil {
					t.Fatal(err)
				}
				if artifact.Packet.PriorAttempt != nil {
					encoded, _ := json.Marshal(artifact.Packet.PriorAttempt)
					feedback = strings.Contains(string(encoded), `"failure_error"`)
				}
			}
			if !feedback {
				t.Fatal("automatic repair did not receive actual failure feedback")
			}
		}
		if err := repository.CheckCheckoutIdentity(ctx, originalIdentity); err != nil {
			t.Fatal(err)
		}
		return
	}
	if failed.Goal.State != domain.GoalWaiting || len(failed.Attempts) != 1 {
		t.Fatalf("failed attempt must wait for explicit checkout retry: %+v", failed)
	}
	if strings.HasPrefix(behavior, "blocked") || behavior == "failed" {
		want := "AGENT_" + strings.ToUpper(strings.TrimSuffix(behavior, "-answer"))
		if len(failed.Failures) != 1 || string(failed.Failures[0].Class) != want || failed.Attempts[0].State == domain.AttemptInvalidOutput {
			t.Fatalf("legal %s was misclassified: failures=%+v attempts=%+v", behavior, failed.Failures, failed.Attempts)
		}
		if !strings.Contains(failed.Failures[0].Error, "Choose cache strategy") {
			t.Fatalf("result details lost: %+v", failed.Failures)
		}
		if strings.HasPrefix(behavior, "blocked") {
			found := false
			for _, gate := range failed.Gates {
				if gate.ReasonCode == "agent_blocked" && strings.Contains(gate.Recommendation, "Choose cache strategy") {
					found = true
				}
			}
			if !found {
				t.Fatalf("blocked decision overwritten by generic checkout retry: %+v", failed.Gates)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := sqlite.Open(ctx, state, clock.Real{})
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		restored, err := reopened.GoalStatus(ctx, goalID)
		if err != nil || len(restored.Gates) != 1 || !strings.Contains(string(restored.Gates[0].FactsJSON), `"agent_result"`) || !strings.Contains(string(restored.Gates[0].FactsJSON), `"invocation_id"`) || !strings.Contains(string(restored.Gates[0].FactsJSON), "Choose cache strategy") {
			t.Fatalf("result context did not survive restart: %v", err)
		}
		if behavior == "blocked-answer" {
			gate := restored.Gates[0]
			answer := "use local cache for this work"
			if _, err := reopened.DecideGate(ctx, gate.ID, gate.Version, domain.GateAllow, "fixture-user", answer, sqlite.EventInput{Type: "GateDecided", ActorType: "human", Payload: map[string]any{"answer": answer}}); err != nil {
				t.Fatal(err)
			}
			checkout, err := reopened.Checkout(ctx)
			if err != nil {
				t.Fatal(err)
			}
			work, err := reopened.WorkItem(ctx, checkout.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reopened.RetryCheckoutWork(ctx, work.ID, work.Version, originalIdentity, checkout.ObservedTree, sqlite.EventInput{Type: "WorkRetryRequested", ActorType: "human", Payload: map[string]any{"reason": answer}}); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(root, "result-status")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "codex-execution-count"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "expected-answer"), []byte(answer), 0600); err != nil {
				t.Fatal(err)
			}
			restarted, err := orchestrator.New(ctx, reopened, project, configuration)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.RunGoal(ctx, goalID); err != nil {
				t.Fatal(err)
			}
			completed, err := reopened.GoalStatus(ctx, goalID)
			if err != nil || completed.Goal.State != domain.GoalCompleted || len(completed.Attempts) != 3 {
				t.Fatalf("answer did not reach a successful continuation: state=%s attempts=%d failures=%+v error=%v", completed.Goal.State, len(completed.Attempts), completed.Failures, err)
			}
			if completed.Gates[0].Used != 1 || completed.Gates[0].Version != 3 {
				t.Fatalf("decision consumption not atomic: %+v", completed.Gates[0])
			}
			if _, err := os.Stat(filepath.Join(root, "answer-observed")); err != nil {
				t.Fatal("provider did not observe the decision")
			}
			if err := repository.CheckCheckoutIdentity(ctx, originalIdentity); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	checkout, err := store.Checkout(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.ObservedTree == checkout.AcceptedTree {
		t.Fatalf("scope-safe partial source was not captured as failed scene: failures=%+v", failed.Failures)
	}
	if content, err := os.ReadFile(filepath.Join(project, "one.txt")); err != nil || string(content) != "partial\n" {
		t.Fatalf("failure scene was not preserved: %q %v", content, err)
	}
	failedWork, err := store.WorkItem(ctx, checkout.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "one.txt"), []byte("operator edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := repository.SnapshotTree(ctx, gitrepo.SnapshotSpec{BaseTree: checkout.AcceptedTree})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetryCheckoutWork(ctx, failedWork.ID, failedWork.Version, originalIdentity, changed.Tree,
		sqlite.EventInput{Type: "WorkRetryRequested", ActorType: "human", Payload: map[string]any{"reason": "must reject drift"}}); !errors.Is(err, sqlite.ErrCheckoutConflict) {
		t.Fatalf("retry adopted externally changed files: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project, "one.txt"), []byte("partial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetryCheckoutWork(ctx, failedWork.ID, failedWork.Version, originalIdentity, checkout.ObservedTree,
		sqlite.EventInput{Type: "WorkRetryRequested", ActorType: "human", Payload: map[string]any{"reason": "retry failed fixture with preserved scene"}}); err != nil {
		t.Fatal(err)
	}
	runErr := engine.RunGoal(ctx, goalID)
	if behavior == "final-ref" {
		if !errors.Is(runErr, gitrepo.ErrRefConflict) {
			t.Fatalf("final ref drift was not rejected: %v", runErr)
		}
		status, err := store.GoalStatus(ctx, goalID)
		if err != nil {
			t.Fatal(err)
		}
		if status.Goal.State == domain.GoalCompleted || status.Goal.FinalReportHash != "" {
			t.Fatalf("final ref drift produced a completed report: %+v", status.Goal)
		}
		if err := repository.CheckCheckoutIdentity(ctx, originalIdentity); err != nil {
			t.Fatal(err)
		}
		return
	}
	if runErr != nil {
		status, _ := store.GoalStatus(context.Background(), goalID)
		t.Fatalf("explicit retry RunGoal() error = %v; status = %+v", runErr, status)
	}
	status, err := store.GoalStatus(ctx, goalID)
	if err != nil {
		t.Fatal(err)
	}
	if behavior != "complete" {
		if status.Goal.State != domain.GoalWaiting || len(status.Attempts) != 2 {
			t.Fatalf("phase mutation did not stop execution: %+v", status)
		}
		if behavior == "trust" {
			found := false
			for _, gate := range status.Gates {
				if gate.ReasonCode == "trusted_validator_change" && strings.Contains(gate.Recommendation, "new Goal") {
					found = true
					if _, err := store.DecideGate(ctx, gate.ID, gate.Version, domain.GateAllow, "fixture-user", "approval must not rebind frozen trust", sqlite.EventInput{Type: "GateDecided", ActorType: "human", Payload: map[string]any{"decision": "ALLOW"}}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if !found {
				t.Fatalf("trust mutation lacks actionable baseline Gate: %+v", status.Gates)
			}
		}
		for _, work := range status.WorkItems {
			if work.State == domain.WorkCompleted {
				t.Fatal("drifted candidate completed a Work")
			}
		}
		if git(t, project, "rev-parse", "refs/xgoal/goals/"+goalID+"/integration") != originalIdentity.HeadCommit {
			t.Fatal("drifted candidate advanced private integration")
		}
		if err := repository.CheckCheckoutIdentity(ctx, originalIdentity); err != nil {
			t.Fatal(err)
		}
		latest, err := store.Checkout(ctx)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := repository.SnapshotTree(ctx, gitrepo.SnapshotSpec{BaseTree: latest.AcceptedTree})
		if err != nil {
			t.Fatal(err)
		}
		if actual.Tree == latest.ObservedTree || latest.AcceptedTree != originalIdentity.HeadTree {
			t.Fatal("phase mutation was adopted as an owned or accepted scene")
		}
		work, err := store.WorkItem(ctx, latest.WorkID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.RetryCheckoutWork(ctx, work.ID, work.Version, actual.Identity, actual.Tree, sqlite.EventInput{Type: "WorkRetryRequested", ActorType: "human", Payload: map[string]any{"reason": "must not adopt phase drift"}}); !errors.Is(err, sqlite.ErrCheckoutConflict) {
			t.Fatalf("retry adopted phase mutation: %v", err)
		}
		return
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
	if err := repository.CheckCheckoutIdentity(ctx, originalIdentity); err != nil {
		t.Fatalf("user Git identity changed: %v", err)
	}
	if got := git(t, project, "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 1 {
		t.Fatalf("xgoal created a worktree: %s", got)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(project, name)); err != nil {
			t.Fatalf("result missing from current directory: %s: %v", name, err)
		}
	}
	if git(t, project, "rev-parse", "refs/xgoal/goals/"+goalID+"/integration^{tree}") != status.Goal.FinalTree {
		t.Fatal("private audit ref does not match final result")
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
		if artifact.Packet.Project.Workspace != project {
			t.Fatalf("provider CWD differs from current root: %s", artifact.Packet.Project.Workspace)
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
if [ -f "$(dirname "$0")/expected-answer" ]; then
  input="$(cat)"
  packet="$(printf '%s' "$input" | sed -n 's/^Execute only the immutable Work Packet at \(.*\/packet.json\)\..*/\1/p')"
  grep -F "$(cat "$(dirname "$0")/expected-answer")" "$packet" >/dev/null || exit 17
  mv "$(dirname "$0")/expected-answer" "$(dirname "$0")/answer-observed"
fi
if [ -f "$(dirname "$0")/result-status" ]; then
  result_status="$(cat "$(dirname "$0")/result-status")"
  outcome_counter="$(dirname "$0")/outcome-counter"
  outcome_count="$(cat "$outcome_counter" 2>/dev/null || echo 0)"
  outcome_count=$((outcome_count + 1))
  printf '%s' "$outcome_count" > "$outcome_counter"
  printf '%s\n' '{"type":"thread.started","thread_id":"codex-legal-result"}'
  printf '%s\n' '{"type":"turn.started"}'
  printf '%s\n' "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"{\\\"protocol_version\\\":\\\"xgoal.agent-result/v1alpha1\\\",\\\"status\\\":\\\"$result_status\\\",\\\"summary\\\":\\\"Need a decision $outcome_count\\\",\\\"blockers\\\":[\\\"Choose cache strategy\\\"],\\\"recommended_next_action\\\":\\\"Choose cache strategy and continue\\\"}\"}}"
  printf '%s\n' '{"type":"turn.completed"}'
  exit 0
fi
if [ ! -f "$counter" ]; then
  : > "$counter"
  printf 'partial\n' > one.txt
  printf '%s\n' '{"type":"thread.started","thread_id":"codex-invalid-session"}'
  printf '%s\n' '{"type":"turn.started"}'
  printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"protocol_version\":\"bad\",\"status\":\"completed\",\"summary\":\"invalid first attempt\"}"}}'
  printf '%s\n' '{"type":"turn.completed"}'
  exit 0
fi
if [ "$(cat one.txt 2>/dev/null || true)" != "one" ]; then printf 'one\n' > one.txt; changed=one.txt; else printf 'two\n' > two.txt; changed=two.txt; fi
if [ -f "$(dirname "$0")/trust-mutation" ]; then printf 'exit 0\n' > scripts/check.sh; fi
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
if [ -f "$(dirname "$0")/review-mutation" ]; then printf 'reviewer-mutation' > one.txt; exit 7; fi
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
