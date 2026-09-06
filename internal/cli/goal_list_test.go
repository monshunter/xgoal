package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestGoalListCommandRequestsAndPresentation(t *testing.T) {
	cursor := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("1:%x", sha256.Sum256([]byte("goal_a?&#%'")))))
	data, _ := json.Marshal(sqlite.GoalPage{Items: []sqlite.GoalSummary{{GoalID: cursor, State: "DRAFT", Version: 2, Summary: "编写游戏\n\x1b[31m API_KEY=private-value", PlanningState: "WAITING"}}, NextCursor: cursor})
	for _, format := range []string{"json", "human"} {
		client := &recordingClient{response: data}
		args := []string{"--project", "/repo with space", "--state-dir", "/state", "--socket", "/socket", "goal", "list", "--state", "DRAFT", "--limit", "1", "--after", cursor, "--format", format}
		var out, errout bytes.Buffer
		code := execute(args, strings.NewReader(""), &out, &errout, testRuntime(client))
		u, err := url.Parse(client.path)
		if err != nil {
			t.Fatal(err)
		}
		if code != 0 || errout.Len() != 0 || client.method != http.MethodGet || client.key != "" || client.body != nil || u.Path != "/v1/goals" || u.Query().Get("after") != cursor || u.Query().Get("state") != "DRAFT" || u.Query().Get("limit") != "1" {
			t.Fatalf("code=%d path=%s out=%s err=%s", code, client.path, &out, &errout)
		}
		if format == "json" {
			if !json.Valid(out.Bytes()) {
				t.Fatal("invalid JSON")
			}
			continue
		}
		for _, want := range []string{"GOAL_ID", "STATE", "VERSION", "PLANNING", "UPDATED_AT", "SUMMARY", "WAITING", "编写游戏", "--project '/repo with space' --state-dir '/state' --socket '/socket' goal list --limit 1 --state DRAFT --after " + shellArg(cursor) + " --format human"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing %q: %s", want, &out)
			}
		}
		if strings.ContainsAny(out.String(), "\x1b\r") || strings.Contains(out.String(), "private-value") {
			t.Fatalf("unsafe terminal output: %q", out.String())
		}
	}
	for _, format := range []string{"json", "human"} {
		client := &recordingClient{response: []byte(`{"items":[],"next_cursor":""}`)}
		var out, errout bytes.Buffer
		if code := execute([]string{"goal", "list", "--format", format}, strings.NewReader(""), &out, &errout, testRuntime(client)); code != 0 {
			t.Fatalf("empty code=%d %s", code, &errout)
		}
		if format == "human" && !strings.Contains(out.String(), "No matching Goals.") {
			t.Fatalf("empty=%s", &out)
		}
	}
}

func TestGoalListValidationHelpAndCompletionBeforeClient(t *testing.T) {
	for _, extra := range [][]string{{"unexpected"}, {"--limit", "0"}, {"--limit", "101"}, {"--limit", "x"}, {"--state", "FAILED"}, {"--format", "yaml"}, {"--after", "a\n"}, {"--after", strings.Repeat("a", 257)}} {
		runtime := testRuntime(nil)
		called := false
		runtime.newClient = func() (apiClient, error) { called = true; return nil, errors.New("unexpected client") }
		var out, errout bytes.Buffer
		if code := execute(append([]string{"goal", "list"}, extra...), strings.NewReader(""), &out, &errout, runtime); code != 2 || called || out.Len() != 0 {
			t.Fatalf("args=%v code=%d called=%v out=%s err=%s", extra, code, called, &out, &errout)
		}
	}
	for _, args := range [][]string{{"goal", "list", "--help"}, {"__complete", "goal", "list", ""}, {"__complete", "goal", "list", "--state", ""}, {"__complete", "goal", "list", "--format", ""}} {
		runtime := testRuntime(nil)
		called := false
		runtime.newClient = func() (apiClient, error) { called = true; return nil, errors.New("unexpected client") }
		var out, errout bytes.Buffer
		code := execute(args, strings.NewReader(""), &out, &errout, runtime)
		if code != 0 || called {
			t.Fatalf("args=%v code=%d called=%v %s", args, code, called, &errout)
		}
		if args[len(args)-1] == "--help" && !strings.Contains(out.String(), "--after") {
			t.Fatalf("help=%s", &out)
		}
		if len(args) > 4 && args[3] == "--state" && !strings.Contains(out.String(), "COMPLETED") {
			t.Fatalf("completion=%s", &out)
		}
	}
}

func TestGoalListFailuresStayErrors(t *testing.T) {
	for _, test := range []struct {
		client *recordingClient
		code   int
	}{
		{&recordingClient{status: 404, response: []byte(`{"error":{"code":"NOT_FOUND"}}`)}, 5},
		{&recordingClient{err: errors.New("daemon unavailable")}, 6},
		{&recordingClient{response: []byte(`{}`)}, 5},
	} {
		var out, errout bytes.Buffer
		code := execute([]string{"goal", "list", "--format", "human"}, strings.NewReader(""), &out, &errout, testRuntime(test.client))
		if code != test.code || out.Len() != 0 || errout.Len() == 0 {
			t.Fatalf("code=%d want=%d out=%s err=%s", code, test.code, &out, &errout)
		}
	}
}
