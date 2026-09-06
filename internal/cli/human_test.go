package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

const humanStatusFixture = `{"goal_id":"goal_1","state":"RUNNING","version":7,"planning_state":"COMPLETED","work_items":[{"ID":"work_1","Title":"Make the endpoint work","State":"EXECUTING","Version":3}],"activity":{"heartbeat_at":"2026-09-05T01:02:03Z","last_output_at":"2026-09-05T01:01:03Z","last_material_progress_at":"2026-09-05T01:00:03Z","material_progress_kind":"plan_approved","latest_invocation":{"id":"invocation_1","role":"implementer","profile_id":"codex","observation":{"status":"running","observed_model":"unknown"}}}}`

func TestHumanStatusSeparatesSignalsAndShowsAction(t *testing.T) {
	var stdout, stderr bytes.Buffer
	client := &recordingClient{response: []byte(humanStatusFixture)}
	code := execute([]string{"status", "goal_1", "--format", "human"}, strings.NewReader(""), &stdout, &stderr, testRuntime(client))
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, &stderr)
	}
	for _, want := range []string{"Goal goal_1", "RUNNING", "version 7", "Heartbeat", "01:02:03Z", "Last output", "01:01:03Z", "Material progress", "01:00:03Z", "plan_approved", "implementer", "work_1", "Make the endpoint work", "xgoal logs --invocation invocation_1 --follow"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("missing %q in %s", want, &stdout)
		}
	}
}

func TestHumanStatusShowsRetainedDecision(t *testing.T) {
	text, err := renderHumanGoal([]byte(`{"goal_id":"goal_1","state":"WAITING","version":3,"gates":[{"ID":"gate_answer","State":"APPROVED","Decision":"ALLOW","Version":2,"Required":true,"Used":0}]}`), "xgoal")
	if err != nil || !strings.Contains(text, "answer retained, continuation pending") || !strings.Contains(text, "Next: xgoal gate get gate_answer") {
		t.Fatalf("retained decision invisible: %s %v", text, err)
	}
}

type humanWatchClient struct {
	planningClient
	cancel context.CancelFunc
}

func (client *humanWatchClient) Do(ctx context.Context, method, path, key string, body any) (int, []byte, error) {
	status, data, err := client.planningClient.Do(ctx, method, path, key, body)
	if len(client.requests) == 2 {
		client.cancel()
	}
	return status, data, err
}

func TestHumanWatchFixesUniquePrefixSelection(t *testing.T) {
	for _, next := range []string{"goal_alpha", "goal_al"} {
		t.Run(next, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &humanWatchClient{planningClient: planningClient{responses: []string{`{"goal_id":"goal_alpha","state":"DRAFT"}`, `{"goal_id":"` + next + `","state":"DRAFT"}`}}, cancel: cancel}
			root := newRootCommand(testRuntime(client))
			root.SetArgs([]string{"status", "goal_al", "--watch", "--format", "human"})
			root.SetContext(ctx)
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			err := root.Execute()
			if len(client.requests) != 2 || client.requests[1] != "GET /v1/goals/goal_alpha" {
				t.Fatalf("watch selection changed: %v", client.requests)
			}
			if (err != nil) != (next != "goal_alpha") {
				t.Fatalf("identity=%s err=%v", next, err)
			}
		})
	}
}

