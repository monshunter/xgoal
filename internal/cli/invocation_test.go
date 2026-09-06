package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/config"
	callindex "github.com/monshunter/xgoal/internal/invocation"
)

func TestRealCLIInvocationContextsLiveFollowAndReconnect(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-live-invocations-")
	if err != nil {
		t.Fatal(err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("live invocation fixture retained: %s", base)
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
		marker := "printf 'entered\\n' >> "
		if !strings.Contains(string(data), marker) {
			t.Fatal("missing planning hold")
		}
		script := strings.Replace(string(data), marker, "printf 'api_key=live-api-sentinel\\nvisible running diagnostic\\n' >&2\n"+marker, 1)
		writeCurrentDirectoryFixture(t, cfg.Agents[0].Command, script, 0700)
		return text
	})
	f.invoke(t, "run", "--id", "goal_live", "--goal", "append accepted output and verify with a real service")
	awaitBackgroundCondition(t, 10*time.Second, "live Planner", func() bool { _, err := os.Stat(f.entered); return err == nil })
	var list struct {
		Invocations []callindex.Summary `json:"invocations"`
	}
	if err := json.Unmarshal([]byte(f.invoke(t, "invocations", "goal_live", "--role", "planner")), &list); err != nil || len(list.Invocations) != 1 {
		t.Fatalf("planner list: %+v %v", list, err)
	}
	id := list.Invocations[0].ID
	var view callindex.Context
	prefix := id[:len(id)-4]
	if err := json.Unmarshal([]byte(f.invoke(t, "context", prefix)), &view); err != nil || view.Invocation.Observation.Status != "running" || view.Invocation.Input.ID != id || len(view.Packet) == 0 || view.Invocation.Input.RequestHash == "" || view.Invocation.Input.GoalRevisionHash != "" {
		t.Fatalf("live context: %+v %v", view, err)
	}
	var stderr callindex.LogPage
	raw := f.invoke(t, "logs", "--invocation", id, "--stream", "stderr")
	if err := json.Unmarshal([]byte(raw), &stderr); err != nil || len(stderr.Events) != 2 || strings.Contains(raw, "live-api-sentinel") {
		t.Fatalf("live stderr: %s %v", raw, err)
	}
	// Interrupting a reader closes only its subscription; the Goal keeps running.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--project", f.root, "logs", "--invocation", prefix, "--follow")
	cmd.Env = f.environment
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var first callindex.LogPage
	if err := json.NewDecoder(pipe).Decode(&first); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("first live page: %v %s", err, diagnostics.String())
	}
	if first.Next == 0 || first.Complete {
		t.Fatalf("first page: %+v", first)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("reader interruption: %v %s", err, diagnostics.String())
	}
	var second callindex.LogPage
	if err := json.Unmarshal([]byte(f.invoke(t, "logs", "--invocation", id, "--after", strconv.FormatInt(first.Next, 10))), &second); err != nil || len(second.Events) != 0 || second.Complete {
		t.Fatalf("reconnect repeated output or stopped Agent: %+v %v", second, err)
	}
	writeCurrentDirectoryFixture(t, f.release, "release", 0600)
	final := f.complete(t, "goal_live")
	if final.Final.EvidenceSetID == "" {
		t.Fatal("no final evidence")
	}
	if text := f.invoke(t, "ids", "goal_l"); !strings.Contains(text, "goal_live") {
		t.Fatalf("Goal lookup=%s", text)
	}
	if text := f.invoke(t, "status", "goal_l", "--format", "human"); !strings.Contains(text, "COMPLETED") {
		t.Fatalf("Goal prefix status=%s", text)
	}
	if text := f.invoke(t, "__complete", "status", "goal_l"); !strings.Contains(text, "goal_live") {
		t.Fatalf("dynamic Goal completion=%s", text)
	}
	stream := f.invoke(t, "logs", "--invocation", id, "--after", strconv.FormatInt(first.Next, 10), "--follow")
	decoder := json.NewDecoder(strings.NewReader(stream))
	cursor := first.Next
	complete := false
	for {
		var page callindex.LogPage
		err := decoder.Decode(&page)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range page.Events {
			if entry.Sequence != cursor+1 {
				t.Fatalf("cursor skipped/repeated %d -> %d", cursor, entry.Sequence)
			}
			cursor = entry.Sequence
		}
		complete = page.Complete
	}
	if !complete || cursor <= first.Next {
		t.Fatalf("follow failed to drain: %s", stream)
	}
	// A file disappearing after the first HTTP 200 page must stop follow with
	// a nonzero exit; successful headers cannot turn incomplete logs into success.
	failureCtx, stopFailure := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopFailure()
	failing := exec.CommandContext(failureCtx, binary, "--project", f.root, "logs", "--invocation", id, "--limit", "1", "--follow")
	failing.Env = f.environment
	failingPipe, err := failing.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var failingDiagnostics bytes.Buffer
	failing.Stderr = &failingDiagnostics
	if err := failing.Start(); err != nil {
		t.Fatal(err)
	}
	failingDecoder := json.NewDecoder(failingPipe)
	var firstPage callindex.LogPage
	if err := failingDecoder.Decode(&firstPage); err != nil {
		failing.Process.Kill()
		failing.Wait()
		t.Fatal(err)
	}
	eventPath := filepath.Join(f.daemon.StateDir, "adapters", "codex", "plans", id, "events", "000002.json")
	savedPath := filepath.Join(filepath.Dir(filepath.Dir(eventPath)), "saved-event.json")
	if err := os.Rename(eventPath, savedPath); err != nil {
		failing.Process.Kill()
		failing.Wait()
		t.Fatal(err)
	}
	tail, readErr := io.ReadAll(io.MultiReader(failingDecoder.Buffered(), failingPipe))
	waitErr := failing.Wait()
	restoreErr := os.Rename(savedPath, eventPath)
	if restoreErr != nil {
		t.Fatal(restoreErr)
	}
	var exitErr *exec.ExitError
	if readErr != nil || !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 6 || !strings.Contains(string(tail), "INVOCATION_STREAM_FAILED") {
		t.Fatalf("missing-file follow: read=%v wait=%v output=%s stderr=%s", readErr, waitErr, tail, &failingDiagnostics)
	}
	if err := json.Unmarshal([]byte(f.invoke(t, "invocations", "goal_live")), &list); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range list.Invocations {
		raw := f.invoke(t, "context", item.ID)
		view = callindex.Context{}
		if err := json.Unmarshal([]byte(raw), &view); err != nil || len(view.Packet) == 0 || len(view.Metadata) == 0 || len(view.Result) == 0 || len(view.ArtifactErrors) > 0 || view.Invocation.Observation.Status != "returned" {
			t.Fatalf("%s context: packet=%d metadata=%d result=%d errors=%v status=%s decode=%v", item.Role, len(view.Packet), len(view.Metadata), len(view.Result), view.ArtifactErrors, view.Invocation.Observation.Status, err)
		}
		if strings.Contains(raw, "live-api-sentinel") {
			t.Fatal("context leaked diagnostic")
		}
		seen[item.Role] = true
	}
	for _, role := range []string{"planner", "implementer", "reviewer", "acceptance"} {
		if !seen[role] {
			t.Fatal(fmt.Sprintf("missing %s context", role))
		}
	}
	selected := f.invoke(t, "logs", "--goal", "goal_live", "--role", "acceptance")
	if !strings.Contains(selected, `"role": "acceptance"`) {
		t.Fatalf("Goal/role selection: %s", selected)
	}
	f.invoke(t, "daemon", "stop", "--timeout", "10s")
	f.stopped = true
}
