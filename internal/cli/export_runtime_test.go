package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/exporter"
	"github.com/monshunter/xgoal/internal/invocation"
)

func TestRealCLIExportsLiveWorkWhileLogsAndLeaseContinue(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-live-export-")
	if err != nil {
		t.Fatal(err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("live export fixture retained: %s", base)
		} else {
			os.RemoveAll(base)
		}
	})
	binary := compileCurrentDirectoryCLI(t, base)
	f := newBackgroundFixture(t, base, binary, "live", func(t *testing.T, root, text, roles string) string {
		text = acceptanceServiceConfiguration(t, root, text, roles)
		cfg, err := config.Load(strings.NewReader(text))
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(cfg.Agents[0].Command)
		if err != nil {
			t.Fatal(err)
		}
		marker := "printf 'implementer\\t"
		if !strings.Contains(string(data), marker) {
			t.Fatal("implementer fixture marker absent")
		}
		loop := "while :; do printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"still working\"}}'; sleep 0.02; done\n"
		writeCurrentDirectoryFixture(t, cfg.Agents[0].Command, strings.Replace(string(data), marker, loop+marker, 1), 0700)
		return text
	})
	writeCurrentDirectoryFixture(t, f.release, "release", 0600)
	f.invoke(t, "run", "--id", "goal_live_export", "--goal", "append accepted output and verify through real services")
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(f.daemon.StateDir, "state.db")}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id, heartbeat string
	var beforeCursor int64
	awaitBackgroundCondition(t, 30*time.Second, "live Work output and lease", func() bool {
		if err := db.QueryRow(`SELECT id,cursor FROM invocations WHERE role='implementer' AND status='running'`).Scan(&id, &beforeCursor); err != nil || beforeCursor < 3 {
			return false
		}
		return db.QueryRow(`SELECT heartbeat_at FROM leases WHERE state='ACTIVE'`).Scan(&heartbeat) == nil
	})
	destination := filepath.Join(base, "audit")
	command := exec.Command(binary, "--project", f.root, "export", "goal_live_exp", "--output", destination)
	command.Env = f.environment
	var output strings.Builder
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	// This independent CLI status request runs while the export is in flight.
	status := f.invoke(t, "status", "goal_live_export")
	if !strings.Contains(status, `"state": "RUNNING"`) {
		t.Fatalf("Goal stopped during export: %s", status)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("live export: %v %s", err, &output)
	}
	data, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest exporter.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	var boundary int64
	for _, log := range manifest.Logs {
		if log.ID == id {
			boundary = log.Stdout
			if log.Status != "running" {
				t.Fatalf("snapshot was not live: %+v", log)
			}
		}
	}
	if boundary < beforeCursor {
		t.Fatal("export omitted previously indexed output")
	}
	awaitBackgroundCondition(t, 8*time.Second, "continued heartbeat and new log bytes", func() bool {
		var cursor int64
		var after string
		return db.QueryRow(`SELECT cursor FROM invocations WHERE id=?`, id).Scan(&cursor) == nil && cursor > boundary && db.QueryRow(`SELECT heartbeat_at FROM leases WHERE state='ACTIVE'`).Scan(&after) == nil && after > heartbeat
	})
	var version int64
	if err := db.QueryRow(`SELECT version FROM goals WHERE id='goal_live_export'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	result, err := invokeCurrentDirectoryCLI(binary, f.environment, "--project", f.root, "cancel", "goal_live_export", "--version", strconv.FormatInt(version, 10))
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 4 {
		t.Fatalf("cancel failed: %v %s", err, result)
	}
	awaitBackgroundCondition(t, 10*time.Second, "cancelled export producer stopped", func() bool {
		var n int
		return db.QueryRow(`SELECT count(*) FROM process_invocations WHERE state NOT IN ('EXITED','TERMINATED')`).Scan(&n) == nil && n == 0
	})
	snapshotDSN := (&url.URL{Scheme: "file", Path: filepath.Join(destination, "snapshot/state.db")}).String() + "?mode=ro&immutable=1"
	snapshot, err := sql.Open("sqlite", snapshotDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var frozenState string
	if err := snapshot.QueryRow(`SELECT state FROM goals WHERE id='goal_live_export'`).Scan(&frozenState); err != nil || frozenState != "RUNNING" {
		t.Fatalf("live snapshot changed: %s %v", frozenState, err)
	}
	dir, _ := invocation.Directory("codex-cli", "implementer", id)
	if _, err := os.Stat(filepath.Join(destination, "files", dir, "events", fmt.Sprintf("%06d.json", boundary+1))); !os.IsNotExist(err) {
		t.Fatal("export followed later log bytes")
	}
	f.invoke(t, "daemon", "stop", "--timeout", "10s")
	f.stopped = true
}

func TestRealCLIExportsCompletedServiceEvidenceAndRejectsCorruption(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-export-")
	if err != nil {
		t.Fatal(err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("export fixture retained: %s", base)
		} else {
			os.RemoveAll(base)
		}
	})
	binary := compileCurrentDirectoryCLI(t, base)
	f := newBackgroundFixture(t, base, binary, "export", acceptanceServiceConfiguration)
	writeCurrentDirectoryFixture(t, f.release, "release", 0600)
	f.invoke(t, "run", "--id", "goal_export", "--goal", "append accepted output and verify through real services")
	_ = f.complete(t, "goal_export")
	destination := filepath.Join(base, "audit")
	var result exporter.Result
	if err := json.Unmarshal([]byte(f.invoke(t, "export", "goal_exp", "--output", destination)), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "complete" || result.Path != destination {
		t.Fatalf("result=%+v", result)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if invocation.SHA256(manifestBytes) != result.ManifestSHA256 {
		t.Fatal("manifest checksum")
	}
	var manifest exporter.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, o := range manifest.Owners {
		kinds[o.Kind] = true
	}
	for _, kind := range []string{"work_packet", "patch", "receipt", "review_packet", "review_result", "scenario", "report", "workspace", "environment", "planner", "acceptance", "invocation"} {
		if !kinds[kind] {
			t.Fatalf("closure omitted %s: %+v", kind, kinds)
		}
	}
	for _, file := range manifest.Files {
		data, err := os.ReadFile(filepath.Join(destination, file.Path))
		if err != nil || invocation.SHA256(data) != file.SHA256 {
			t.Fatalf("copy %s: %v", file.Path, err)
		}
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(destination, "snapshot/state.db")}).String() + "?mode=ro&immutable=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	if err := db.QueryRow("SELECT state FROM goals WHERE id='goal_export'").Scan(&state); err != nil || state != "COMPLETED" {
		t.Fatalf("snapshot state=%s %v", state, err)
	}
	// Corrupt an owned sealed file. The next export must fail without changing
	// either the previously complete export or the completed Goal's authority.
	var damaged string
	for _, file := range manifest.Files {
		if strings.Contains(file.Path, "validator/logs/") {
			damaged = filepath.Join(f.daemon.StateDir, strings.TrimPrefix(file.Path, "files/"))
			break
		}
	}
	if damaged == "" {
		t.Fatal("no validator log exported")
	}
	if err := os.WriteFile(damaged, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	badDestination := filepath.Join(base, "corrupt-audit")
	output, err := invokeCurrentDirectoryCLI(binary, f.environment, "--project", f.root, "export", "goal_exp", "--output", badDestination)
	if err == nil || !strings.Contains(output, "EXPORT_INCOMPLETE") {
		t.Fatalf("corrupt export=%v %s", err, output)
	}
	if _, err := os.Stat(badDestination); !os.IsNotExist(err) {
		t.Fatal("corrupt output published")
	}
	const sentinel = "export-secret-sentinel"
	for _, file := range manifest.Files {
		if strings.HasPrefix(file.Path, "files/packets/") {
			path := filepath.Join(f.daemon.StateDir, strings.TrimPrefix(file.Path, "files/"))
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(`{"API_KEY=`+sentinel+`":1}`), 0400); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0400); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	output, err = invokeCurrentDirectoryCLI(binary, f.environment, "--project", f.root, "export", "goal_exp", "--output", filepath.Join(base, "private-error-audit"))
	if err == nil || !strings.Contains(output, "REDACTED") || strings.Contains(output, sentinel) {
		t.Fatalf("unredacted export error: %v %s", err, output)
	}
	liveDSN := (&url.URL{Scheme: "file", Path: filepath.Join(f.daemon.StateDir, "state.db")}).String() + "?mode=ro"
	liveDB, err := sql.Open("sqlite", liveDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer liveDB.Close()
	var leaked, redacted int
	if err := liveDB.QueryRow(`SELECT count(*) FROM idempotency_records WHERE instr(CAST(response_json AS TEXT),?)>0`, sentinel).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if err := liveDB.QueryRow(`SELECT count(*) FROM idempotency_records WHERE instr(CAST(response_json AS TEXT),'REDACTED')>0`).Scan(&redacted); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 || redacted == 0 {
		t.Fatalf("idempotency response redaction: leaked=%d redacted=%d", leaked, redacted)
	}
	f.invoke(t, "daemon", "stop", "--timeout", "10s")
	f.stopped = true
}
