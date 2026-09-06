package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDoctorRequestSeparatesPassiveAndExplicitActiveProbe(t *testing.T) {
	request, err := doctorRequest(doctorOptions{})
	if err != nil || request.method != http.MethodGet || request.path != "/v1/doctor" || request.body != nil || request.watch {
		t.Fatalf("passive doctor request = %#v, %v", request, err)
	}
	want := map[string]any{
		"profile_id":                     "codex",
		"acknowledge_provider_transport": true,
		"timeout_milliseconds":           int64(1500),
	}
	request, err = doctorRequest(doctorOptions{active: true, profile: "codex", timeout: 1500 * time.Millisecond})
	if err != nil || request.method != http.MethodPost || request.path != "/v1/doctor/active-probes" || request.watch || !reflect.DeepEqual(request.body, want) {
		t.Fatalf("active doctor request = %#v, %v", request, err)
	}
}

func TestDoctorRequestRejectsIncompleteOrInvalidActiveProbe(t *testing.T) {
	for _, options := range []doctorOptions{
		{active: true, profile: "codex", timeout: 500 * time.Microsecond},
		{active: true, timeout: time.Second},
		{profile: "codex", timeout: time.Second},
	} {
		if _, err := doctorRequest(options); err == nil {
			t.Fatalf("doctorRequest(%#v) accepted invalid options", options)
		}
	}
}

func TestRunRequestRejectsAmbiguousGoalAndInvalidMode(t *testing.T) {
	newID := func(string) (string, error) { return "goal-1", nil }
	for _, options := range []runOptions{
		{goal: "one", goalFile: "goal.md"},
		{},
		{goal: "one", mode: "turbo"},
	} {
		if _, err := runRequest(strings.NewReader(""), options, newID); err == nil {
			t.Fatalf("runRequest(%#v) accepted invalid options", options)
		}
	}
}

func TestRunRequestReadsStdinAndPreservesTypedOptions(t *testing.T) {
	request, err := runRequest(strings.NewReader("goal from stdin"), runOptions{
		goalFile: "-",
		goalID:   "goal-1",
		mode:     "standard",
		wait:     true,
	}, func(string) (string, error) { return "unused", nil })
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"goal_id":    "goal-1",
		"raw_goal":   "goal from stdin",
		"created_by": currentActor(),
		"mode":       "standard",
	}
	if request.method != http.MethodPost || request.path != "/v1/goals" || !request.wait || !reflect.DeepEqual(request.body, want) {
		t.Fatalf("request = %#v, want body %#v", request, want)
	}
}

func TestCreatedGoalStateRecognizesTerminalAndPlannerGate(t *testing.T) {
	goalID, state, waiting, err := createdGoalState([]byte(`{"goal_id":"goal-1","state":"RUNNING"}`))
	if err != nil || goalID != "goal-1" || state != "RUNNING" || waiting {
		t.Fatalf("createdGoalState running = %q %q %t %v", goalID, state, waiting, err)
	}
	_, _, waiting, err = createdGoalState([]byte(`{"goal_id":"goal-1","state":"DRAFT","planner_gate_required":true}`))
	if err != nil || !waiting {
		t.Fatalf("createdGoalState gate = waiting %t, %v", waiting, err)
	}
}

