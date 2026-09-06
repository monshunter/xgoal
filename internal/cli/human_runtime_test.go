package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestRealCLIHumanWaitAndObservation(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-human-")
	if err != nil {
		t.Fatal(err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("human fixture retained: %s", base)
		} else {
			os.RemoveAll(base)
		}
	})
	binary := compileCurrentDirectoryCLI(t, base)
	trigger := filepath.Join(base, "diagnostic-trigger")
	f := newBackgroundFixture(t, base, binary, "human", func(t *testing.T, root, text, roles string) string {
		text = acceptanceServiceConfiguration(t, root, text, roles)
		cfg, err := config.Load(strings.NewReader(text))
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(cfg.Agents[0].Command)
		if err != nil {
			t.Fatal(err)
		}
		before := "do sleep 0.02; done"
		after := "do if [ -f " + currentDirectoryShellQuote(trigger) + " ] && [ \"${diagnostic_sent:-0}\" != 1 ]; then printf 'stderr-only progress diagnostic\\n' >&2; diagnostic_sent=1; fi; sleep 0.02; done"
		if !strings.Contains(string(data), before) {
			t.Fatal("fixture hold missing")
		}
		writeCurrentDirectoryFixture(t, cfg.Agents[0].Command, strings.Replace(string(data), before, after, 1), 0700)
		return text
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--project", f.root, "run", "--id", "goal_human", "--goal", "append accepted output and verify with a real service", "--wait", "--format", "human")
	cmd.Env = f.environment
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	pipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	reader := bufio.NewReader(pipe)
	var early strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("wait feedback: %v %s", err, &early)
		}
		early.WriteString(line)
		if strings.HasPrefix(line, "Next:") {
			break
		}
	}
	if !strings.Contains(early.String(), "QUEUED") || !strings.Contains(early.String(), "Material progress: unknown") {
		t.Fatalf("early feedback=%s", &early)
	}
	awaitBackgroundCondition(t, 10*time.Second, "held Planner", func() bool { _, err := os.Stat(f.entered); return err == nil })
	human := f.invoke(t, "status", "goal_human", "--format", "human")
	if !strings.Contains(human, "planner") || !strings.Contains(human, "Last output:") || !strings.Contains(human, "Material progress: unknown") {
		t.Fatalf("live human=%s", human)
	}
	var prior struct {
		Activity sqlite.GoalActivity `json:"activity"`
	}
	if err := json.Unmarshal([]byte(f.invoke(t, "status", "goal_human")), &prior); err != nil || prior.Activity.LastOutputAt == nil {
		t.Fatalf("initial output=%+v %v", prior, err)
	}
	if err := os.WriteFile(trigger, []byte("emit stderr\n"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitBackgroundCondition(t, 5*time.Second, "stderr-only status refresh", func() bool {
		var current struct {
			Activity sqlite.GoalActivity `json:"activity"`
		}
		if err := json.Unmarshal([]byte(f.invoke(t, "status", "goal_human")), &current); err != nil {
			t.Fatal(err)
		}
		return current.Activity.LastOutputAt != nil && current.Activity.LastOutputAt.After(*prior.Activity.LastOutputAt) && current.Activity.LastMaterialProgressAt == nil && current.Activity.HeartbeatAt == nil
	})
	watch := exec.CommandContext(ctx, binary, "--project", f.root, "status", "goal_human", "--watch", "--format", "human")
	watch.Env = f.environment
	watchPipe, err := watch.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var watchErr bytes.Buffer
	watch.Stderr = &watchErr
	if err := watch.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if watch.ProcessState == nil {
			watch.Process.Kill()
			watch.Wait()
		}
	})
	watchReader := bufio.NewReader(watchPipe)
	if line, err := watchReader.ReadString('\n'); err != nil || !strings.Contains(line, "Goal goal_human") {
		t.Fatalf("watch=%q %v", line, err)
	}
	if err := watch.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, watchReader)
	if err := watch.Wait(); err != nil {
		t.Fatalf("watch cancel=%v %s", err, &watchErr)
	}
	if raw := f.invoke(t, "status", "goal_human"); !strings.Contains(raw, `"EXECUTING"`) {
		t.Fatalf("watch changed Goal: %s", raw)
	}
	if err := os.WriteFile(f.release, []byte("continue\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rest, readErr := io.ReadAll(reader)
	waitErr := cmd.Wait()
	if readErr != nil || waitErr != nil {
		t.Fatalf("wait failed: read=%v wait=%v stderr=%s stdout=%s", readErr, waitErr, rest, &stdout)
	}
	decoder := json.NewDecoder(&stdout)
	var first, last map[string]any
	if err := decoder.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&last); err != nil || last["state"] != "COMPLETED" {
		t.Fatalf("final=%v %v", last, err)
	}
	if !strings.Contains(string(rest), "COMPLETED") || !strings.Contains(string(rest), " report goal_human") {
		t.Fatalf("terminal feedback=%s", rest)
	}
	final := f.invoke(t, "status", "goal_human", "--format", "human")
	if strings.Contains(final, "Heartbeat: unknown") || strings.Contains(final, "Material progress: unknown") || !strings.Contains(final, "final_report") {
		t.Fatalf("terminal status=%s", final)
	}
	f.complete(t, "goal_human")
}
