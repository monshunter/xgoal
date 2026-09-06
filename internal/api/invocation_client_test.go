package api

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	callindex "github.com/monshunter/xgoal/internal/invocation"
)

func TestInvocationStreamRequiresCompleteAndRejectsTerminalErrors(t *testing.T) {
	frame := func(complete bool) string {
		p := callindex.LogPage{Invocation: callindex.Summary{ID: "i", Observation: callindex.Observation{Status: "returned"}}, Stream: "stdout", After: 0, Next: 1, DurableCursor: 1, Events: []callindex.LogEvent{{Sequence: 1, Data: json.RawMessage(`{"type":"message"}`)}}, Complete: complete}
		data, _ := json.Marshal(p)
		return string(data) + "\n"
	}
	for _, test := range []struct {
		name, data string
		fails      bool
	}{{"complete", frame(true), false}, {"unexpected-eof", frame(false), true}, {"terminal-error", frame(false) + `{"error":{"code":"INVOCATION_STREAM_FAILED","message":"missing durable file"}}` + "\n", true}} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			err := copyInvocationStream(context.Background(), "/v1/invocations/i/logs?watch=1", strings.NewReader(test.data), &out)
			if (err != nil) != test.fails {
				t.Fatalf("error=%v output=%s", err, &out)
			}
		})
	}
}

func TestInvocationStreamRejectsFalseCompletion(t *testing.T) {
	for _, test := range []struct {
		name, status string
		boundary     int64
	}{{"truncated-boundary", "returned", 2}, {"unknown-status", "unknown", 1}, {"empty-status", "", 1}} {
		t.Run(test.name, func(t *testing.T) {
			page := callindex.LogPage{Invocation: callindex.Summary{ID: "i", Observation: callindex.Observation{Status: test.status}}, Stream: "stdout", Next: 1, DurableCursor: test.boundary, Events: []callindex.LogEvent{{Sequence: 1}}, Complete: true}
			data, _ := json.Marshal(page)
			var out bytes.Buffer
			if err := copyInvocationStream(context.Background(), "/v1/invocations/i/logs?watch=1", bytes.NewReader(data), &out); err == nil {
				t.Fatal("false completion was accepted")
			}
		})
	}
}