func TestCobraReadCommandsRouteRequests(t *testing.T) {
	tests := []struct {
		args   []string
		path   string
		stream bool
	}{
		{[]string{"status", "goal-1"}, "/v1/goals/goal-1", false},
		{[]string{"status", "goal-1", "--watch", "--after-event-id", "event-2"}, "/v1/goals/goal-1/events?watch=1&after_event_id=event-2", true},
		{[]string{"invocations", "goal-1", "--role", "planner"}, "/v1/goals/goal-1/invocations?role=planner", false},
		{[]string{"context", "invoke-1"}, "/v1/invocations/invoke-1/context", false},
		{[]string{"logs", "--invocation", "invoke-1", "--after", "2"}, "/v1/invocations/invoke-1/logs?after=2&limit=100&stream=stdout", false},
		{[]string{"logs", "--invocation", "invoke-1", "--stream", "stderr", "--follow"}, "/v1/invocations/invoke-1/logs?after=0&limit=100&stream=stderr&watch=1", true},
		{[]string{"logs", "attempt-1"}, "/v1/attempts/attempt-1/logs", false},
		{[]string{"gates", "goal-1"}, "/v1/goals/goal-1/gates?state=open", false},
		{[]string{"report", "goal-1"}, "/v1/goals/goal-1/report", false},
		{[]string{"goal", "get", "goal-1"}, "/v1/goals/goal-1", false},
		{[]string{"work", "list", "goal-1"}, "/v1/goals/goal-1/work-items", false},
	}
	for _, test := range tests {
		client := &recordingClient{}
		if err := executeTestCommand(test.args, client); err != nil {
			t.Errorf("execute(%v): %v", test.args, err)
			continue
		}
		if client.method != http.MethodGet || client.path != test.path || client.streamed != test.stream {
			t.Errorf("execute(%v) = method %s path %s stream %t", test.args, client.method, client.path, client.streamed)
		}
	}
}

func TestCobraMutationCommandsPreserveRoutesAndBodies(t *testing.T) {
	tests := []struct {
		args []string
		path string
		body map[string]any
	}{
		{
			[]string{"approve", "gate-1", "--version", "2", "--reason", "reviewed", "--by", "alice"},
			"/v1/gates/gate-1/decisions",
			map[string]any{"expected_version": int64(2), "decision": "ALLOW", "decided_by": "alice", "reason": "reviewed"},
		},
		{
			[]string{"pause", "goal-1", "--version", "3", "--reason", "operator"},
			"/v1/goals/goal-1/pause",
			map[string]any{"expected_version": int64(3), "reason": "operator"},
		},
		{
			[]string{"work", "retry", "work-1", "--version", "4", "--reason", "retry"},
			"/v1/work-items/work-1/retry",
			map[string]any{"expected_version": int64(4), "reason": "retry"},
		},
	}
	for _, test := range tests {
		client := &recordingClient{}
		if err := executeTestCommand(test.args, client); err != nil {
			t.Errorf("execute(%v): %v", test.args, err)
			continue
		}
		if client.method != http.MethodPost || client.path != test.path || !reflect.DeepEqual(client.body, test.body) || client.key != "request-test" {
			t.Errorf("execute(%v) = %s %s key=%q body=%#v", test.args, client.method, client.path, client.key, client.body)
		}
	}
}

func TestCobraCommandTreeContainsEverySupportedCommand(t *testing.T) {
	root := newRootCommand(testRuntime(&recordingClient{}))
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	paths := [][]string{
		{"goal", "plan"},
		{"init"}, {"doctor"}, {"run"}, {"status"}, {"logs"}, {"gates"}, {"approve"},
		{"pause"}, {"resume"}, {"cancel"}, {"report"}, {"clean"}, {"version"},
		{"config", "validate"}, {"benchmark", "validate"}, {"benchmark", "run"},
		{"daemon", "serve"}, {"daemon", "start"}, {"daemon", "stop"}, {"daemon", "status"}, {"goal", "get"}, {"goal", "replan"}, {"goal", "finalize"},
		{"work", "list"}, {"work", "retry"}, {"work", "cancel"}, {"completion"}, {"help"},
	}
	for _, path := range paths {
		command, remaining, err := root.Find(path)
		if err != nil || len(remaining) != 0 || command.Name() != path[len(path)-1] {
			t.Errorf("Find(%v) = command %v, remaining %v, err %v", path, command, remaining, err)
		}
	}
}

