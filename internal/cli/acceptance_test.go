package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/acceptance"
	"github.com/monshunter/xgoal/internal/config"
	callindex "github.com/monshunter/xgoal/internal/invocation"
	"github.com/monshunter/xgoal/internal/supervisor"
)

func acceptanceServiceConfiguration(t *testing.T, root, text, roles string) string {
	t.Helper()
	configured := managedServiceConfiguration(t, root, text, roles)
	cfg, err := config.Load(strings.NewReader(configured))
	if err != nil {
		t.Fatal(err)
	}
	profile := cfg.Agents[1]
	profile.ID = "claude-acceptance"
	profile.Roles = []string{"acceptance"}
	profile.AllowedTools = []string{"Read", "Bash(./service-client.py *)"}
	cfg.Agents = append(cfg.Agents, profile)
	cfg.Acceptance = &config.Acceptance{ScenarioIDs: []string{"output-workflow"}}
	writeCurrentDirectoryFixture(t, filepath.Join(root, "service-client.py"), "#!/usr/bin/env python3\n"+managedServiceClient, 0700)
	if err := os.Chmod(filepath.Join(root, "service-client.py"), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The bounded CLI fixture executes the actual trusted client and emits a
	// separate acceptance claim. Independent final Validators still run afterward.
	binary := profile.Command
	script, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	marker := "printf 'reviewer\\t"
	start := strings.Index(string(script), marker)
	if start < 0 {
		t.Fatal("missing reviewer marker")
	}
	injected := `case " $* " in
 *" --tools Bash,Read "*)
` + "printf 'acceptance\\t%s\\n' \"$(pwd -P)\" >> " + currentDirectoryShellQuote(roles) + "\n" + "./service-client.py api assert " + currentDirectoryShellQuote(roles) + ` >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"acceptance-independent-session"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"acceptance-independent-session","structured_output":{"protocol_version":"xgoal.agent-result/v1alpha1","status":"completed","summary":"observed real service response","changed_files_claimed":[],"checks_claimed":[],"blockers":[],"assumptions":[],"recommended_next_action":"run independent assertions"}}'
exit 0 ;;
esac
`
	updated := string(script[:start]) + injected + string(script[start:])
	writeCurrentDirectoryFixture(t, binary, updated, 0700)
	return string(data)
}

func TestRealCLIAcceptanceSessionCallsServiceBeforeIndependentFinalAssertions(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-acceptance-cli-")
	if err != nil {
		t.Fatal(err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("acceptance fixture retained: %s", base)
		} else {
			_ = os.RemoveAll(base)
		}
	})
	binary := compileCurrentDirectoryCLI(t, base)
	f := newBackgroundFixture(t, base, binary, "acceptance", acceptanceServiceConfiguration)
	writeCurrentDirectoryFixture(t, f.release, "release", 0600)
	f.invoke(t, "run", "--id", "goal_acceptance", "--goal", "append accepted output and verify through the running service")
	final := f.complete(t, "goal_acceptance")
	f.invoke(t, "daemon", "stop", "--timeout", "10s")
	f.stopped = true
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(f.daemon.StateDir, "state.db")}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT input_json,observation_json FROM invocations WHERE goal_id='goal_acceptance' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for rows.Next() {
		var rawInput, rawObservation []byte
		if err := rows.Scan(&rawInput, &rawObservation); err != nil {
			t.Fatal(err)
		}
		var input callindex.Input
		var output callindex.Observation
		if json.Unmarshal(rawInput, &input) != nil || json.Unmarshal(rawObservation, &output) != nil {
			t.Fatal("invalid invocation index")
		}
		if input.Validate() != nil || output.Status != "returned" || output.Cursor == 0 || output.SessionID == "" || output.LogError != "" {
			t.Fatalf("incomplete invocation context: %s %s", rawInput, rawObservation)
		}
		if input.Role == "planner" && (input.RequestHash == "" || input.GoalRevisionHash != "") {
			t.Fatal("Planner requires request identity without a premature revision")
		}
		seen[input.Role] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, role := range []string{"planner", "implementer", "reviewer", "acceptance"} {
		if !seen[role] {
			t.Fatalf("missing %s invocation", role)
		}
	}
	var requestJSON, observationJSON []byte
	var state string
	if err := db.QueryRow(`SELECT state,request_json,observation_json FROM effects WHERE effect_type='acceptance'`).Scan(&state, &requestJSON, &observationJSON); err != nil {
		t.Fatal(err)
	}
	var request acceptance.Request
	var observation acceptance.Observation
	if json.Unmarshal(requestJSON, &request) != nil || json.Unmarshal(observationJSON, &observation) != nil || state != "SUCCEEDED" || observation.Result == nil || observation.Result.Status != "completed" || observation.Historical || request.Packet.TreeHash != final.Final.Tree {
		t.Fatalf("invalid acceptance %s %s %s", state, requestJSON, observationJSON)
	}
	if _, err := acceptance.ReadClaim(f.daemon.StateDir, request); err != nil {
		t.Fatal(err)
	}
	var finalRuns int
	if err := db.QueryRow(`SELECT count(*) FROM validator_runs r JOIN evidence_records e ON e.receipt_hash=r.receipt_hash JOIN evidence_set_members m ON m.evidence_id=e.id JOIN evidence_sets s ON s.id=m.evidence_set_id WHERE s.phase='FINAL' AND r.result='PASSED'`).Scan(&finalRuns); err != nil || finalRuns == 0 {
		t.Fatalf("no independent final assertion: %d %v", finalRuns, err)
	}
	var processes int
	if err := db.QueryRow(`SELECT count(*) FROM process_invocations WHERE state NOT IN ('EXITED','TERMINATED')`).Scan(&processes); err != nil || processes != 0 {
		t.Fatalf("remaining process ownership %d %v", processes, err)
	}
	if len(final.Scenarios) != 1 {
		t.Fatal("missing sealed scenario")
	}
	roles, err := os.ReadFile(filepath.Join(base, "acceptance.roles"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(roles)), "\n")
	acceptorIndex := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "acceptance\t") {
			acceptorIndex = i
		}
	}
	if acceptorIndex < 0 || len(lines) <= acceptorIndex+2 || !strings.HasPrefix(lines[len(lines)-1], "validator\t") {
		t.Fatalf("expected acceptance then trusted assertions: %s", roles)
	}
	stopped, err := os.ReadFile(filepath.Join(filepath.Dir(request.Packet.ScenarioDir), "scenario", "stopped"))
	if err != nil || string(stopped) != "api\ndb\n" {
		t.Fatalf("service cleanup %q %v", stopped, err)
	}
}

