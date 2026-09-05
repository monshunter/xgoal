package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/app"
	"github.com/monshunter/xgoal/internal/project"
	"github.com/monshunter/xgoal/internal/report"
)

// This opt-in test sends only a fixed temporary fixture to the installed Codex
// service. Standard review uses a separate session with the same provider.
func TestRealCodexCLIBackgroundGoalToFinalReport(t *testing.T) {
	if os.Getenv("XGOAL_RUN_REAL_GOAL_SMOKE") != "1" {
		t.Skip("set XGOAL_RUN_REAL_GOAL_SMOKE=1 to invoke the real Codex service")
	}
	provider, err := exec.LookPath("codex")
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
project: {baseBranch: main, trustedRepository: true}
orchestration: {defaultMode: standard, maxParallel: 1, leaseTTL: 10s, heartbeatInterval: 1s, noProgressLimit: 2, integrationBranchPrefix: xgoal/}
agents:
  - {id: codex-implementer, adapter: codex-cli, command: %q, roles: [planner, implementer], timeout: 5m, sandbox: workspace-write, providerTransport: allow, credentialSource: cli-session, activeProbe: disabled, environmentAllowlist: [PATH, HOME, TMPDIR, CODEX_HOME]}
  - {id: codex-reviewer, adapter: codex-cli, command: %q, roles: [reviewer], timeout: 5m, sandbox: read-only, providerTransport: allow, credentialSource: cli-session, activeProbe: disabled, environmentAllowlist: [PATH, HOME, TMPDIR, CODEX_HOME]}
workspace: {provider: current-directory, keepFailed: true, cleanupCompletedAfter: 1h}
runtime: {provider: local-process, isolationLevelRequired: L0, projectNetwork: deny, projectSecrets: deny}
scopePolicy: {deny: ["/.git/**", "/.env"], validatorChanges: human-gate}
validators:
  - {id: output-check, type: command, phases: [change, final], argv: [sh, -c, %q], timeout: 5s, required: true}
review: {requiredInStandard: true, blockSeverities: [blocker, high], requireIndependentSession: true, preferDifferentProvider: false}
policy: {gitPush: deny, publishArtifact: deny, production: deny, destructiveCommands: human-gate, expandScope: human-gate}
report: {formats: [markdown, json], includeAgentRawLogs: false, includeReproductionCommands: true}
`, provider, provider, "printf 'accepted\\n' | cmp - output.txt")
	writeCurrentDirectoryFixture(t, filepath.Join(root, "xgoal.yaml"), configuration, 0600)
	currentDirectoryGit(t, root, "add", "xgoal.yaml", ".xgoalignore", ".gitignore")
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
	invoke("run", "--id", "goal_real_background", "--goal", "Create only output.txt with exact UTF-8 bytes accepted followed by one newline. Use the registered output-check validator for the acceptance criterion. Do not change any other file or Git references. This is a single bounded Work Item; no external services or publication are needed.")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	for {
		var status struct {
			State string `json:"state"`
		}
		body := invoke("status", "goal_real_background")
		if err := json.Unmarshal([]byte(body), &status); err != nil {
			t.Fatal(err)
		}
		if status.State == "COMPLETED" {
			break
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
	if err := db.QueryRow(`SELECT implementation_session_id,reviewer_session_id FROM review_runs WHERE review_status='approved' AND reviewer_profile_id='codex-reviewer' AND implementation_profile_id='codex-implementer' AND candidate_tree=? LIMIT 1`, privateTree).Scan(&implementationSession, &reviewerSession); err != nil || implementationSession == "" || reviewerSession == "" || implementationSession == reviewerSession {
		t.Fatalf("independent real Review not proven: implementer=%s reviewer=%s err=%v", implementationSession, reviewerSession, err)
	}
	t.Logf("real sessions: planner=%s implementer=%s reviewer=%s", plannerSession, implementationSession, reviewerSession)
	t.Logf("real Codex Standard Goal completed in %s; daemon=%d tree=%s evidence=%s", time.Since(started).Round(time.Millisecond), daemon.Identity.PID, response.Report.Final.Tree, response.Report.Final.EvidenceSetID)
}