func TestHumanFeedbackIsBoundedAndPreservesSelectedProject(t *testing.T) {
	client := &recordingClient{response: []byte(humanStatusFixture)}
	var stdout, stderr bytes.Buffer
	if code := execute([]string{"--project", "/repo/with space", "status", "goal_1", "--format", "human"}, strings.NewReader(""), &stdout, &stderr, testRuntime(client)); code != 0 || !strings.Contains(stdout.String(), "Next: xgoal --project '/repo/with space' logs --invocation invocation_1 --follow") {
		t.Fatalf("code=%d out=%s err=%s", code, &stdout, &stderr)
	}
	var feedback humanFeedback
	var output bytes.Buffer
	now := time.Now()
	for i := 0; i < 1000; i++ {
		if err := feedback.write(&output, []byte(humanStatusFixture), now.Add(time.Duration(i)*time.Millisecond), false); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(output.String(), "Goal goal_1"); n != 1 {
		t.Fatalf("unchanged output flooded: %d", n)
	}
	if err := feedback.write(&output, []byte(humanStatusFixture), now.Add(15*time.Second), false); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(output.String(), "Goal goal_1"); n != 2 {
		t.Fatalf("missing quiet-period feedback: %d", n)
	}
	if err := feedback.write(&output, []byte(strings.Replace(humanStatusFixture, "RUNNING", "COMPLETED", 1)), now.Add(15*time.Second), true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Next: xgoal report goal_1") {
		t.Fatal("terminal update was throttled")
	}
	if text := terminalText("token=secret-sentinel\n\x1b[31m"); strings.ContainsAny(text, "\n\x1b") || strings.Contains(text, "secret-sentinel") {
		t.Fatalf("unsafe terminal text=%q", text)
	}
}

func TestHumanWaitKeepsJSONOnStdoutAndExplainsWaitingOnStderr(t *testing.T) {
	client := &planningClient{responses: []string{`{"goal_id":"g1","state":"DRAFT","planning_state":"QUEUED"}`, `{"goal_id":"g1","state":"DRAFT","planning_state":"EXECUTING"}`, `{"goal_id":"g1","state":"DRAFT","version":4,"planning_state":"WAITING","planning_blocker":"Need the API contract","gates":[{"ID":"gate_1","State":"OPEN","Required":true,"Version":2,"ReasonCode":"AGENT_BLOCKED"}]}`}}
	var stdout, stderr bytes.Buffer
	code := execute([]string{"run", "--id", "g1", "--goal", "implement endpoint", "--wait", "--format", "human"}, strings.NewReader(""), &stdout, &stderr, testRuntime(client))
	if code != 3 {
		t.Fatalf("exit=%d stderr=%s", code, &stderr)
	}
	decoder := json.NewDecoder(&stdout)
	var first, final map[string]any
	if err := decoder.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&final); err != nil || final["planning_state"] != "WAITING" {
		t.Fatalf("final=%v err=%v", final, err)
	}
	for _, want := range []string{"QUEUED", "WAITING", "Need the API contract", "gate_1", "version 2", "xgoal gates g1", "Heartbeat: unknown", "Material progress: unknown"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("missing %q in %s", want, &stderr)
		}
	}
}

func TestHumanFormatValidationBeforeClientCreation(t *testing.T) {
	for _, args := range [][]string{{"status", "g", "--format", "yaml"}, {"status", "g", "--watch", "--format", "human", "--after-event-id", "e"}, {"run", "--goal", "one", "--format", "human"}} {
		r := testRuntime(&recordingClient{})
		r.newClient = func() (apiClient, error) { t.Fatal("client created for invalid input"); return nil, nil }
		if code := execute(args, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, r); code != 2 {
			t.Fatalf("%v exit=%d", args, code)
		}
	}
}

func TestInteractiveWaitDefaultsToProgressWithoutChangingJSONContract(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tty      bool
		format   string
		progress bool
	}{{"terminal", true, "", true}, {"redirected", false, "", false}, {"explicit json", true, "json", false}, {"explicit human", false, "human", true}} {
		t.Run(tc.name, func(t *testing.T) {
			client := &planningClient{responses: []string{`{"goal_id":"g1","state":"DRAFT","planning_state":"QUEUED"}`, `{"goal_id":"g1","state":"COMPLETED","planning_state":"COMPLETED","acceptance_preparation":{"state":"frozen","policy":"allow","criteria":2,"generated_validators":1}}`}}
			r := testRuntime(client)
			r.terminal = func(io.Writer) bool { return tc.tty }
			args := []string{"run", "--id", "g1", "--goal", "implement game", "--wait"}
			if tc.format != "" {
				args = append(args, "--format", tc.format)
			}
			var out, errOut bytes.Buffer
			if code := execute(args, strings.NewReader(""), &out, &errOut, r); code != 0 {
				t.Fatalf("exit %d %s", code, &errOut)
			}
			decoder := json.NewDecoder(&out)
			var first, final map[string]any
			if decoder.Decode(&first) != nil || decoder.Decode(&final) != nil || final["state"] != "COMPLETED" {
				t.Fatalf("stdout contract changed: %s", &out)
			}
			if strings.Contains(errOut.String(), "Acceptance: frozen") != tc.progress {
				t.Fatalf("progress=%t: %s", tc.progress, &errOut)
			}
		})
	}
}
