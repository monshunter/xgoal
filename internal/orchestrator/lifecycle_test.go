package orchestrator

import (
	"context"
	"errors"
	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/protocol"
	"strings"
	"testing"
	"time"
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

// A successful cancellation request is not evidence that the process exited.
type delayedExitAdapter struct {
	adapter.Adapter
	waiting      chan struct{}
	cancelCalled chan struct{}
	exited       chan struct{}
}

func (runtime *delayedExitAdapter) Wait(ctx context.Context, _ adapter.Handle) (protocol.AgentResult, error) {
	close(runtime.waiting)
	select {
	case <-ctx.Done():
		return protocol.AgentResult{}, ctx.Err()
	case <-runtime.exited:
		return protocol.AgentResult{}, errors.New("process interrupted")
	}
}
func (runtime *delayedExitAdapter) Cancel(ctx context.Context, _ adapter.Handle) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	close(runtime.cancelCalled)
	return nil
}
func TestWaitAgentConfirmsProcessExitAfterCancellation(t *testing.T) {
	runtime := &delayedExitAdapter{waiting: make(chan struct{}), cancelCalled: make(chan struct{}), exited: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan error, 1)
	engine := &Engine{}
	go func() { _, err := engine.waitAgent(ctx, runtime, adapter.Handle{}); completed <- err }()
	<-runtime.waiting
	cancel()
	select {
	case <-runtime.cancelCalled:
	case err := <-completed:
		close(runtime.exited)
		t.Fatalf("cancelled wait returned without stopping process: %v", err)
	case <-time.After(time.Second):
		close(runtime.exited)
		t.Fatal("process cancellation was not requested")
	}
	select {
	case err := <-completed:
		close(runtime.exited)
		t.Fatalf("process is still alive but wait returned: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(runtime.exited)
	select {
	case err := <-completed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation cause lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not complete after process exit")
	}
}
