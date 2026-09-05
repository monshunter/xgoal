package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/app"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/project"
	"github.com/monshunter/xgoal/internal/report"
)

// These opt-in tests send a generated temporary fixture to installed providers.
// Each uses real planning, implementation, independent review and final validation.
func TestRealCodexCLIBackgroundGoalToFinalReport(t *testing.T) {
	runRealProfileGoal(t, "codex", "gpt-6-astra")
}

func TestRealClaudeCLIBackgroundGoalToFinalReport(t *testing.T) {
	runRealProfileGoal(t, "claude", "sonnet")
}

func runRealProfileGoal(t *testing.T, providerName, model string) {
	t.Helper()
	if os.Getenv("XGOAL_RUN_REAL_GOAL_SMOKE") != "1" {
		t.Skip("set XGOAL_RUN_REAL_GOAL_SMOKE=1 to invoke real Provider services")
	}
	if configured := os.Getenv("XGOAL_SMOKE_" + strings.ToUpper(providerName) + "_MODEL"); configured != "" {
		model = configured
	}
	provider, err := exec.LookPath(providerName)
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp("/tmp", "xgoal-real-goal-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("failed real Goal fixture retained for diagnosis: %s", base)
			return
		}
		_ = os.RemoveAll(base)
	})
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "project")
	cliRepository(t, root)
	writeCurrentDirectoryFixture(t, filepath.Join(root, "README.md"), "# Fixed xgoal smoke fixture\n", 0600)
	currentDirectoryGit(t, root, "add", "README.md")
	currentDirectoryGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@invalid", "commit", "-q", "-m", "baseline")
	binary := compileCurrentDirectoryCLI(t, base)
	var environment []string
	for _, entry := range project.GitEnvironment() {
		if !strings.HasPrefix(entry, "XGOAL_") {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "XGOAL_RUNTIME_DIR="+filepath.Join(base, "runtime"))
	invoke := func(args ...string) string {
		output, err := invokeCurrentDirectoryCLI(binary, environment, append([]string{"--project", root}, args...)...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, output)
		}
		return output
	}
	invoke("init")
	// The real smoke needs the installed CLI's runtime/login environment. Its
	// validator only reads source; fixture-only role logging is not included.
	configuration := fmt.Sprintf(`apiVersion: xgoal.dev/v1alpha1
kind: Project
metadata: {name: real-background-goal}
project: {baseBranch: main, trustedRepository: true, harness: {type: autogo, required: true}}
orchestration: {defaultMode: standard, maxParallel: 1, leaseTTL: 10s, heartbeatInterval: 1s, noProgressLimit: 2, integrationBranchPrefix: xgoal/, roleProfiles: {planner: worker, implementer: worker, reviewer: reviewer}}
agents:
  - {id: worker, adapter: %s-cli, command: %q, roles: [planner, implementer], model: %q, reasoningEffort: low, timeout: 5m, providerTransport: allow, credentialSource: cli-session, activeProbe: disabled, environmentAllowlist: [PATH, HOME, TMPDIR, CODEX_HOME]}
  - {id: reviewer, adapter: %s-cli, command: %q, roles: [reviewer], model: %q, reasoningEffort: low, timeout: 5m, providerTransport: allow, credentialSource: cli-session, activeProbe: disabled, environmentAllowlist: [PATH, HOME, TMPDIR, CODEX_HOME]}
workspace: {provider: current-directory, keepFailed: true, cleanupCompletedAfter: 1h}
runtime: {provider: local-process, isolationLevelRequired: L0, projectNetwork: deny, projectSecrets: deny}
scopePolicy: {deny: ["/.git/**", "/.env"], validatorChanges: human-gate}
validators:
  - {id: output-check, type: command, phases: [change, final], trustedFiles: [xgoal.yaml], argv: [sh, -c, %q], timeout: 5s, required: true}
review: {requiredInStandard: true, blockSeverities: [blocker, high], requireIndependentSession: true, preferDifferentProvider: false}
policy: {gitPush: deny, publishArtifact: deny, production: deny, destructiveCommands: human-gate, expandScope: human-gate}
report: {formats: [markdown, json], includeAgentRawLogs: false, includeReproductionCommands: true}
`, providerName, provider, model, providerName, provider, model, "printf 'accepted\\n' | cmp - output.txt")
	if providerName == "claude" {
		configuration = strings.ReplaceAll(configuration, "[PATH, HOME, TMPDIR, CODEX_HOME]", "[PATH, HOME, TMPDIR, CLAUDE_CONFIG_DIR, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_BASE_URL, ANTHROPIC_MODEL, ANTHROPIC_DEFAULT_HAIKU_MODEL, ANTHROPIC_DEFAULT_OPUS_MODEL, ANTHROPIC_DEFAULT_SONNET_MODEL]")
	}
	writeCurrentDirectoryFixture(t, filepath.Join(root, "xgoal.yaml"), configuration, 0600)
	entry, skills := "AGENTS.md", ".agents/skills/autogo-smoke/SKILL.md"
	if providerName == "claude" {
		entry, skills = "CLAUDE.md", ".claude/skills/autogo-smoke/SKILL.md"
	}
	for _, directory := range []string{"docs", filepath.Dir(skills), ".autogo/manifests"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeCurrentDirectoryFixture(t, filepath.Join(root, entry), "# Project rules\nRead docs/acceptance.md for the exact product behavior. Preserve the final newline. The xgoal delegated worker contract owns runtime state and Git operations.\n", 0600)
	writeCurrentDirectoryFixture(t, filepath.Join(root, "docs/acceptance.md"), "# Acceptance behavior\nThe output.txt file must contain exactly the UTF-8 text accepted followed by one newline.\n", 0600)
	writeCurrentDirectoryFixture(t, filepath.Join(root, skills), "---\nname: autogo-smoke\ndescription: Implement the smoke fixture behavior described in docs/acceptance.md within the delegated packet.\n---\nFollow the project behavior and return the role result.\n", 0600)
	manifest, err := json.Marshal(map[string]any{"owner": "autogo", "schema_version": 3, "autogo_version": "0.3.0", "harness_pack": "core", "agent": providerName, "managed_files": []string{entry, skills, "docs/acceptance.md"}})
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(".autogo/manifests", providerName+".json")
	writeCurrentDirectoryFixture(t, filepath.Join(root, manifestPath), string(manifest), 0600)
	currentDirectoryGit(t, root, "add", "xgoal.yaml", ".xgoalignore", ".gitignore", entry, skills, "docs/acceptance.md", manifestPath)
	currentDirectoryGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@invalid", "commit", "-q", "-m", "configuration")
	invoke("config", "validate", "--file", filepath.Join(root, "xgoal.yaml"))
	head := currentDirectoryGit(t, root, "rev-parse", "HEAD")
	indexPath := filepath.Join(root, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	worktrees := currentDirectoryGit(t, root, "worktree", "list", "--porcelain")
	startedDaemon := false
	t.Cleanup(func() {
		if startedDaemon {
			output, err := invokeCurrentDirectoryCLI(binary, environment, "--project", root, "daemon", "stop", "--timeout", "30s")
			if err != nil {
				t.Errorf("cleanup: %v %s", err, output)
			}
		}
	})
	startOutput := invoke("daemon", "start", "--timeout", "20s")
	startedDaemon = true
	var daemon app.StartResult
	if err := json.Unmarshal([]byte(startOutput), &daemon); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	invoke("run", "--id", "goal_real_background", "--goal", "Create only output.txt with the exact UTF-8 bytes required by docs/acceptance.md. Use the registered output-check validator for the acceptance criterion. Do not change any other file or Git references. This is a single bounded Work Item; no external services or publication are needed.")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	for {
		var status struct {
			State         string `json:"state"`
			PlanningState string `json:"planning_state"`
		}
		body, statusErr := invokeCurrentDirectoryCLI(binary, environment, "--project", root, "status", "goal_real_background")
		var exitErr *exec.ExitError
		if statusErr != nil && (!errors.As(statusErr, &exitErr) || exitErr.ExitCode() != 3) {
			t.Fatalf("status: %v %s", statusErr, body)
		}
		if err := json.Unmarshal([]byte(body), &status); err != nil {
			t.Fatal(err)
		}
		if status.State == "COMPLETED" {
			break
		}
		if status.State == "WAITING" || status.PlanningState == "WAITING" {
			t.Fatalf("real Goal requires action: %s", body)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Goal did not complete: %s", body)
		case <-time.After(time.Second):
		}
	}
	var response struct {
		Report report.Report `json:"json"`
	}
	if err := json.Unmarshal([]byte(invoke("report", "goal_real_background")), &response); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "output.txt"))
	if err != nil || string(data) != "accepted\n" {
		t.Fatalf("output=%q %v", data, err)
	}
	if currentDirectoryGit(t, root, "rev-parse", "HEAD") != head || currentDirectoryGit(t, root, "worktree", "list", "--porcelain") != worktrees {
		t.Fatal("user Git state moved")
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(indexBefore, indexAfter) {
		t.Fatal("user index bytes changed")
	}
	invoke("daemon", "stop", "--timeout", "30s")
	startedDaemon = false
	assertCurrentDirectoryEvidence(t, daemon.StateDir, []report.Report{response.Report})
	privateTree := currentDirectoryGit(t, root, "rev-parse", "refs/xgoal/goals/goal_real_background/integration^{tree}")
	if privateTree != response.Report.Final.Tree {
		t.Fatalf("private ref tree %s differs from Report %s", privateTree, response.Report.Final.Tree)
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(daemon.StateDir, "state.db")}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var planningState, plannerSession string
	if err := db.QueryRow(`SELECT e.state,json_extract(e.observation_json,'$.session_id') FROM effects e JOIN goals g ON g.planning_effect_id=e.id WHERE g.id=?`, "goal_real_background").Scan(&planningState, &plannerSession); err != nil || planningState != "SUCCEEDED" || plannerSession == "" {
		t.Fatalf("real Planner not proven: state=%s session=%s err=%v", planningState, plannerSession, err)
	}
	var implementationSession, reviewerSession string
	if err := db.QueryRow(`SELECT implementation_session_id,reviewer_session_id FROM review_runs WHERE review_status='approved' AND reviewer_profile_id='reviewer' AND implementation_profile_id='worker' AND candidate_tree=? LIMIT 1`, privateTree).Scan(&implementationSession, &reviewerSession); err != nil || implementationSession == "" || reviewerSession == "" || implementationSession == reviewerSession {
		t.Fatalf("independent real Review not proven: implementer=%s reviewer=%s err=%v", implementationSession, reviewerSession, err)
	}
	for directory, role := range map[string]string{"plans": "planner", "invocations": "implementer", "reviews": "reviewer"} {
		records, err := filepath.Glob(filepath.Join(daemon.StateDir, "adapters", providerName, directory, "*", "invocation.json"))
		if err != nil || len(records) == 0 {
			t.Fatalf("missing %s invocation identity: %v", role, err)
		}
		for _, recordPath := range records {
			var record struct {
				Effective      *config.ExecutionConfig `json:"execution_config"`
				RequestHash    string                  `json:"request_hash"`
				InputTree      string                  `json:"input_tree"`
				Generation     int64                   `json:"generation"`
				DelegationHash string                  `json:"delegation_hash"`
			}
			raw, err := os.ReadFile(recordPath)
			if err != nil || json.Unmarshal(raw, &record) != nil || record.Effective == nil {
				t.Fatalf("unreadable effective invocation %s %v", recordPath, err)
			}
			e := record.Effective
			if e.Model != model || e.ReasoningEffort != "low" || e.Role != role || e.CLIVersion == "" || record.DelegationHash == "" {
				t.Fatalf("actual %s identity: %+v", role, record)
			}
			if role == "planner" && (record.RequestHash == "" || record.InputTree == "" || record.Generation < 1) {
				t.Fatalf("initial Planner provenance missing: %+v", record)
			}
		}
	}
	t.Logf("real sessions: planner=%s implementer=%s reviewer=%s", plannerSession, implementationSession, reviewerSession)
	t.Logf("real %s Standard Goal completed in %s; daemon=%d tree=%s evidence=%s", providerName, time.Since(started).Round(time.Millisecond), daemon.Identity.PID, response.Report.Final.Tree, response.Report.Final.EvidenceSetID)
}
