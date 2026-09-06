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
	"github.com/monshunter/xgoal/internal/exporter"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/project"
	"github.com/monshunter/xgoal/internal/report"
	"github.com/monshunter/xgoal/internal/scenario"
)

// Real CLI and detached daemon processes exercise the complete public path.
// Only provider executables are deterministic fixtures; no provider service is contacted.
func TestRealCLICurrentDirectoryTwoGoalsPreserveGitAndBindFinalEvidence(t *testing.T) {
	runRealCLICurrentDirectoryGoals(t, false)
}

func runRealCLICurrentDirectoryGoals(t *testing.T, withServices bool) {
	root, err := os.MkdirTemp("/tmp", "xgoal-cli-current-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	binary := compileCurrentDirectoryCLI(t, bin)
	projectRoot := filepath.Join(root, "project")
	cliRepository(t, projectRoot)
	writeCurrentDirectoryFixture(t, filepath.Join(projectRoot, "README.md"), "fixture\n", 0600)
	currentDirectoryGit(t, projectRoot, "add", "README.md")
	currentDirectoryGit(t, projectRoot, "-c", "user.name=Fixture", "-c", "user.email=fixture@invalid", "commit", "-q", "-m", "initial")
	rolesPath := filepath.Join(root, "roles.tsv")
	var scenarioIDs []string
	if withServices {
		scenarioIDs = []string{"output-workflow"}
	}
	proposal := currentDirectoryProviders(t, bin, rolesPath, scenarioIDs...)
	environment := []string{}
	for _, entry := range project.GitEnvironment() {
		if !strings.HasPrefix(entry, "XGOAL_") && !strings.HasPrefix(entry, "PATH=") {
			environment = append(environment, entry)
		}
	}
	if withServices {
		environment = append(environment, "XGOAL_PROJECT_VISIBLE=fixture")
	}
	environment = append(environment, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "XGOAL_RUNTIME_DIR="+filepath.Join(root, "run"))
	invoke := func(args ...string) string {
		t.Helper()
		all := append([]string{"--project", projectRoot}, args...)
		output, err := invokeCurrentDirectoryCLI(binary, environment, all...)
		if err != nil {
			t.Fatalf("CLI %v failed: %v\n%s", args, err, output)
		}
		return output
	}
	invoke("init")
	configuration := currentDirectoryConfiguration(bin, rolesPath)
	if withServices {
		configuration = managedServiceConfiguration(t, projectRoot, configuration, rolesPath)
	}
	writeCurrentDirectoryFixture(t, filepath.Join(projectRoot, "xgoal.yaml"), configuration, 0600)
	invoke("config", "validate", "--file", filepath.Join(projectRoot, "xgoal.yaml"))
	currentDirectoryGit(t, projectRoot, "add", "xgoal.yaml", ".xgoalignore", ".gitignore")
	if withServices {
		currentDirectoryGit(t, projectRoot, "add", "service-server.py", "service-client.py")
	}
	currentDirectoryGit(t, projectRoot, "-c", "user.name=Fixture", "-c", "user.email=fixture@invalid", "commit", "-q", "-m", "configure fixture")
	repo, err := gitrepo.Open(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repo.ReadCheckoutIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(repo.CommonDir(), "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	worktreesBefore := currentDirectoryGit(t, projectRoot, "worktree", "list", "--porcelain")
	branchesBefore := currentDirectoryGit(t, projectRoot, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	started := false
	t.Cleanup(func() {
		if started {
			if output, err := invokeCurrentDirectoryCLI(binary, environment, "--project", projectRoot, "daemon", "stop", "--timeout", "10s"); err != nil {
				t.Errorf("stop fixture daemon: %v\n%s", err, output)
			}
		}
	})
	startOutput := invoke("daemon", "start", "--timeout", "20s")
	started = true
	var daemon app.StartResult
	if err := json.Unmarshal([]byte(startOutput), &daemon); err != nil {
		t.Fatal(err)
	}
	if daemon.Identity.PID <= 0 || daemon.Identity.PID == os.Getpid() || daemon.ProjectRoot != projectRoot {
		t.Fatalf("not an independent project daemon: %+v", daemon)
	}
	proposalPath := filepath.Join(root, "proposal.json")
	writeCurrentDirectoryFixture(t, proposalPath, proposal, 0600)
	var reports []report.Report
	var reportHashes []string
	for index, goalID := range []string{"goal_current_first", "goal_current_second"} {
		args := []string{"run", "--id", goalID, "--goal", "append one accepted line to output.txt", "--wait"}
		if index == 1 {
			args = append(args, "--proposal-file", proposalPath)
		}
		startedAt := time.Now()
		invoke(args...)
		var status struct {
			State          string `json:"state"`
			FinalTree      string `json:"final_tree"`
			EvidenceSetID  string `json:"final_evidence_set_id"`
			ReportHash     string `json:"final_report_hash"`
			ExecutionModel string `json:"execution_model"`
		}
		if err := json.Unmarshal([]byte(invoke("status", goalID)), &status); err != nil {
			t.Fatal(err)
		}
		if status.State != "COMPLETED" || status.ExecutionModel != "current-directory" || status.FinalTree == "" {
			t.Fatalf("Goal did not complete: %+v", status)
		}
		var response struct {
			Tree   string        `json:"tree"`
			Hash   string        `json:"report_hash"`
			Report report.Report `json:"json"`
		}
		if err := json.Unmarshal([]byte(invoke("report", goalID)), &response); err != nil {
			t.Fatal(err)
		}
		ref := "refs/xgoal/goals/" + goalID + "/integration"
		privateCommit := currentDirectoryGit(t, projectRoot, "rev-parse", ref)
		privateTree := currentDirectoryGit(t, projectRoot, "rev-parse", ref+"^{tree}")
		if response.Tree != status.FinalTree || response.Report.Final.Tree != privateTree || privateTree != status.FinalTree || response.Report.Final.Commit != privateCommit || response.Report.Final.EvidenceSetID != status.EvidenceSetID || response.Hash != status.ReportHash {
			t.Fatalf("final report, status, and private ref differ: status=%+v report=%+v ref=%s/%s", status, response, privateCommit, privateTree)
		}
		if err := repo.CheckSnapshot(context.Background(), gitrepo.SnapshotSpec{BaseTree: privateTree, ExcludePaths: []string{daemon.StateDir}}, identity, privateTree); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filepath.Join(projectRoot, "output.txt"))
		if err != nil || string(content) != strings.Repeat("accepted\n", index+1) {
			t.Fatalf("current-directory result=%q err=%v", content, err)
		}
		indexAfter, err := os.ReadFile(indexPath)
		if err != nil || !bytes.Equal(indexBefore, indexAfter) {
			t.Fatal("user index bytes changed")
		}
		if currentDirectoryGit(t, projectRoot, "worktree", "list", "--porcelain") != worktreesBefore || currentDirectoryGit(t, projectRoot, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/") != branchesBefore {
			t.Fatal("user branches or Git worktree registrations changed")
		}
		reports = append(reports, response.Report)
		reportHashes = append(reportHashes, response.Hash)
		t.Logf("%s completed through CLI/daemon in %s: tree=%s", goalID, time.Since(startedAt).Round(time.Millisecond), privateTree)
	}
	if reports[0].Final.Tree == reports[1].Final.Tree {
		t.Fatal("second Goal did not produce a distinct accepted Tree")
	}
	if parent := currentDirectoryGit(t, projectRoot, "rev-parse", reports[1].Final.Commit+"^"); parent != reports[0].Final.Commit {
		t.Fatalf("second Goal did not continue the first accepted private commit: %s", parent)
	}
	if historical := invoke("report", reports[0].Goal.ID); !strings.Contains(historical, reportHashes[0]) {
		t.Fatal("second Goal changed the first immutable report")
	}
	auditPath := filepath.Join(root, "audit")
	invoke("export", reports[0].Goal.ID, "--output", auditPath)
	manifestBytes, err := os.ReadFile(filepath.Join(auditPath, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest exporter.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	reportCount, supplied := 0, false
	for _, owner := range manifest.Owners {
		if owner.Kind == "report" {
			reportCount++
		}
		if owner.Kind == "planner" && strings.Contains(owner.ArtifactStatus, "provided-proposal") {
			supplied = true
		}
	}
	if reportCount != 2 || !supplied {
		t.Fatalf("full export lost historical or supplied-proposal Goal: reports=%d supplied=%v", reportCount, supplied)
	}
	invoke("daemon", "stop", "--timeout", "10s")
	started = false
	assertCurrentDirectoryRoles(t, rolesPath, projectRoot)
	assertCurrentDirectoryEvidence(t, daemon.StateDir, reports)
	if withServices {
		assertManagedServicesStopped(t, daemon.StateDir)
	}
}

func compileCurrentDirectoryCLI(t *testing.T, bin string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(bin, "xgoal")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/xgoal")
	command.Dir = filepath.Clean(filepath.Join(cwd, "..", ".."))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile CLI: %v\n%s", err, output)
	}
	return binary
}

func invokeCurrentDirectoryCLI(binary string, environment []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = environment
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	return string(output), err
}

func currentDirectoryGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(project.GitEnvironment(), "GIT_OPTIONAL_LOCKS=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeCurrentDirectoryFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func currentDirectoryProviders(t *testing.T, bin, rolesPath string, scenarioIDs ...string) string {
	t.Helper()
	proposal := `{"contract":{"summary":"append output","rationale":"exercise current directory execution","in_scope":["output.txt"],"out_of_scope":["remote publication"],"constraints":["no network"],"acceptance_criteria":[{"id":"AC-CURRENT","statement":"output contains an accepted line","validators":["output-check"],"human_acceptance":false}],"quality_attributes":["deterministic validation"],"human_gates":["scope expansion"],"completion_policy":{"require_all_required_items":true,"require_no_blocking_findings":true,"require_final_validation":true}},"plan":{"summary":"one bounded change","work_items":[{"client_key":"output","title":"append output","objective":"append one accepted line to output.txt","depends_on":[],"read_scope":["/**"],"write_scope":["/output.txt"],"acceptance_criteria":["AC-CURRENT"],"validators":["output-check"],"recommended_role":"implementer","required":true}]}}`
	var plannerProposal map[string]any
	if err := json.Unmarshal([]byte(proposal), &plannerProposal); err != nil {
		t.Fatal(err)
	}
	if len(scenarioIDs) > 0 {
		criteria := plannerProposal["contract"].(map[string]any)["acceptance_criteria"].([]any)
		criteria[0].(map[string]any)["scenario_ids"] = scenarioIDs
		updated, err := json.Marshal(plannerProposal)
		if err != nil {
			t.Fatal(err)
		}
		proposal = string(updated)
	}
	plannerProposal["protocol_version"], plannerProposal["ambiguities"] = "xgoal.planner-proposal/v1alpha1", []any{}
	plannerJSON, err := json.Marshal(plannerProposal)
	if err != nil {
		t.Fatal(err)
	}
	message := func(text string) string {
		encoded, err := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": text}})
		if err != nil {
			t.Fatal(err)
		}
		return "printf '%s\\n' " + currentDirectoryShellQuote(string(encoded)) + "\n"
	}
	logRole := func(role string) string {
		return "printf '" + role + "\\t%s\\n' \"$(pwd -P)\" >> " + currentDirectoryShellQuote(rolesPath) + "\n"
	}
	codex := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then echo 'codex-cli 1.0.0'; exit 0; fi
if [ "${1:-}" = "exec" ] && [ "${2:-}" = "--help" ]; then echo '--json --output-schema --sandbox'; exit 0; fi
if [ "${1:-}" = "exec" ] && [ "${2:-}" = "resume" ] && [ "${3:-}" = "--help" ]; then echo '--json --output-schema'; exit 0; fi
if [ "${1:-}" = "login" ]; then echo 'Logged in'; exit 0; fi
printf '%s\n' "{\"type\":\"thread.started\",\"thread_id\":\"codex-session-$$\"}"
printf '%s\n' '{"type":"turn.started"}'
case " $* " in
  *" --sandbox read-only "*)
` + logRole("planner") + message(string(plannerJSON)) + `    ;;
  *)
` + logRole("implementer") + `printf 'accepted\n' >> output.txt
` + message(`{"protocol_version":"xgoal.agent-result/v1alpha1","status":"completed","summary":"appended accepted line","changed_files_claimed":["output.txt"],"checks_claimed":[],"blockers":[],"assumptions":[],"recommended_next_action":""}`) + `    ;;
esac
printf '%s\n' '{"type":"turn.completed"}'
`
	claude := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then echo '1.0 (Claude Code)'; exit 0; fi
if [ "${1:-}" = "--help" ]; then echo '--print --output-format --json-schema --permission-mode --tools --allowedTools --resume'; exit 0; fi
if [ "${1:-}" = "auth" ]; then echo '{"loggedIn": true}'; exit 0; fi
` + logRole("reviewer") + `printf '%s\n' '{"type":"system","subtype":"init","session_id":"claude-independent-review"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"claude-independent-review","structured_output":{"protocol_version":"xgoal.review-result/v1alpha1","review_status":"approved","findings":[],"suggested_validators":[]}}'
`
	writeCurrentDirectoryFixture(t, filepath.Join(bin, "codex"), codex, 0700)
	writeCurrentDirectoryFixture(t, filepath.Join(bin, "claude"), claude, 0700)
	return proposal
}

func currentDirectoryShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func assertCurrentDirectoryRoles(t *testing.T, path, root string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		role, cwd, ok := strings.Cut(line, "\t")
		if !ok || cwd != root {
			t.Fatalf("role executed outside the current directory: %q", line)
		}
		counts[role]++
	}
	for role, minimum := range map[string]int{"planner": 1, "implementer": 2, "reviewer": 2, "validator": 4} {
		if counts[role] < minimum {
			t.Fatalf("role %s did not execute enough times: %v", role, counts)
		}
	}
}

func assertCurrentDirectoryEvidence(t *testing.T, stateDir string, reports []report.Report) {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(stateDir, "state.db")}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var unfinished int
	if err := db.QueryRow(`SELECT count(*) FROM process_invocations WHERE state NOT IN ('EXITED','TERMINATED')`).Scan(&unfinished); err != nil || unfinished != 0 {
		t.Fatalf("daemon stopped with unconfirmed process ownership: count=%d %v", unfinished, err)
	}
	for _, value := range reports {
		var setTree, phase string
		if err := db.QueryRow(`SELECT tree_hash,phase FROM evidence_sets WHERE id=?`, value.Final.EvidenceSetID).Scan(&setTree, &phase); err != nil {
			t.Fatal(err)
		}
		if setTree != value.Final.Tree || phase != "FINAL" {
			t.Fatalf("final evidence set differs: %s/%s report=%s", phase, setTree, value.Final.Tree)
		}
		for _, criterion := range value.Criteria {
			if criterion.Status != "PASS" || len(criterion.EvidenceIDs) == 0 {
				t.Fatalf("criterion not proven: %+v", criterion)
			}
			for _, id := range criterion.EvidenceIDs {
				var kind string
				if err := db.QueryRow(`SELECT kind FROM evidence_records WHERE id=?`, id).Scan(&kind); err != nil {
					t.Fatal(err)
				}
				if kind == "SCENARIO" {
					m, err := scenario.Load(context.Background(), stateDir, id)
					if err != nil || m.TreeHash != value.Final.Tree || len(m.Files) != len(m.Scenario.ArtifactPaths) {
						t.Fatalf("invalid scenario evidence: %+v %v", m, err)
					}
					continue
				}
				var tree, receiptTree, result string
				if err := db.QueryRow(`SELECT evidence.tree_hash,run.tree_hash,run.result FROM evidence_records evidence JOIN validator_runs run ON run.receipt_hash=evidence.receipt_hash WHERE evidence.id=?`, id).Scan(&tree, &receiptTree, &result); err != nil {
					t.Fatal(err)
				}
				if tree != value.Final.Tree || receiptTree != value.Final.Tree || result != "PASSED" {
					t.Fatalf("final Evidence/Receipt is stale: %s %s %s", tree, receiptTree, result)
				}
			}
		}
	}
}

