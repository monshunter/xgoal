package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/supervisor"
)

func TestRealCLIManagedServicesBusinessAssertionsAndCleanup(t *testing.T) {
	runRealCLICurrentDirectoryGoals(t, true)
}

func managedServiceConfiguration(t *testing.T, root, text, rolesPath string) string {
	t.Helper()
	cfg, err := config.Load(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Runtime.ProjectNetwork = "allow"
	cfg.Bootstrap.Commands = []config.Command{{ID: "prepare", Argv: []string{"sh", "-c", `test "$XGOAL_PROJECT_VISIBLE" = fixture && test -z "${ANTHROPIC_AUTH_TOKEN:-}" && printf 'bootstrap ready\n'`}, TrustedFiles: []string{"xgoal.yaml"}, Timeout: config.Duration{Duration: 10 * time.Second}, Environment: config.Environment{Allow: []string{"XGOAL_PROJECT_VISIBLE"}}}}
	for _, role := range []string{"api", "db"} {
		s := config.Service{ID: role, Argv: []string{"python3", "service-server.py", role}, Network: "allow", Readiness: config.Readiness{Argv: []string{"python3", "service-client.py", role, "ready"}, Timeout: config.Duration{Duration: 10 * time.Second}, Interval: config.Duration{Duration: 50 * time.Millisecond}}, StopGracePeriod: config.Duration{Duration: 2 * time.Second}}
		if role == "api" {
			s.DependsOn = []string{"db"}
		}
		cfg.Services = append(cfg.Services, s)
	}
	cfg.Validators[0].Services = []string{"api"}
	cfg.Validators[0].Argv = []string{"python3", "service-client.py", "api", "assert", rolesPath}
	cfg.Validators[0].TrustedFiles = []string{"service-client.py"}
	cfg.Validators[0].Description = "Call the running API and assert every returned output line equals accepted."
	cfg.Validators[0].ScenarioIDs = []string{"output-workflow"}
	cfg.Scenarios = []config.Scenario{{ID: "output-workflow", Description: "Fetch the result through the running API", Steps: []string{"Fetch /output", "Assert accepted lines and retain the response"}, Services: []string{"api"}, Validators: []string{cfg.Validators[0].ID}, ArtifactPaths: []string{"response.json"}}}
	writeCurrentDirectoryFixture(t, filepath.Join(root, "service-server.py"), managedServiceServer, 0600)
	writeCurrentDirectoryFixture(t, filepath.Join(root, "service-client.py"), managedServiceClient, 0600)
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const managedServiceServer = `import http.server, json, os, pathlib, signal, sys, urllib.request
role = sys.argv[1]
root = pathlib.Path(os.environ["XGOAL_SCENARIO_DIR"])
assert "XGOAL_PROJECT_VISIBLE" not in os.environ
assert "ANTHROPIC_AUTH_TOKEN" not in os.environ
if role == "api":
    endpoint = json.loads((root / "db.json").read_text())["url"]
    assert json.load(urllib.request.urlopen(endpoint + "/health", timeout=2))["role"] == "db"
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health": data = {"role": role, "ready": True}
        elif self.path == "/output": data = {"value": pathlib.Path("output.txt").read_text()}
        else: self.send_error(404); return
        encoded = json.dumps(data).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers(); self.wfile.write(encoded)
    def log_message(self, *args): pass
server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
(root / (role + ".json")).write_text(json.dumps({"url": "http://127.0.0.1:%s" % server.server_port}))
def stop(*args):
    with (root / "stopped").open("a") as out: out.write(role + "\n")
    raise SystemExit(0)
signal.signal(signal.SIGTERM, stop)
print("TOKEN=fixture-service-secret", flush=True)
server.serve_forever()
`

const managedServiceClient = `import json, os, pathlib, sys, urllib.request
role, mode = sys.argv[1:3]
root = pathlib.Path(os.environ["XGOAL_SCENARIO_DIR"])
endpoint = json.loads((root / (role + ".json")).read_text())["url"]
assert "XGOAL_PROJECT_VISIBLE" not in os.environ
assert "ANTHROPIC_AUTH_TOKEN" not in os.environ
if mode == "ready":
    result = json.load(urllib.request.urlopen(endpoint + "/health", timeout=2))
    assert result == {"role": role, "ready": True}, result
else:
    result = json.load(urllib.request.urlopen(endpoint + "/output", timeout=2))
    value = result["value"]
    assert value and value.endswith("\n") and all(line == "accepted" for line in value.splitlines()), result
    (root / "response.json").write_text(json.dumps(result))
    with open(sys.argv[3], "a") as out: out.write("validator\t" + os.getcwd() + "\n")
print("business assertion passed" if mode == "assert" else "ready")
`

func assertManagedServicesStopped(t *testing.T, stateDir string) {
	t.Helper()
	endpoints, err := filepath.Glob(filepath.Join(stateDir, "environments", "*", "scenario", "api.json"))
	if err != nil || len(endpoints) != 4 {
		t.Fatalf("expected change/final environments for both Goals: %v %v", endpoints, err)
	}
	for _, path := range endpoints {
		stopped, err := os.ReadFile(filepath.Join(filepath.Dir(path), "stopped"))
		if err != nil || string(stopped) != "api\ndb\n" {
			t.Fatalf("services did not stop in reverse dependency order: %q %v", stopped, err)
		}
		for _, id := range []string{"api", "db"} {
			data, err := os.ReadFile(filepath.Join(filepath.Dir(path), id+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var endpoint struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(data, &endpoint); err != nil {
				t.Fatal(err)
			}
			conn, err := net.DialTimeout("tcp", strings.TrimPrefix(endpoint.URL, "http://"), time.Second)
			if err == nil {
				_ = conn.Close()
				t.Fatalf("owned service still accepts connections: %s", endpoint.URL)
			}
			log, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(path)), "logs", id+".stdout.log"))
			if err != nil || !strings.Contains(string(log), "TOKEN=[REDACTED]") || strings.Contains(string(log), "fixture-service-secret") {
				t.Fatalf("service diagnostics missing or unsafe: %v", err)
			}
		}
	}
	logs, err := filepath.Glob(filepath.Join(stateDir, "environments", "*", "logs", "commands", "bootstrap_prepare.stdout.log"))
	if err != nil || len(logs) != 6 {
		t.Fatalf("missing bootstrap diagnostics: %v %v", logs, err)
	}
	for _, path := range logs {
		if data, err := os.ReadFile(path); err != nil || string(data) != "bootstrap ready\n" {
			t.Fatalf("bootstrap diagnostics: %q %v", data, err)
		}
	}
}

func TestRealCLIServiceFailureAndInterruptionPreserveSafety(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-service-failure-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("service failure fixture retained: %s", base)
		} else {
			_ = os.RemoveAll(base)
		}
	})
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	binary := compileCurrentDirectoryCLI(t, base)
	for _, failure := range []string{"bootstrap", "startup", "probe", "business", "missing-artifact", "cancel", "crash"} {
		t.Run(failure, func(t *testing.T) {
			f := newBackgroundFixture(t, base, binary, failure, func(t *testing.T, root, text, roles string) string {
				text = managedServiceConfiguration(t, root, text, roles)
				cfg, err := config.Load(strings.NewReader(text))
				if err != nil {
					t.Fatal(err)
				}
				switch failure {
				case "bootstrap":
					cfg.Bootstrap.Commands[0].Argv = []string{"sh", "-c", "printf 'fixture bootstrap failed\n'; exit 7"}
				case "startup":
					cfg.Services[1].Argv = []string{"python3", "-c", "import sys; print('fixture startup failed'); sys.exit(7)"}
				case "probe":
					cfg.Services[1].Readiness.Argv = []string{"sh", "-c", "printf 'fixture readiness failed\n' >&2; exit 7"}
					cfg.Services[1].Readiness.TrustedFiles = []string{"xgoal.yaml"}
					cfg.Services[1].Readiness.Timeout = config.Duration{Duration: 500 * time.Millisecond}
				case "business":
					writeCurrentDirectoryFixture(t, filepath.Join(root, "service-server.py"), strings.Replace(managedServiceServer, `pathlib.Path("output.txt").read_text()`, `"wrong"`, 1), 0600)
				case "missing-artifact":
					writeCurrentDirectoryFixture(t, filepath.Join(root, "service-client.py"), strings.Replace(managedServiceClient, `    (root / "response.json").write_text(json.dumps(result))`, `    # deliberately omit the declared result artifact`, 1), 0600)
				case "cancel", "crash":
					cfg.Validators[0].Timeout = config.Duration{Duration: 60 * time.Second}
					client := strings.Replace(managedServiceClient, `else:
    result = json.load`, `else:
    import time
    (root / "client-entered").write_text("entered")
    time.sleep(60)
    result = json.load`, 1)
					writeCurrentDirectoryFixture(t, filepath.Join(root, "service-client.py"), client, 0600)
				}
				data, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			})
			writeCurrentDirectoryFixture(t, f.release, "release", 0600)
			goalID := "goal_" + failure
			f.invoke(t, "run", "--id", goalID, "--goal", "append an accepted line")
			dsn := (&url.URL{Scheme: "file", Path: filepath.Join(f.daemon.StateDir, "state.db")}).String() + "?mode=ro"
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			readStatus := func() (state string, version int64, body string) {
				body, err := invokeCurrentDirectoryCLI(binary, f.environment, "--project", f.root, "status", goalID)
				var exit *exec.ExitError
				if err != nil && (!errors.As(err, &exit) || (exit.ExitCode() != 3 && exit.ExitCode() != 4)) {
					t.Fatalf("status: %v %s", err, body)
				}
				var view struct {
					State     string `json:"state"`
					Version   int64  `json:"version"`
					FinalTree string `json:"final_tree"`
				}
				if err := json.Unmarshal([]byte(body), &view); err != nil {
					t.Fatal(err)
				}
				if view.FinalTree != "" || view.State == "COMPLETED" {
					t.Fatalf("invalid completion: %s", body)
				}
				return view.State, view.Version, body
			}
			if failure == "cancel" || failure == "crash" {
				awaitBackgroundCondition(t, 30*time.Second, "business validator entered", func() bool {
					paths, _ := filepath.Glob(filepath.Join(f.daemon.StateDir, "environments", "*", "scenario", "client-entered"))
					return len(paths) > 0
				})
				rows, err := db.Query(`SELECT pid,pgid,start_identity FROM process_invocations WHERE goal_id=? AND state='REGISTERED'`, goalID)
				if err != nil {
					t.Fatal(err)
				}
				var processes []supervisor.ProcessIdentity
				for rows.Next() {
					var p supervisor.ProcessIdentity
					if err := rows.Scan(&p.PID, &p.PGID, &p.StartID); err != nil {
						t.Fatal(err)
					}
					processes = append(processes, p)
				}
				_ = rows.Close()
				if len(processes) < 3 {
					t.Fatalf("services/client lack durable ownership: %+v", processes)
				}
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					for _, p := range processes {
						_ = supervisor.TerminateOwnedGroup(ctx, p, 100*time.Millisecond)
					}
				})
				if failure == "crash" {
					if err := syscall.Kill(f.daemon.Identity.PID, syscall.SIGKILL); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal([]byte(f.invoke(t, "daemon", "start", "--timeout", "20s")), &f.daemon); err != nil {
						t.Fatal(err)
					}
				} else {
					_, version, _ := readStatus()
					body, err := invokeCurrentDirectoryCLI(binary, f.environment, "--project", f.root, "cancel", goalID, "--version", fmt.Sprint(version), "--reason", "stop owned test services")
					var exit *exec.ExitError
					if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 4) {
						t.Fatalf("cancel: %v %s", err, body)
					}
				}
				awaitBackgroundCondition(t, 15*time.Second, "all old process groups stopped", func() bool {
					for _, p := range processes {
						alive, err := supervisor.ProcessGroupAlive(p.PGID)
						if err != nil {
							t.Fatal(err)
						}
						if alive {
							return false
						}
					}
					return true
				})
				stops, err := filepath.Glob(filepath.Join(f.daemon.StateDir, "environments", "*", "scenario", "stopped"))
				if err != nil || len(stops) != 1 {
					t.Fatalf("service stop records = %v: %v", stops, err)
				}
				if data, err := os.ReadFile(stops[0]); err != nil || string(data) != "api\ndb\n" {
					t.Fatalf("interruption cleanup order = %q: %v", data, err)
				}
			}
			want := "WAITING"
			if failure == "cancel" {
				want = "CANCELLED"
			}
			awaitBackgroundCondition(t, 60*time.Second, "safe terminal/waiting state", func() bool { state, _, _ := readStatus(); return state == want })
			awaitBackgroundCondition(t, 15*time.Second, "owned process records resolved", func() bool {
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM process_invocations WHERE state IN ('INTENT','REGISTERED','UNKNOWN')`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				return count == 0
			})
			index, err := os.ReadFile(filepath.Join(f.root, ".git", "index"))
			if err != nil || !bytes.Equal(index, f.index) {
				t.Fatal("failure changed user index")
			}
			if currentDirectoryGit(t, f.root, "rev-parse", "HEAD") != f.head {
				t.Fatal("failure changed user HEAD")
			}
			if failure != "bootstrap" {
				if data, err := os.ReadFile(filepath.Join(f.root, "output.txt")); err != nil || string(data) != "accepted\n" {
					t.Fatalf("failure discarded source: %q %v", data, err)
				}
			}
			if expected := map[string]string{"bootstrap": "fixture bootstrap failed", "startup": "fixture startup failed", "probe": "fixture readiness failed"}[failure]; expected != "" {
				found := false
				_ = filepath.WalkDir(filepath.Join(f.daemon.StateDir, "environments"), func(path string, entry os.DirEntry, err error) error {
					if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".log") {
						if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), expected) {
							found = true
						}
					}
					return err
				})
				if !found {
					t.Fatalf("missing failure diagnostics: %s", expected)
				}
			}
			f.invoke(t, "daemon", "stop", "--timeout", "10s")
			f.stopped = true
			t.Logf("%s preserved source and stopped all owned processes", failure)
		})
	}
}