func TestRealCLIAcceptanceBlockedAnswerAndFalseClaimsCannotComplete(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-acceptance-boundaries-")
	if err != nil {
		t.Fatal(err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("acceptance boundary fixtures retained: %s", base)
		} else {
			_ = os.RemoveAll(base)
		}
	})
	binary := compileCurrentDirectoryCLI(t, base)
	for _, mode := range []string{"blocked", "source-write", "false-claim"} {
		t.Run(mode, func(t *testing.T) {
			approved := filepath.Join(base, mode+".approved")
			f := newBackgroundFixture(t, base, binary, mode, func(t *testing.T, root, text, roles string) string {
				text = acceptanceServiceConfiguration(t, root, text, roles)
				cfg, err := config.Load(strings.NewReader(text))
				if err != nil {
					t.Fatal(err)
				}
				binary := cfg.Agents[2].Command
				data, err := os.ReadFile(binary)
				if err != nil {
					t.Fatal(err)
				}
				script := string(data)
				marker := "./service-client.py api assert "
				injection := ""
				switch mode {
				case "blocked":
					injection = "if [ ! -f " + currentDirectoryShellQuote(approved) + ` ]; then
printf '%s\n' '{"type":"system","subtype":"init","session_id":"acceptance-blocked"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"acceptance-blocked","structured_output":{"protocol_version":"xgoal.agent-result/v1alpha1","status":"blocked","summary":"which test account should be used?","blockers":["test account required"],"recommended_next_action":"provide a test account"}}'
exit 0
fi
`
					injection += "python3 -c " + currentDirectoryShellQuote(`import json,re,sys
p=json.load(open(re.search(r'(/\S+/packet\.json)',sys.stdin.read()).group(1)))
assert p['prior']['observation']['result']['summary']=='which test account should be used?'
assert p['decisions'][0]['answer']=='use fixture test account'
`) + "\n"
				case "source-write":
					injection = "printf 'unauthorized\\n' >> output.txt\n"
					// The fixture deliberately claims success despite the corrupted source.
					script = strings.Replace(script, " >/dev/null\n", " >/dev/null 2>&1 || true\n", 1)
				case "false-claim":
					// The client observes a healthy service, then the fixture changes only
					// approved test data and emits success. Final assertions must catch this.
					script = strings.Replace(script, " >/dev/null\n", " >/dev/null\nprintf bad > \"$XGOAL_SCENARIO_DIR/bad-response\"\n", 1)
					server := strings.Replace(managedServiceServer, `elif self.path == "/output": data = {"value": pathlib.Path("output.txt").read_text()}`, `elif self.path == "/output": data = {"value": "wrong\n" if (root / "bad-response").exists() else pathlib.Path("output.txt").read_text()}`, 1)
					writeCurrentDirectoryFixture(t, filepath.Join(root, "service-server.py"), server, 0600)
				}
				script = strings.Replace(script, marker, injection+marker, 1)
				writeCurrentDirectoryFixture(t, binary, script, 0700)
				return text
			})
			writeCurrentDirectoryFixture(t, f.release, "release", 0600)
			goalID := "goal_" + strings.ReplaceAll(mode, "-", "_")
			f.invoke(t, "run", "--id", goalID, "--goal", "append accepted output and verify through the running service")
			dsn := (&url.URL{Scheme: "file", Path: filepath.Join(f.daemon.StateDir, "state.db")}).String() + "?mode=ro"
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var goalVersion, gateVersion int64
			var gateID string
			awaitBackgroundCondition(t, 60*time.Second, "final acceptance Gate", func() bool {
				var state string
				if err := db.QueryRow(`SELECT state,version FROM goals WHERE id=?`, goalID).Scan(&state, &goalVersion); err != nil {
					return false
				}
				if state == "COMPLETED" {
					t.Fatal("false or blocked acceptance completed")
				}
				err := db.QueryRow(`SELECT id,version FROM gates WHERE goal_id=? AND reason_code='acceptance_replay_required' AND state='OPEN'`, goalID).Scan(&gateID, &gateVersion)
				return state == "WAITING" && err == nil
			})
			awaitBackgroundCondition(t, 10*time.Second, "all services and acceptance process stopped", func() bool {
				var pending int
				err := db.QueryRow(`SELECT count(*) FROM process_invocations WHERE state NOT IN ('EXITED','TERMINATED')`).Scan(&pending)
				return err == nil && pending == 0
			})
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM final_reports`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("unproven final report %d %v", count, err)
			}
			var observationJSON []byte
			if err := db.QueryRow(`SELECT observation_json FROM effects WHERE effect_type='acceptance'`).Scan(&observationJSON); err != nil {
				t.Fatal(err)
			}
			var observation acceptance.Observation
			if err := json.Unmarshal(observationJSON, &observation); err != nil {
				t.Fatal(err)
			}
			if mode == "blocked" {
				if observation.Result == nil || observation.Result.Status != "blocked" {
					t.Fatal("blocked was not preserved")
				}
				writeCurrentDirectoryFixture(t, approved, "operator prepared account", 0600)
				outputPath := filepath.Join(f.root, "output.txt")
				acceptedOutput, err := os.ReadFile(outputPath)
				if err != nil {
					t.Fatal(err)
				}
				writeCurrentDirectoryFixture(t, outputPath, string(acceptedOutput)+"external edit\n", 0600)
				out, err := invokeCurrentDirectoryCLI(f.binary, f.environment, "--project", f.root, "approve", gateID, "--version", strconv.FormatInt(gateVersion, 10), "--reason", "use fixture test account", "--by", "operator", "--resume", "--owner-version", strconv.FormatInt(goalVersion, 10))
				if err == nil || !strings.Contains(out, "Decision retained") || !strings.Contains(out, "CHECKOUT_WAITING") {
					t.Fatalf("changed scene resumed: %v %s", err, out)
				}
				var retainedState string
				var retainedUsed int
				if err := db.QueryRow(`SELECT state,used,version FROM gates WHERE id=?`, gateID).Scan(&retainedState, &retainedUsed, &gateVersion); err != nil || retainedState != "APPROVED" || retainedUsed != 0 {
					t.Fatalf("decision was lost: %s %d %v", retainedState, retainedUsed, err)
				}
				writeCurrentDirectoryFixture(t, outputPath, string(acceptedOutput), 0600)
				f.invoke(t, "gate", "resume", gateID, "--version", strconv.FormatInt(gateVersion, 10), "--owner-version", strconv.FormatInt(goalVersion, 10))
				_ = f.complete(t, goalID)
				var used int
				if err := db.QueryRow(`SELECT used FROM gates WHERE id=?`, gateID).Scan(&used); err != nil || used != 1 {
					t.Fatalf("replay decision usage %d %v", used, err)
				}
				var requestJSON []byte
				if err := db.QueryRow(`SELECT request_json FROM effects WHERE effect_type='acceptance' ORDER BY rowid DESC LIMIT 1`).Scan(&requestJSON); err != nil {
					t.Fatal(err)
				}
				var request acceptance.Request
				if json.Unmarshal(requestJSON, &request) != nil || len(request.Packet.Decisions) != 1 || request.Packet.Decisions[0].Answer != "use fixture test account" || request.Packet.Prior == nil || request.Packet.Prior.Observation.Result == nil || request.Packet.Prior.Observation.Result.Summary != "which test account should be used?" {
					t.Fatalf("missing exact answer: %s", requestJSON)
				}
			} else if mode == "source-write" {
				if observation.FailureCode != "acceptance_source_changed" {
					t.Fatalf("source write not detected: %+v", observation)
				}
			} else {
				if observation.Result == nil || observation.Result.Status != "completed" {
					t.Fatalf("false claim fixture missing: %+v", observation)
				}
				var failed int
				if err := db.QueryRow(`SELECT count(*) FROM validator_runs WHERE result<>'PASSED'`).Scan(&failed); err != nil || failed == 0 {
					t.Fatalf("final assertion did not reject false claim %d %v", failed, err)
				}
			}
			f.invoke(t, "daemon", "stop", "--timeout", "10s")
			f.stopped = true
		})
	}
}

func TestRealCLIAcceptanceCrashPreservesPersistedBlockedClaimAndBoundsSafeReplay(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-acceptance-crash-")
	if err != nil {
		t.Fatal(err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("acceptance crash fixtures retained: %s", base)
		} else {
			_ = os.RemoveAll(base)
		}
	})
	binary := compileCurrentDirectoryCLI(t, base)
	for _, mode := range []string{"persisted-blocked", "interrupted-safe", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			marker := filepath.Join(base, mode+".acceptance-entered")
			release := filepath.Join(base, mode+".acceptance-release")
			f := newBackgroundFixture(t, base, binary, mode, func(t *testing.T, root, text, roles string) string {
				configured := acceptanceServiceConfiguration(t, root, text, roles)
				cfg, err := config.Load(strings.NewReader(configured))
				if err != nil {
					t.Fatal(err)
				}
				cfg.Acceptance.ReplaySafe = true
				binary := cfg.Agents[2].Command
				data, err := os.ReadFile(binary)
				if err != nil {
					t.Fatal(err)
				}
				script := string(data)
				wait := "printf entered > " + currentDirectoryShellQuote(marker) + "\nwhile [ ! -f " + currentDirectoryShellQuote(release) + " ]; do sleep 0.02; done\n"
				if mode == "persisted-blocked" {
					script = strings.Replace(script, `"status":"completed","summary":"observed real service response"`, `"status":"blocked","summary":"persistent account question"`, 1)
					script = strings.Replace(script, "exit 0 ;;\nesac", wait+"exit 0 ;;\nesac", 1)
				} else {
					script = strings.Replace(script, "./service-client.py api assert ", wait+"./service-client.py api assert ", 1)
				}
				writeCurrentDirectoryFixture(t, binary, script, 0700)
				encoded, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				return string(encoded)
			})
			writeCurrentDirectoryFixture(t, f.release, "planner released", 0600)
			goalID := "goal_" + strings.ReplaceAll(mode, "-", "_")
			f.invoke(t, "run", "--id", goalID, "--goal", "append accepted output and verify the running service")
			awaitBackgroundCondition(t, 60*time.Second, "Acceptance Provider executing", func() bool { _, err := os.Stat(marker); return err == nil })
			dsn := (&url.URL{Scheme: "file", Path: filepath.Join(f.daemon.StateDir, "state.db")}).String() + "?mode=ro"
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var effectID string
			var requestJSON []byte
			if err := db.QueryRow(`SELECT id,request_json FROM effects WHERE effect_type='acceptance'`).Scan(&effectID, &requestJSON); err != nil {
				t.Fatal(err)
			}
			var request acceptance.Request
			if err := json.Unmarshal(requestJSON, &request); err != nil {
				t.Fatal(err)
			}
			var identity supervisor.ProcessIdentity
			if err := db.QueryRow(`SELECT pid,pgid,start_identity FROM process_invocations WHERE id=? AND state='REGISTERED'`, acceptance.ProcessID(request.Packet.ID)).Scan(&identity.PID, &identity.PGID, &identity.StartID); err != nil {
				t.Fatal(err)
			}
			if mode == "persisted-blocked" {
				dir := filepath.Join(f.daemon.StateDir, "adapters", "claude", "acceptances", request.Packet.ID)
				awaitBackgroundCondition(t, 5*time.Second, "complete blocked event fsynced before result.json", func() bool {
					events, err := filepath.Glob(filepath.Join(dir, "events", "*.json"))
					if err != nil {
						return false
					}
					for _, path := range events {
						data, _ := os.ReadFile(path)
						if strings.Contains(string(data), "persistent account question") {
							return true
						}
					}
					return false
				})
				if _, err := os.Stat(filepath.Join(dir, "result.json")); !os.IsNotExist(err) {
					t.Fatalf("fixture missed result-file crash window: %v", err)
				}
			}
			if mode == "cancel" {
				var version int64
				if err := db.QueryRow(`SELECT version FROM goals WHERE id=?`, goalID).Scan(&version); err != nil {
					t.Fatal(err)
				}
				output, cancelErr := invokeCurrentDirectoryCLI(f.binary, f.environment, "--project", f.root, "cancel", goalID, "--version", strconv.FormatInt(version, 10), "--reason", "cancel acceptance")
				var exitErr *exec.ExitError
				if !errors.As(cancelErr, &exitErr) || exitErr.ExitCode() != 4 || !strings.Contains(output, `"state": "CANCELLED"`) {
					t.Fatalf("cancel: %v %s", cancelErr, output)
				}
			} else {
				if err := syscall.Kill(f.daemon.Identity.PID, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				writeCurrentDirectoryFixture(t, release, "release any authorized new invocation", 0600)
				if err := json.Unmarshal([]byte(f.invoke(t, "daemon", "start", "--timeout", "20s")), &f.daemon); err != nil {
					t.Fatal(err)
				}
			}
			awaitBackgroundCondition(t, 10*time.Second, "old Acceptance process reclaimed", func() bool { alive, err := supervisor.ProcessGroupAlive(identity.PGID); return err == nil && !alive })
			if mode == "interrupted-safe" {
				_ = f.complete(t, goalID)
				var invocations int
				if err := db.QueryRow(`SELECT count(*) FROM effects WHERE effect_type='acceptance'`).Scan(&invocations); err != nil || invocations != 2 {
					t.Fatalf("recovery did not create exactly one fresh invocation %d %v", invocations, err)
				}
			} else {
				awaitBackgroundCondition(t, 10*time.Second, "terminal old invocation", func() bool {
					var state string
					err := db.QueryRow(`SELECT state FROM effects WHERE id=?`, effectID).Scan(&state)
					return err == nil && (state == "SUCCEEDED" || state == "FAILED")
				})
				var state string
				if err := db.QueryRow(`SELECT state FROM goals WHERE id=?`, goalID).Scan(&state); err != nil {
					t.Fatal(err)
				}
				expected := "WAITING"
				if mode == "cancel" {
					expected = "CANCELLED"
				}
				if state != expected {
					t.Fatalf("crash/cancel state=%s want=%s", state, expected)
				}
				var observationJSON []byte
				if err := db.QueryRow(`SELECT observation_json FROM effects WHERE id=?`, effectID).Scan(&observationJSON); err != nil {
					t.Fatal(err)
				}
				if mode == "persisted-blocked" && (!strings.Contains(string(observationJSON), `"status":"blocked"`) || strings.Contains(string(observationJSON), `"failure_code":"acceptance_interrupted"`)) {
					t.Fatalf("durable blocked claim erased: %s", observationJSON)
				}
				var calls int
				if err := db.QueryRow(`SELECT count(*) FROM effects WHERE effect_type='acceptance'`).Scan(&calls); err != nil || calls != 1 {
					t.Fatalf("blocked/cancelled call replayed %d %v", calls, err)
				}
			}
			f.invoke(t, "daemon", "stop", "--timeout", "10s")
			f.stopped = true
		})
	}
}
