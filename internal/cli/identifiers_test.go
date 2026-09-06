package cli

import (
	"bytes"
	"net/url"
	"strings"
	"testing"
)

func TestIdentifierCommandsPreserveLiteralPathAndCursor(t *testing.T) {
	id := "goal_a?case#fragment%literal"
	escaped := url.PathEscape(id)
	for _, test := range []struct {
		args []string
		path string
	}{
		{[]string{"status", id}, "/v1/goals/" + escaped},
		{[]string{"status", id, "--watch", "--after-event-id", "event?x&y#%"}, "/v1/goals/" + escaped + "/events?watch=1&after_event_id=" + url.QueryEscape("event?x&y#%")},
		{[]string{"gates", id}, "/v1/goals/" + escaped + "/gates?state=open"},
		{[]string{"report", id}, "/v1/goals/" + escaped + "/report"},
		{[]string{"goal", "get", id}, "/v1/goals/" + escaped},
		{[]string{"work", "list", id}, "/v1/goals/" + escaped + "/work-items"},
		{[]string{"approve", id, "--version", "1", "--reason", "answer"}, "/v1/gates/" + escaped + "/decisions"},
		{[]string{"pause", id, "--version", "1"}, "/v1/goals/" + escaped + "/pause"},
		{[]string{"goal", "plan", id, "--version", "1", "--reason", "retry"}, "/v1/goals/" + escaped + "/plan"},
		{[]string{"work", "retry", id, "--version", "1"}, "/v1/work-items/" + escaped + "/retry"},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			client := &recordingClient{}
			if err := executeTestCommand(test.args, client); err != nil || client.path != test.path {
				t.Fatalf("path=%q want=%q err=%v", client.path, test.path, err)
			}
		})
	}
}

func TestIdentifierCommandsAndDynamicCompletion(t *testing.T) {
	for _, test := range []struct {
		args   []string
		path   string
		output string
	}{
		{[]string{"ids", "work_", "--kind", "work"}, "/v1/identifiers?kind=work&prefix=work_", ""},
		{[]string{"work", "get", "work_abc"}, "/v1/work-items/work_abc", ""},
		{[]string{"gate", "get", "gate_abc"}, "/v1/gates/gate_abc", ""},
		{[]string{"__complete", "status", "goal_"}, "/v1/identifiers?kind=goal&prefix=goal_", "goal_abc"},
		{[]string{"__complete", "pause", "goal_abc", "--version", ""}, "/v1/identifiers?kind=goal&prefix=goal_abc", "7"},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			client := &recordingClient{response: []byte(`{"items":[{"id":"goal_abc","kind":"goal","state":"RUNNING","version":7}]}`)}
			var stdout, stderr bytes.Buffer
			if code := execute(test.args, strings.NewReader(""), &stdout, &stderr, testRuntime(client)); code != 0 || client.path != test.path || !strings.Contains(stdout.String(), test.output) {
				t.Fatalf("code=%d path=%s out=%s err=%s", code, client.path, &stdout, &stderr)
			}
		})
	}
}