func TestCobraRemainingAPICommandsPreserveRequests(t *testing.T) {
	directory := t.TempDir()
	requestFile := directory + "/request.json"
	if err := os.WriteFile(requestFile, []byte(`{"reason":"updated"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		args []string
		path string
		body map[string]any
	}{
		{
			[]string{"doctor", "--active", "--profile", "codex", "--timeout", "1500ms"},
			"/v1/doctor/active-probes",
			map[string]any{"profile_id": "codex", "acknowledge_provider_transport": true, "timeout_milliseconds": int64(1500)},
		},
		{
			[]string{"run", "--goal", "ship it", "--id", "goal-1", "--mode", "fast"},
			"/v1/goals",
			map[string]any{"goal_id": "goal-1", "raw_goal": "ship it", "created_by": currentActor(), "mode": "fast"},
		},
		{[]string{"resume", "goal-1", "--version", "2"}, "/v1/goals/goal-1/resume", map[string]any{"expected_version": int64(2), "reason": ""}},
		{[]string{"cancel", "goal-1", "--version", "2"}, "/v1/goals/goal-1/cancel", map[string]any{"expected_version": int64(2), "reason": ""}},
		{[]string{"clean", "project-1", "--dry-run"}, "/v1/projects/project-1/clean", map[string]any{"dry_run": true}},
		{[]string{"goal", "replan", "goal-1", "--file", requestFile}, "/v1/goals/goal-1/replan", map[string]any{"reason": "updated"}},
		{[]string{"goal", "finalize", "goal-1", "--file", requestFile}, "/v1/goals/goal-1/finalize", map[string]any{"reason": "updated"}},
		{[]string{"work", "cancel", "work-1", "--version", "5"}, "/v1/work-items/work-1/cancel", map[string]any{"expected_version": int64(5), "reason": ""}},
	}
	for _, test := range tests {
		client := &recordingClient{}
		if err := executeTestCommand(test.args, client); err != nil {
			t.Errorf("execute(%v): %v", test.args, err)
			continue
		}
		if client.method != http.MethodPost || client.path != test.path || !reflect.DeepEqual(client.body, test.body) {
			t.Errorf("execute(%v) = %s %s body=%#v", test.args, client.method, client.path, client.body)
		}
	}
}

func TestBenchmarkRunnerArgumentsRequireDashAndRemainOpaque(t *testing.T) {
	command := newBenchmarkRunCommand(testRuntime(&recordingClient{}))
	if err := command.ParseFlags([]string{"--file", "suite.json", "--task", "task-1", "--group", "native", "--", "runner", "--its-flag"}); err != nil {
		t.Fatal(err)
	}
	args := command.Flags().Args()
	if err := command.Args(command, args); err != nil {
		t.Fatal(err)
	}
	if want := []string{"runner", "--its-flag"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("runner args = %v, want %v", args, want)
	}

	withoutDash := newBenchmarkRunCommand(testRuntime(&recordingClient{}))
	if err := withoutDash.ParseFlags([]string{"--file", "suite.json", "--task", "task-1", "--group", "native", "runner"}); err != nil {
		t.Fatal(err)
	}
	if err := withoutDash.Args(withoutDash, withoutDash.Flags().Args()); err == nil {
		t.Fatal("benchmark run accepted runner argv without --")
	}
}

func TestStableExitCodesAndOutputStreams(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		response string
		err      error
		code     int
		stderr   bool
	}{
		{"success", http.StatusOK, `{"state":"RUNNING"}`, nil, 0, false},
		{"waiting", http.StatusOK, `{"state":"WAITING"}`, nil, 3, false},
		{"cancelled", http.StatusOK, `{"state":"CANCELLED"}`, nil, 4, false},
		{"bad request", http.StatusBadRequest, `{"error":"bad"}`, nil, 2, true},
		{"forbidden", http.StatusForbidden, `{"error":"denied"}`, nil, 7, true},
		{"unavailable", http.StatusServiceUnavailable, `{"error":"down"}`, nil, 6, true},
		{"internal", http.StatusInternalServerError, `{"error":"broken"}`, nil, 5, true},
		{"transport", 0, "", errors.New("dial failed"), 6, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &recordingClient{status: test.status, response: []byte(test.response), err: test.err}
			var stdout, stderr bytes.Buffer
			code := execute([]string{"status", "goal-1"}, strings.NewReader(""), &stdout, &stderr, testRuntime(client))
			if code != test.code {
				t.Fatalf("code = %d, want %d; stdout=%q stderr=%q", code, test.code, stdout.String(), stderr.String())
			}
			if test.stderr && stderr.Len() == 0 {
				t.Fatalf("expected stderr, stdout=%q", stdout.String())
			}
			if !test.stderr && test.response != "" && stdout.Len() == 0 {
				t.Fatalf("expected stdout, stderr=%q", stderr.String())
			}
		})
	}
}

func TestRunWaitMapsImmediateTerminalGoalStates(t *testing.T) {
	for _, test := range []struct {
		state string
		code  int
	}{
		{"COMPLETED", 0},
		{"WAITING", 3},
		{"CANCELLED", 4},
	} {
		client := &recordingClient{status: http.StatusOK, response: []byte(`{"goal_id":"goal-1","state":"` + test.state + `"}`)}
		var stdout, stderr bytes.Buffer
		code := execute([]string{"run", "--goal", "ship it", "--id", "goal-1", "--wait"}, strings.NewReader(""), &stdout, &stderr, testRuntime(client))
		if code != test.code {
			t.Errorf("state %s code = %d, want %d; stdout=%q stderr=%q", test.state, code, test.code, stdout.String(), stderr.String())
		}
	}
}

func TestWatchPropagatesCancelledContextWithoutDuplicateError(t *testing.T) {
	client := &cancellationClient{}
	root := newRootCommand(testRuntime(client))
	root.SetArgs([]string{"status", "goal-1", "--watch"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root.SetContext(ctx)
	err := root.Execute()
	var commandErr *commandError
	if !errors.As(err, &commandErr) || commandErr.code != 5 || !commandErr.silent {
		t.Fatalf("watch cancellation error = %#v", err)
	}
}

func TestCobraValidationRunsBeforeAPIClientCreation(t *testing.T) {
	for _, args := range [][]string{
		{"doctor", "--active", "--profile", "codex", "--timeout", "500us"},
		{"doctor", "--max-wall-time", "1s"},
		{"run", "--goal", "one", "--goal-file", "goal.md"},
		{"run", "--goal", "one", "--max-tokens", "100"},
		{"approve", "gate-1", "--version", "0", "--reason", "reviewed"},
		{"approve", "gate-1", "--version", "1"},
		{"pause", "goal-1"},
		{"status", "goal-1", "--after-event-id", "event-1"},
		{"work", "retry", "work-1", "--version", "not-a-number"},
	} {
		created := false
		runtime := testRuntime(&recordingClient{})
		runtime.newClient = func() (apiClient, error) {
			created = true
			return &recordingClient{}, nil
		}
		root := newRootCommand(runtime)
		root.SetArgs(args)
		root.SetIn(strings.NewReader(""))
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		if err := root.Execute(); err == nil || strings.TrimSpace(err.Error()) == "" {
			t.Errorf("execute(%v) error = %v", args, err)
		}
		if created {
			t.Errorf("execute(%v) created API client before validation", args)
		}
	}
}

type recordingClient struct {
	method   string
	path     string
	key      string
	body     any
	streamed bool
	status   int
	response []byte
	err      error
}

func (client *recordingClient) Do(_ context.Context, method, path, key string, body any) (int, []byte, error) {
	client.method = method
	client.path = path
	client.key = key
	client.body = body
	status := client.status
	if status == 0 && client.err == nil {
		status = http.StatusOK
	}
	response := client.response
	if response == nil && client.err == nil {
		response = []byte(`{}`)
	}
	return status, response, client.err
}

func (client *recordingClient) Stream(_ context.Context, path string, _ io.Writer) (int, error) {
	client.method = http.MethodGet
	client.path = path
	client.streamed = true
	return http.StatusOK, nil
}

type cancellationClient struct{}

func (*cancellationClient) Do(context.Context, string, string, string, any) (int, []byte, error) {
	return 0, nil, errors.New("unexpected Do")
}

func (*cancellationClient) Stream(ctx context.Context, _ string, _ io.Writer) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

func testRuntime(client apiClient) runtime {
	return runtime{
		newClient: func() (apiClient, error) { return client, nil },
		newID:     func(prefix string) (string, error) { return prefix + "-test", nil },
		now:       time.Now,
	}
}

func executeTestCommand(args []string, client apiClient) error {
	root := newRootCommand(testRuntime(client))
	root.SetArgs(args)
	root.SetIn(strings.NewReader(""))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	return root.Execute()
}
