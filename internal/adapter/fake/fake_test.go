package fake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/adapter/fake"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
)

func TestPassiveProbeAndScriptedResult(t *testing.T) {
	script := fake.Script{
		Events: []protocol.AgentEvent{{ProtocolVersion: protocol.AgentEventVersion, Type: "session", At: time.Unix(100, 0).UTC(), SessionID: "session_1"}},
		Result: protocol.AgentResult{ProtocolVersion: protocol.AgentResultVersion, Status: protocol.ResultCompleted, Summary: "claim complete"},
	}
	runtime := fake.New("fake-runtime", script)
	caps, err := runtime.Probe(context.Background(), adapter.ProbeSpec{Mode: adapter.ProbePassive})
	if err != nil {
		t.Fatal(err)
	}
	if caps.ProbeMode != adapter.ProbePassive || runtime.InvocationCount() != 0 {
		t.Fatalf("Probe() caps = %+v, invocations = %d", caps, runtime.InvocationCount())
	}

	var events []protocol.AgentEvent
	handle, err := runtime.Start(context.Background(), adapter.Invocation{InvocationID: "inv_1", AttemptID: "att_1", Role: domain.RoleImplementer}, func(event protocol.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Wait(context.Background(), handle)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.ResultCompleted || len(events) != 1 || runtime.InvocationCount() != 1 {
		t.Fatalf("result = %+v, events = %+v", result, events)
	}
}

func TestCancelBlockedInvocation(t *testing.T) {
	runtime := fake.New("fake-runtime", fake.Script{BlockUntilCancel: true})
	handle, err := runtime.Start(context.Background(), adapter.Invocation{InvocationID: "inv_1", AttemptID: "att_1", Role: domain.RoleImplementer}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Cancel(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Wait(context.Background(), handle); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want canceled", err)
	}
}