func currentDirectoryConfiguration(bin, rolesPath string) string {
	return fmt.Sprintf(`apiVersion: xgoal.dev/v1alpha1
kind: Project
metadata: {name: current-directory-cli}
project: {baseBranch: main, trustedRepository: true}
orchestration: {defaultMode: standard, maxParallel: 1, leaseTTL: 10s, heartbeatInterval: 1s, noProgressLimit: 2, integrationBranchPrefix: xgoal/}
agents:
  - {id: codex-implementer, adapter: codex-cli, command: %q, roles: [planner, implementer], timeout: 20s, providerTransport: allow, credentialSource: cli-session, activeProbe: disabled}
  - {id: claude-reviewer, adapter: claude-cli, command: %q, roles: [reviewer], timeout: 20s, permissionMode: dontAsk, providerTransport: allow, credentialSource: cli-session, activeProbe: disabled}
workspace: {provider: current-directory, keepFailed: true, cleanupCompletedAfter: 1h}
runtime: {provider: local-process, isolationLevelRequired: L0, projectNetwork: deny, projectSecrets: deny}
scopePolicy: {deny: ["/.git/**", "/.env"], validatorChanges: human-gate}
validators:
  - {id: output-check, type: command, phases: [change, final], trustedFiles: [xgoal.yaml], argv: [sh, -c, %q], timeout: 5s, required: true}
review: {requiredInStandard: true, blockSeverities: [blocker, high], requireIndependentSession: true, preferDifferentProvider: true}
policy: {gitPush: deny, publishArtifact: deny, production: deny, destructiveCommands: human-gate, expandScope: human-gate}
report: {formats: [markdown, json], includeAgentRawLogs: false, includeReproductionCommands: true}
`, filepath.Join(bin, "codex"), filepath.Join(bin, "claude"), "printf 'validator\\t%s\\n' \"$(pwd -P)\" >> "+currentDirectoryShellQuote(rolesPath)+"; test -s output.txt")
}
