package cli

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPlanningStatusControlsWaitWithoutTreatingHistoryAsOpenGate(t *testing.T) {
	for _, test := range []struct {
		name, body string
		waiting    bool
		code       int
	}{
		{"queued", `{"goal_id":"g1","state":"DRAFT","planning_state":"QUEUED"}`, false, 0},
		{"executing", `{"goal_id":"g1","state":"DRAFT","planning_state":"EXECUTING"}`, false, 0},
		{"paused", `{"goal_id":"g1","state":"DRAFT","planning_state":"PAUSED"}`, true, 3},
		{"waiting", `{"goal_id":"g1","state":"DRAFT","planning_state":"WAITING"}`, true, 3},
		{"historical_gate", `{"goal_id":"g1","state":"RUNNING","gates":[{"State":"APPROVED","Required":true}]}`, false, 0},
		{"ready_blocked", `{"goal_id":"g1","state":"READY","gates":[{"State":"OPEN","Required":true}]}`, true, 3},
		{"optional_gate", `{"goal_id":"g1","state":"RUNNING","gates":[{"State":"OPEN","Required":false}]}`, false, 0},
		{"legacy_blocked", `{"goal_id":"g1","state":"READY","execution_blocker":"EXECUTION_MIGRATION_REQUIRED"}`, true, 3},
		{"cancelled", `{"goal_id":"g1","state":"CANCELLED","planning_state":"WAITING"}`, false, 4},
		{"completed", `{"goal_id":"g1","state":"COMPLETED","execution_blocker":"CONFIG_MIGRATION_REQUIRED"}`, false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, waiting, err := createdGoalState([]byte(test.body))
			if err != nil || waiting != test.waiting {
				t.Fatalf("waiting=%t err=%v", waiting, err)
			}
			if code := exitCode(200, []byte(test.body)); code != test.code {
				t.Fatalf("exit=%d want=%d", code, test.code)
			}
		})
	}
}

type planningClient struct {
	recordingClient
	responses []string
	requests  []string
}

func (client *planningClient) Do(_ context.Context, method, path, key string, body any) (int, []byte, error) {
	client.requests = append(client.requests, method+" "+path)
	response := client.responses[0]
	if len(client.responses) > 1 {
		client.responses = client.responses[1:]
	}
	return 200, []byte(response), nil
}
func TestRunWaitOnlyObservesQueuedPlanningUntilPaused(t *testing.T) {
	client := &planningClient{responses: []string{
		`{"goal_id":"g1","state":"DRAFT","planning_state":"QUEUED"}`,
		`{"goal_id":"g1","state":"DRAFT","planning_state":"EXECUTING"}`,
		`{"goal_id":"g1","state":"DRAFT","planning_state":"PAUSED"}`,
	}}
	var stdout, stderr bytes.Buffer
	code := execute([]string{"run", "--goal", "bounded goal", "--id", "g1", "--wait"}, strings.NewReader(""), &stdout, &stderr, testRuntime(client))
	if code != 3 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	want := []string{"POST /v1/goals", "GET /v1/goals/g1", "GET /v1/goals/g1"}
	if !reflect.DeepEqual(client.requests, want) {
		t.Fatalf("wait mutated Goal: %v", client.requests)
	}
}
func TestGoalPlanCommandPreservesCASReasonAndProposal(t *testing.T) {
	file := filepath.Join(t.TempDir(), "proposal.json")
	if err := os.WriteFile(file, []byte(`{"protocol_version":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--expected-version", "--version"} {
		client := &recordingClient{}
		if err := executeTestCommand([]string{"goal", "plan", "g1", flag, "3", "--reason", "reviewed proposal", "--proposal-file", file}, client); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"expected_version": int64(3), "reason": "reviewed proposal", "proposal": map[string]any{"protocol_version": "fixture"}}
		if client.method != http.MethodPost || client.path != "/v1/goals/g1/plan" || !reflect.DeepEqual(client.body, want) {
			t.Fatalf("request=%s %s %#v", client.method, client.path, client.body)
		}
	}
}

func TestGoalPlanRejectsInvalidInputBeforeClientConstruction(t *testing.T) {
	for _, args := range [][]string{
		{"goal", "plan", "g1", "--reason", "retry"},
		{"goal", "plan", "g1", "--expected-version", "0", "--reason", "retry"},
		{"goal", "plan", "g1", "--expected-version", "2"},
		{"goal", "plan", "g1", "--expected-version", "2", "--reason", " "},
		{"goal", "plan", "g1", "--expected-version", "2", "--version", "3", "--reason", "retry"},
		{"goal", "plan", "g1", "--expected-version", "2", "--reason", "retry", "--proposal-file", "/missing/proposal"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dependencies := testRuntime(&recordingClient{})
			dependencies.newClient = func() (apiClient, error) {
				t.Fatal("constructed API client for invalid input")
				return nil, nil
			}
			var stdout, stderr bytes.Buffer
			if code := execute(args, strings.NewReader(""), &stdout, &stderr, dependencies); code != 2 {
				t.Fatalf("exit=%d stderr=%s", code, stderr.String())
			}
		})
	}
}
