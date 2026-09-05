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
	"syscall"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/app"
	"github.com/monshunter/xgoal/internal/project"
	"github.com/monshunter/xgoal/internal/report"
	"github.com/monshunter/xgoal/internal/supervisor"
)

type backgroundFixture struct {
	binary, root, release, entered string
	environment                    []string
	daemon                         app.StartResult
	stopped                        bool
	head, worktrees                string
	index                          []byte
}

func TestDaemonCrashDuringPlanningReclaimsOldProcessAndCompletesNewGeneration(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-planning-crash-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	binary := compileCurrentDirectoryCLI(t, base)
	f := newBackgroundFixture(t, base, binary, "crash")
	f.invoke(t, "run", "--id", "goal_crash", "--goal", "append one accepted line")
	awaitBackgroundCondition(t, 10*time.Second, "original planner started", func() bool { _, err := os.Stat(f.entered); return err == nil })
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(f.daemon.StateDir, "state.db")}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	var identity supervisor.ProcessIdentity
	err = db.QueryRow(`SELECT pid,pgid,start_identity FROM process_invocations WHERE owner_kind='planning' AND goal_id='goal_crash' AND state='REGISTERED'`).Scan(&identity.PID, &identity.PGID, &identity.StartID)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = supervisor.TerminateOwnedGroup(ctx, identity, 100*time.Millisecond)
	})
	// This PID was obtained from the fixture daemon's authenticated handshake.
	if err := syscall.Kill(f.daemon.Identity.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	previousInstance := f.daemon.Identity.InstanceID
	if err := json.Unmarshal([]byte(f.invoke(t, "daemon", "start", "--timeout", "20s")), &f.daemon); err != nil {
		t.Fatal(err)
	}
	if f.daemon.Identity.InstanceID == previousInstance {
		t.Fatal("restart reused the dead daemon identity")
	}
	awaitBackgroundCondition(t, 10*time.Second, "new planning invocation", func() bool {
		data, err := os.ReadFile(f.entered)
		return err == nil && strings.Count(string(data), "entered\n") == 2
	})
	if alive, err := supervisor.ProcessGroupAlive(identity.PGID); err != nil || alive {
		t.Fatalf("old planner still executing after restart: %t %v", alive, err)
	}
	var status struct {
		Generation int64  `json:"planning_generation"`
		State      string `json:"planning_state"`
	}
	if err := json.Unmarshal([]byte(f.invoke(t, "status", "goal_crash")), &status); err != nil {
		t.Fatal(err)
	}
	if status.Generation != 2 || status.State != "EXECUTING" {
		t.Fatalf("recovery=%+v", status)
	}
	writeCurrentDirectoryFixture(t, f.release, "release", 0600)
	result := f.complete(t, "goal_crash")
	f.invoke(t, "daemon", "stop", "--timeout", "10s")
	f.stopped = true
	assertCurrentDirectoryEvidence(t, f.daemon.StateDir, []report.Report{result})
	t.Logf("crashed planner pgid=%d reclaimed; generation=%d completed tree=%s", identity.PGID, status.Generation, result.Final.Tree)
}

func (fixture *backgroundFixture) invoke(t *testing.T, args ...string) string {
	t.Helper()
	output, err := invokeCurrentDirectoryCLI(fixture.binary, fixture.environment, append([]string{"--project", fixture.root}, args...)...)
	if err != nil {
		t.Fatalf("CLI %v: %v\n%s", args, err, output)
	}
	return output
}

func newBackgroundFixture(t *testing.T, base, binary, name string) *backgroundFixture {
	t.Helper()
	f := &backgroundFixture{binary: binary, root: filepath.Join(base, name), release: filepath.Join(base, name+".release"), entered: filepath.Join(base, name+".entered")}
	cliRepository(t, f.root)
	writeCurrentDirectoryFixture(t, filepath.Join(f.root, "README.md"), "fixture\n", 0600)
	currentDirectoryGit(t, f.root, "add", "README.md")
	currentDirectoryGit(t, f.root, "-c", "user.name=Fixture", "-c", "user.email=fixture@invalid", "commit", "-q", "-m", "baseline")
	bin := filepath.Join(base, name+"-bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	roles := filepath.Join(base, name+".roles")
	currentDirectoryProviders(t, bin, roles)
	script, err := os.ReadFile(filepath.Join(bin, "codex"))
	if err != nil {
		t.Fatal(err)
	}
	marker := "  *\" --sandbox read-only \"*)\n"
	wait := "printf 'entered\\n' >> " + currentDirectoryShellQuote(f.entered) + "\nwhile [ ! -f " + currentDirectoryShellQuote(f.release) + " ]; do sleep 0.02; done\n"
	if !strings.Contains(string(script), marker) {
		t.Fatal("planner fixture marker missing")
	}
	writeCurrentDirectoryFixture(t, filepath.Join(bin, "codex"), strings.Replace(string(script), marker, marker+wait, 1), 0700)
	for _, entry := range project.GitEnvironment() {
		if !strings.HasPrefix(entry, "XGOAL_") && !strings.HasPrefix(entry, "PATH=") {
			f.environment = append(f.environment, entry)
		}
	}
	f.environment = append(f.environment, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "XGOAL_RUNTIME_DIR="+filepath.Join(base, "runtime"))
	f.invoke(t, "init")
	configuration := strings.ReplaceAll(currentDirectoryConfiguration(bin, roles), "timeout: 20s", "timeout: 90s")
	writeCurrentDirectoryFixture(t, filepath.Join(f.root, "xgoal.yaml"), configuration, 0600)
	currentDirectoryGit(t, f.root, "add", "xgoal.yaml", ".xgoalignore", ".gitignore")
	currentDirectoryGit(t, f.root, "-c", "user.name=Fixture", "-c", "user.email=fixture@invalid", "commit", "-q", "-m", "configuration")
	f.head = currentDirectoryGit(t, f.root, "rev-parse", "HEAD")
	f.worktrees = currentDirectoryGit(t, f.root, "worktree", "list", "--porcelain")
	f.index, err = os.ReadFile(filepath.Join(f.root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(f.invoke(t, "daemon", "start", "--timeout", "20s")), &f.daemon); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !f.stopped {
			output, err := invokeCurrentDirectoryCLI(f.binary, f.environment, "--project", f.root, "daemon", "stop", "--timeout", "10s")
			if err != nil {
				t.Errorf("cleanup %s: %v %s", name, err, output)
			}
		}
	})
	return f
}

