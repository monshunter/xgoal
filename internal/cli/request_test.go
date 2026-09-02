package cli

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestDoctorRequestSeparatesPassiveAndExplicitActiveProbe(t *testing.T) {
	method, path, body, watch, err := commandRequest([]string{"doctor"})
	if err != nil || method != http.MethodGet || path != "/v1/doctor" || body != nil || watch {
		t.Fatalf("passive doctor request = %s %s %#v %t, %v", method, path, body, watch, err)
	}
	want := map[string]any{
		"profile_id":                     "codex",
		"acknowledge_provider_transport": true,
		"timeout_milliseconds":           int64(1500),
	}
	method, path, body, watch, err = commandRequest([]string{"doctor", "--active", "--profile", "codex", "--timeout", "1500ms"})
	if err != nil || method != http.MethodPost || path != "/v1/doctor/active-probes" || watch || !reflect.DeepEqual(body, want) {
		t.Fatalf("active doctor request = %s %s %#v %t, %v", method, path, body, watch, err)
	}
}

func TestDoctorRequestRejectsRemovedOrInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"doctor", "--active", "--profile", "codex", "--timeout", "500us"},
		{"doctor", "--active", "--profile", "codex", "--timeout", "1s", "--max-tokens", "10"},
		{"doctor", "--max-wall-time", "1s"},
	} {
		if _, _, _, _, err := commandRequest(args); err == nil {
			t.Fatalf("commandRequest(%v) accepted removed or invalid arguments", args)
		}
	}
}

func TestRunRequestRejectsAmbiguousGoalAndRemovedAccounting(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--goal", "one", "--goal-file", "goal.md"},
		{"run", "--goal", "one", "--max-tokens", "100"},
		{"run", "--goal", "one", "--cost-budget", "1"},
	} {
		if _, _, _, _, err := commandRequest(args); err == nil {
			t.Fatalf("commandRequest(%v) accepted ambiguous or removed arguments", args)
		}
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

func TestReadCommandRoutes(t *testing.T) {
	tests := []struct {
		args  []string
		path  string
		watch bool
	}{
		{[]string{"status", "goal-1"}, "/v1/goals/goal-1", false},
		{[]string{"status", "goal-1", "--watch", "--after-event-id", "event-2"}, "/v1/goals/goal-1/events?watch=1&after_event_id=event-2", true},
		{[]string{"logs", "attempt-1"}, "/v1/attempts/attempt-1/logs", false},
		{[]string{"gates", "goal-1"}, "/v1/goals/goal-1/gates?state=open", false},
		{[]string{"report", "goal-1"}, "/v1/goals/goal-1/report", false},
		{[]string{"goal", "get", "goal-1"}, "/v1/goals/goal-1", false},
		{[]string{"work", "list", "goal-1"}, "/v1/goals/goal-1/work-items", false},
	}
	for _, test := range tests {
		method, path, body, watch, err := commandRequest(test.args)
		if err != nil || method != http.MethodGet || path != test.path || body != nil || watch != test.watch {
			t.Errorf("commandRequest(%v) = %s %s %#v %t, %v", test.args, method, path, body, watch, err)
		}
	}
}

func TestMutationCommandsRequirePositiveVersionAndReasonWhereNeeded(t *testing.T) {
	method, path, body, watch, err := commandRequest([]string{"approve", "gate-1", "--version", "2", "--reason", "reviewed"})
	if err != nil || method != http.MethodPost || path != "/v1/gates/gate-1/decisions" || watch {
		t.Fatalf("approve request = %s %s %#v %t, %v", method, path, body, watch, err)
	}
	if got := body.(map[string]any)["expected_version"]; got != int64(2) {
		t.Fatalf("expected_version = %#v", got)
	}
	for _, args := range [][]string{
		{"approve", "gate-1", "--version", "0", "--reason", "reviewed"},
		{"approve", "gate-1", "--version", "1"},
		{"pause", "goal-1"},
		{"work", "retry", "work-1", "--version", "not-a-number"},
	} {
		if _, _, _, _, err := commandRequest(args); err == nil || strings.TrimSpace(err.Error()) == "" {
			t.Fatalf("commandRequest(%v) error = %v", args, err)
		}
	}
}

func TestWorkRetryAndCancelRoutes(t *testing.T) {
	for _, action := range []string{"retry", "cancel"} {
		method, path, body, watch, err := commandRequest([]string{"work", action, "work-1", "--version", "4", "--reason", "operator decision"})
		if err != nil || method != http.MethodPost || path != "/v1/work-items/work-1/"+action || watch {
			t.Fatalf("work %s request = %s %s %#v %t, %v", action, method, path, body, watch, err)
		}
		if got := body.(map[string]any)["expected_version"]; got != int64(4) {
			t.Fatalf("work %s expected_version = %#v", action, got)
		}
	}
}
