package orchestrator

import (
	"context"
	"strings"
	"testing"
)

func TestCancelWorkTargetsOnlyMatchingAttempt(t *testing.T) {
	engine := &Engine{workRuns: make(map[string]context.CancelCauseFunc)}
	first, cancelFirst := context.WithCancelCause(context.Background())
	second, cancelSecond := context.WithCancelCause(context.Background())
	defer cancelFirst(nil)
	defer cancelSecond(nil)
	engine.registerWork("work-1", cancelFirst)
	engine.registerWork("work-2", cancelSecond)

	engine.CancelWork("work-1")
	if cause := context.Cause(first); cause == nil || !strings.Contains(cause.Error(), "cancelled by operator") {
		t.Fatalf("matching work cancellation cause = %v", cause)
	}
	if cause := context.Cause(second); cause != nil {
		t.Fatalf("unrelated work was cancelled: %v", cause)
	}

	engine.unregisterWork("work-1")
	engine.CancelWork("work-1")
}