func awaitBackgroundCondition(t *testing.T, timeout time.Duration, description string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timed out: " + description)
}

func (f *backgroundFixture) complete(t *testing.T, goalID string) report.Report {
	t.Helper()
	awaitBackgroundCondition(t, 60*time.Second, "Goal completion "+goalID, func() bool {
		output, err := invokeCurrentDirectoryCLI(f.binary, f.environment, "--project", f.root, "status", goalID)
		if err != nil {
			t.Fatalf("Goal stopped progressing: %v %s", err, output)
		}
		var view struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal([]byte(output), &view); err != nil {
			t.Fatal(err)
		}
		return view.State == "COMPLETED"
	})
	var response struct {
		Report report.Report `json:"json"`
		Tree   string        `json:"tree"`
	}
	if err := json.Unmarshal([]byte(f.invoke(t, "report", goalID)), &response); err != nil {
		t.Fatal(err)
	}
	tree := currentDirectoryGit(t, f.root, "rev-parse", "refs/xgoal/goals/"+goalID+"/integration^{tree}")
	if tree != response.Tree || tree != response.Report.Final.Tree || response.Report.Final.EvidenceSetID == "" {
		t.Fatalf("report binding=%+v", response)
	}
	output, err := os.ReadFile(filepath.Join(f.root, "output.txt"))
	if err != nil || string(output) != "accepted\n" {
		t.Fatalf("result=%q %v", output, err)
	}
	index, err := os.ReadFile(filepath.Join(f.root, ".git", "index"))
	if err != nil || !bytes.Equal(index, f.index) || currentDirectoryGit(t, f.root, "rev-parse", "HEAD") != f.head || currentDirectoryGit(t, f.root, "worktree", "list", "--porcelain") != f.worktrees {
		t.Fatal("user Git state changed")
	}
	return response.Report
}

