package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/app"
)

// The subprocess enters the exact production Run path, including its detached child.
func TestMain(m *testing.M) {
	if os.Getenv("XGOAL_LIFECYCLE_TEST_HELPER") == "1" {
		os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func TestRealCLIConcurrentStartIdentityIsolationAndStop(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "xgoal-cli-life-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	cliRepository(t, a)
	cliRepository(t, b)
	environment := lifecycleEnvironment(filepath.Join(root, "run"))
	t.Cleanup(func() {
		for _, project := range []string{a, b} {
			code, output, err := invokeLifecycleCLI(environment, "--project", project, "daemon", "stop", "--timeout", "5s")
			if code != 0 || err != nil {
				t.Errorf("cleanup daemon: %d %v %s", code, err, output)
			}
		}
	})
	args := [][]string{{"--project", a, "daemon", "start"}, {"daemon", "start", "--project", a}, {"daemon", "start", "--project", b}}
	type callResult struct {
		code int
		body string
		err  error
	}
	results := make([]callResult, 3)
	var workers sync.WaitGroup
	for i, arg := range args {
		workers.Add(1)
		go func(i int, arg []string) {
			defer workers.Done()
			results[i].code, results[i].body, results[i].err = invokeLifecycleCLI(environment, arg...)
		}(i, arg)
	}
	workers.Wait()
	values := make([]app.StartResult, 3)
	for i, result := range results {
		if result.err != nil || result.code != 0 {
			t.Fatalf("start %d code=%d err=%v output=%s", i, result.code, result.err, result.body)
		}
		if err := json.Unmarshal([]byte(result.body), &values[i]); err != nil {
			t.Fatal(err)
		}
	}
	if values[0].Identity.InstanceID != values[1].Identity.InstanceID || values[0].AlreadyRunning == values[1].AlreadyRunning {
		t.Fatalf("concurrent start did not select one winner: %#v", values)
	}
	if values[0].Identity.PID == values[2].Identity.PID || values[0].ProjectID == values[2].ProjectID || values[0].StateDir == values[2].StateDir {
		t.Fatalf("independent projects share ownership: %#v", values)
	}
	code, output, err := invokeLifecycleCLI(environment, "--project", a, "run", "--id", "goal_a", "--goal", "hold a bounded draft")
	if err != nil || code != 3 || !strings.Contains(output, "goal_a") || !strings.Contains(output, "WAITING") {
		t.Fatalf("create A: %d %v %s", code, err, output)
	}
	code, output, err = invokeLifecycleCLI(environment, "--project", b, "status", "goal_a")
	if err != nil || code != 5 {
		t.Fatalf("B read A Goal: %d %v %s", code, err, output)
	}
	code, output, err = invokeLifecycleCLI(environment, "--project", b, "--socket", values[0].SocketPath, "status", "goal_a")
	if err != nil || code != 6 || !strings.Contains(output, "identity") {
		t.Fatalf("B accepted A socket: %d %v %s", code, err, output)
	}
	oldClient, err := api.NewProjectClient(values[0].SocketPath, time.Second, api.ExpectedIdentity{ProjectID: values[0].ProjectID, RepositoryIdentity: values[0].Identity.RepositoryIdentity, ProjectRoot: values[0].ProjectRoot, StateDir: values[0].StateDir})
	if err != nil {
		t.Fatal(err)
	}
	defer oldClient.Close()
	if _, err := oldClient.Handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, output, err = invokeLifecycleCLI(environment, "--project", a, "daemon", "stop")
	if err != nil || code != 0 || !strings.Contains(output, "STOPPED") {
		t.Fatalf("stop A: %d %v %s", code, err, output)
	}
	code, output, err = invokeLifecycleCLI(environment, "--project", b, "daemon", "status")
	if err != nil || code != 0 || !strings.Contains(output, values[2].Identity.InstanceID) {
		t.Fatalf("B did not survive A stop: %d %v %s", code, err, output)
	}
	code, output, err = invokeLifecycleCLI(environment, "--project", a, "daemon", "start")
	if err != nil || code != 0 {
		t.Fatalf("restart A: %d %v %s", code, err, output)
	}
	status, _, err := oldClient.Do(context.Background(), http.MethodPost, "/v1/goals", "old-instance", map[string]any{"goal_id": "must_not_exist", "raw_goal": "no", "mode": "standard"})
	if err != nil || status != http.StatusConflict {
		t.Fatalf("stale client was not fenced from new instance: %d %v", status, err)
	}
}

func TestOfflineDoctorDoesNotCreateStateOrOpenCorruptDatabase(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "xgoal-cli-doctor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	cliRepository(t, project)
	environment := lifecycleEnvironment(filepath.Join(root, "runtime"))
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	commands := filepath.Join(root, "commands")
	if err := os.Mkdir(commands, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitPath, filepath.Join(commands, "git")); err != nil {
		t.Fatal(err)
	}
	environment = append(environment, "PATH="+commands)
	subdir := filepath.Join(project, "child")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []bool{false, true} {
		state := filepath.Join(project, ".xgoal")
		if corrupt {
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(state, "state.db"), []byte("not a database"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		code, output, err := invokeLifecycleCLI(environment, "--project", subdir, "doctor")
		if err != nil || code != 0 {
			t.Fatalf("offline doctor: %d %v %s", code, err, output)
		}
		var value struct {
			ProjectRoot string `json:"project_root"`
			Store       struct {
				Opened bool `json:"opened"`
				Exists bool `json:"exists"`
			} `json:"store"`
			ModelCalls int `json:"model_calls"`
		}
		if err := json.Unmarshal([]byte(output), &value); err != nil {
			t.Fatal(err)
		}
		if value.ProjectRoot != project || value.Store.Opened || value.Store.Exists != corrupt || value.ModelCalls != 0 {
			t.Fatalf("unexpected offline diagnostics: %s", output)
		}
		if corrupt {
			raw, err := os.ReadFile(filepath.Join(state, "state.db"))
			if err != nil || string(raw) != "not a database" {
				t.Fatalf("doctor changed corrupt state: %q %v", raw, err)
			}
		} else if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("doctor created state: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "runtime")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("doctor created runtime: %v", err)
		}
	}
}

func cliRepository(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "-C", path, "init", "-b", "main")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, output)
	}
}
func lifecycleEnvironment(runtimeDir string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "XGOAL_") {
			env = append(env, entry)
		}
	}
	return append(env, "XGOAL_LIFECYCLE_TEST_HELPER=1", "XGOAL_RUNTIME_DIR="+runtimeDir)
}
func invokeLifecycleCLI(environment []string, args ...string) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], args...)
	command.Env = environment
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	if err == nil {
		return 0, string(output), nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), string(output), nil
	}
	return -1, string(output), err
}
