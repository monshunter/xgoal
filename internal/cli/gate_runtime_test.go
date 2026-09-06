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
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/config"
)

// These bounded Provider executables read the real immutable packet delivered by
// each native adapter. A success without the prior question and answer fails.
func gateProviderProbe(role, receipt string) string {
	code := `import json,re,sys,pathlib
role,receipt=sys.argv[1:]
p=json.load(open(re.search(r'(/\S+\.json)',sys.stdin.read()).group(1)))
prior=p.get('prior') if role=='planner' else p.get('prior_attempt')
if not prior:
    result={'protocol_version':'xgoal.planner-proposal/v1alpha1','contract':{},'plan':{},'ambiguities':['which locale should be used?']} if role=='planner' else {'protocol_version':'xgoal.agent-result/v1alpha1','status':'blocked','summary':'which test account should be used?','blockers':['test account required']}
    print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':json.dumps(result)}}))
    print(json.dumps({'type':'turn.completed'}))
    sys.exit(10)
answer=prior['decision']['answer'] if role=='planner' else prior['decisions'][0]['answer']
failure=prior['observation']['failure_reason'] if role=='planner' else prior['failure_error']
assert answer==('use zh-CN' if role=='planner' else 'use fixture account'),answer
assert ('which locale' if role=='planner' else 'which test account') in failure,failure
pathlib.Path(receipt).write_text('prior question and scoped answer read\n')
`
	return "if python3 -c " + currentDirectoryShellQuote(code) + " " + currentDirectoryShellQuote(role) + " " + currentDirectoryShellQuote(receipt) + "; then :; else code=$?; if [ \"$code\" = 10 ]; then exit 0; else exit \"$code\"; fi; fi\n"
}

func TestRealCLIGateContinuationPlannerAndWork(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-gate-continuation-")
	if err != nil {
		t.Fatal(err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("Gate continuation fixture retained: %s", base)
		} else {
			os.RemoveAll(base)
		}
	})
	binary := compileCurrentDirectoryCLI(t, base)
	f := newBackgroundFixture(t, base, binary, "answered", func(t *testing.T, root, text, roles string) string {
		text = acceptanceServiceConfiguration(t, root, text, roles)
		cfg, err := config.Load(strings.NewReader(text))
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(cfg.Agents[0].Command)
		if err != nil {
			t.Fatal(err)
		}
		script := string(data)
		for _, role := range []string{"planner", "implementer"} {
			marker := "printf '" + role + "\\t"
			if !strings.Contains(script, marker) {
				t.Fatalf("missing role marker %s", role)
			}
			script = strings.Replace(script, marker, gateProviderProbe(role, filepath.Join(base, role+".read"))+marker, 1)
		}
		writeCurrentDirectoryFixture(t, cfg.Agents[0].Command, script, 0700)
		return text
	})
	writeCurrentDirectoryFixture(t, f.release, "release", 0600)
	f.invoke(t, "run", "--id", "goal_answered", "--goal", "append accepted output and verify through real services")
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(f.daemon.StateDir, "state.db")}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, owner := range []string{"planner", "work"} {
		var gateID, workID string
		var gateVersion, ownerVersion int64
		awaitBackgroundCondition(t, 30*time.Second, owner+" asks for input", func() bool {
			reason := "planner_failed"
			if owner == "work" {
				reason = "agent_blocked"
			}
			err := db.QueryRow(`SELECT id,COALESCE(work_item_id,''),version FROM gates WHERE goal_id='goal_answered' AND state='OPEN' AND reason_code=?`, reason).Scan(&gateID, &workID, &gateVersion)
			if err != nil {
				return false
			}
			var pending int
			if err := db.QueryRow(`SELECT count(*) FROM process_invocations WHERE state IN ('INTENT','REGISTERED','UNKNOWN')`).Scan(&pending); err != nil || pending != 0 {
				return false
			}
			if owner == "planner" {
				err = db.QueryRow(`SELECT version FROM goals WHERE id='goal_answered'`).Scan(&ownerVersion)
			} else {
				err = db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&ownerVersion)
			}
			return err == nil
		})
		var gateView struct {
			ID      string
			Version int64
		}
		if err := json.Unmarshal([]byte(f.invoke(t, "gate", "get", gateID)), &gateView); err != nil || gateView.ID != gateID || gateView.Version != gateVersion {
			t.Fatalf("gate get cannot supply the decision version: %+v %v", gateView, err)
		}
		if owner == "work" {
			var workView struct {
				ID      string
				Version int64
			}
			output, err := invokeCurrentDirectoryCLI(f.binary, f.environment, "--project", f.root, "work", "get", workID)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 3 {
				t.Fatalf("waiting work get exit=%v output=%s", err, output)
			}
			if err := json.Unmarshal([]byte(output), &workView); err != nil || workView.ID != workID || workView.Version != ownerVersion {
				t.Fatalf("work get cannot supply the owner version: %+v %v", workView, err)
			}
		}
		answer := "use zh-CN"
		if owner == "work" {
			answer = "use fixture account"
		}
		f.invoke(t, "approve", gateID, "--version", strconv.FormatInt(gateVersion, 10), "--reason", answer, "--resume", "--owner-version", strconv.FormatInt(ownerVersion, 10))
		var used, decisions int
		if err := db.QueryRow(`SELECT used FROM gates WHERE id=?`, gateID).Scan(&used); err != nil || used != 1 {
			t.Fatalf("consumption=%d %v", used, err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM events WHERE aggregate_type='gate' AND aggregate_id=? AND event_type='GateDecided'`, gateID).Scan(&decisions); err != nil || decisions != 1 {
			t.Fatalf("decision count=%d %v", decisions, err)
		}
	}
	_ = f.complete(t, "goal_answered")
	for _, role := range []string{"planner", "implementer"} {
		data, err := os.ReadFile(filepath.Join(base, role+".read"))
		if err != nil || !strings.Contains(string(data), "scoped answer") {
			t.Fatalf("%s did not consume context: %s %v", role, data, err)
		}
	}
	f.invoke(t, "daemon", "stop", "--timeout", "10s")
	f.stopped = true
}