func TestTwoProjectGoalsContinueIndependentlyAfterClientExitAndPeerDaemonStop(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-background-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	binary := compileCurrentDirectoryCLI(t, base)
	a := newBackgroundFixture(t, base, binary, "a")
	b := newBackgroundFixture(t, base, binary, "b")
	if a.daemon.Identity.PID == b.daemon.Identity.PID || a.daemon.ProjectID == b.daemon.ProjectID || a.daemon.StateDir == b.daemon.StateDir {
		t.Fatal("projects share daemon or state identity")
	}
	// A is accepted while its Planner is held at a deterministic barrier.
	started := time.Now()
	accepted := a.invoke(t, "run", "--id", "goal_a", "--goal", "append one accepted line")
	if time.Since(started) > 3*time.Second || !strings.Contains(accepted, "QUEUED") {
		t.Fatalf("acceptance waited for planner: %s", accepted)
	}
	// B's wait client exits after the actual Provider starts. It must not
	// cancel or pause the daemon-owned planning invocation.
	waitLog, err := os.Create(filepath.Join(base, "wait.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer waitLog.Close()
	client := exec.Command(binary, "--project", b.root, "run", "--id", "goal_b", "--goal", "append one accepted line", "--wait")
	client.Env = b.environment
	client.Stdout = waitLog
	client.Stderr = waitLog
	client.WaitDelay = time.Second
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	clientDone := make(chan error, 1)
	go func() { clientDone <- client.Wait() }()
	clientStopped := false
	t.Cleanup(func() {
		if !clientStopped {
			_ = client.Process.Kill()
			<-clientDone
		}
	})
	for _, f := range []*backgroundFixture{a, b} {
		awaitBackgroundCondition(t, 10*time.Second, "Planner entered "+f.root, func() bool { _, err := os.Stat(f.entered); return err == nil })
	}
	if err := client.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-clientDone:
		clientStopped = true
	case <-time.After(5 * time.Second):
		t.Fatal("wait client did not stop observing")
	}
	before := b.invoke(t, "status", "goal_b")
	if !strings.Contains(before, `"planning_state": "EXECUTING"`) {
		t.Fatalf("client exit cancelled planning: %s", before)
	}
	writeCurrentDirectoryFixture(t, a.release, "release", 0600)
	reportA := a.complete(t, "goal_a")
	a.invoke(t, "daemon", "stop", "--timeout", "10s")
	a.stopped = true
	bStatus := b.invoke(t, "daemon", "status")
	if !strings.Contains(bStatus, b.daemon.Identity.InstanceID) {
		t.Fatalf("B did not survive A stop: %s", bStatus)
	}
	if output, err := invokeCurrentDirectoryCLI(binary, b.environment, "--project", b.root, "status", "goal_a"); err == nil || !strings.Contains(output, "NOT_FOUND") {
		t.Fatalf("B read A goal: %s %v", output, err)
	}
	writeCurrentDirectoryFixture(t, b.release, "release", 0600)
	reportB := b.complete(t, "goal_b")
	b.invoke(t, "daemon", "stop", "--timeout", "10s")
	b.stopped = true
	assertCurrentDirectoryEvidence(t, a.daemon.StateDir, []report.Report{reportA})
	assertCurrentDirectoryEvidence(t, b.daemon.StateDir, []report.Report{reportB})
	t.Logf("independent completed goals: A pid=%d tree=%s; B pid=%d tree=%s", a.daemon.Identity.PID, reportA.Final.Tree, b.daemon.Identity.PID, reportB.Final.Tree)
}

func TestPlanningPauseResumeAndCancelStopRealProviderGroups(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-planning-controls-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	binary := compileCurrentDirectoryCLI(t, base)
	f := newBackgroundFixture(t, base, binary, "controls")
	f.invoke(t, "run", "--id", "goal_controls", "--goal", "append one accepted line")
	awaitBackgroundCondition(t, 10*time.Second, "planner start", func() bool { _, err := os.Stat(f.entered); return err == nil })
	readStatus := func() struct {
		Version       int64  `json:"version"`
		State         string `json:"state"`
		PlanningState string `json:"planning_state"`
		Generation    int64  `json:"planning_generation"`
	} {
		var view struct {
			Version       int64  `json:"version"`
			State         string `json:"state"`
			PlanningState string `json:"planning_state"`
			Generation    int64  `json:"planning_generation"`
		}
		output, err := invokeCurrentDirectoryCLI(f.binary, f.environment, "--project", f.root, "status", "goal_controls")
		var exitError *exec.ExitError
		if err != nil && (!errors.As(err, &exitError) || (exitError.ExitCode() != 3 && exitError.ExitCode() != 4)) {
			t.Fatalf("status: %v %s", err, output)
		}
		if err := json.Unmarshal([]byte(output), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	version := readStatus().Version
	control := func(expectedExit int, args ...string) {
		output, err := invokeCurrentDirectoryCLI(f.binary, f.environment, append([]string{"--project", f.root}, args...)...)
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != expectedExit {
			t.Fatalf("control %v: %v %s", args, err, output)
		}
	}
	control(3, "pause", "goal_controls", "--version", fmt.Sprint(version), "--reason", "inspect planning")
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(f.daemon.StateDir, "state.db")}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	awaitStopped := func() {
		awaitBackgroundCondition(t, 10*time.Second, "owned process cleanup", func() bool {
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM process_invocations WHERE state IN ('INTENT','REGISTERED','UNKNOWN')`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			return count == 0
		})
	}
	awaitStopped()
	paused := readStatus()
	if paused.State != "DRAFT" || paused.PlanningState != "PAUSED" {
		t.Fatalf("pause=%+v", paused)
	}
	f.invoke(t, "resume", "goal_controls", "--version", fmt.Sprint(paused.Version), "--reason", "continue planning")
	awaitBackgroundCondition(t, 10*time.Second, "resumed planner", func() bool {
		data, err := os.ReadFile(f.entered)
		return err == nil && strings.Count(string(data), "entered\n") == 2
	})
	resumed := readStatus()
	if resumed.Generation != 2 || resumed.PlanningState != "EXECUTING" {
		t.Fatalf("resume=%+v", resumed)
	}
	control(4, "cancel", "goal_controls", "--version", fmt.Sprint(resumed.Version), "--reason", "cancel active planning")
	awaitStopped()
	if cancelled := readStatus(); cancelled.State != "CANCELLED" {
		t.Fatalf("cancel=%+v", cancelled)
	}
	var revisions int
	if err := db.QueryRow(`SELECT count(*) FROM goal_revisions WHERE goal_id='goal_controls'`).Scan(&revisions); err != nil || revisions != 0 {
		t.Fatalf("cancel published a revision: %d %v", revisions, err)
	}
	if currentDirectoryGit(t, f.root, "rev-parse", "HEAD") != f.head {
		t.Fatal("planning controls moved HEAD")
	}
	f.invoke(t, "daemon", "stop", "--timeout", "10s")
	f.stopped = true
}
