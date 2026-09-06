package cli

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

type gateClient struct {
	recordingClient
	requests     []string
	bodies       []any
	secondStatus int
}

func (c *gateClient) Do(_ context.Context, method, path, key string, body any) (int, []byte, error) {
	c.requests = append(c.requests, method+" "+path)
	c.bodies = append(c.bodies, body)
	if len(c.requests) == 1 {
		return 200, []byte(`{"ID":"gate_exact","Version":3,"State":"APPROVED","Decision":"ALLOW"}`), nil
	}
	return c.secondStatus, []byte(`{"result":"continuation outcome"}`), nil
}

func TestDecideAndResumeKeepsDecisionAndUsesReturnedIdentity(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusConflict} {
		c := &gateClient{secondStatus: status}
		var stdout, stderr bytes.Buffer
		code := execute([]string{"--project", "/chosen/project", "approve", "gate_e", "--version", "2", "--reason", "use fixture account", "--resume", "--owner-version", "8"}, strings.NewReader(""), &stdout, &stderr, testRuntime(c))
		if (code == 0) != (status == http.StatusOK) || len(c.requests) != 2 || c.requests[0] != "POST /v1/gates/gate_e/decisions" || c.requests[1] != "POST /v1/gates/gate_exact/resume" {
			t.Fatalf("code=%d requests=%v err=%s", code, c.requests, &stderr)
		}
		body := c.bodies[1].(map[string]any)
		if body["expected_gate_version"] != int64(3) || body["expected_owner_version"] != int64(8) {
			t.Fatalf("CAS changed: %v", body)
		}
		if !strings.Contains(stdout.String(), "gate_exact") {
			t.Fatal("lost recorded decision")
		}
		if status == http.StatusConflict && (!strings.Contains(stderr.String(), "Decision retained") || !strings.Contains(stderr.String(), "xgoal --project '/chosen/project' gate resume gate_exact --version 3 --owner-version 8")) {
			t.Fatalf("missing precise retry: %s", &stderr)
		}
	}
}

func TestGateResumeDoesNotDecideAgain(t *testing.T) {
	c := &recordingClient{}
	if err := executeTestCommand([]string{"gate", "resume", "gate_exact", "--version", "3", "--owner-version", "8"}, c); err != nil || c.path != "/v1/gates/gate_exact/resume" {
		t.Fatalf("path=%s err=%v", c.path, err)
	}
	for _, args := range [][]string{{"approve", "gate_exact", "--version", "2", "--reason", "x", "--resume"}, {"approve", "gate_exact", "--version", "2", "--reason", "x", "--resume", "--owner-version", "8", "--decision", "DENY"}} {
		if err := executeTestCommand(args, &recordingClient{}); err == nil {
			t.Fatalf("invalid args accepted %v", args)
		}
	}
}
