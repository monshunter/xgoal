package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestProjectExecutionSlotRejectsOverlapAndDrainsOnShutdown(t *testing.T) {
	daemonContext, cancelDaemon := context.WithCancel(context.Background())
	defer cancelDaemon()
	state, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), ".xgoal"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	engine := &Engine{store: state, runtimeContext: daemonContext, runs: make(map[string]context.CancelFunc)}
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		acquired, err := engine.TryProjectExecution(context.Background(), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(cancelled)
			<-release
			return ctx.Err()
		})
		if !acquired {
			err = errors.New("idle project rejected probe")
		}
		finished <- err
	}()
	<-started
	called := false
	if acquired, err := engine.TryProjectExecution(context.Background(), func(context.Context) error { called = true; return nil }); acquired || err != nil || called {
		t.Fatalf("overlapping probe acquired=%v err=%v called=%v", acquired, err, called)
	}
	cancelDaemon()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("daemon stop did not cancel probe")
	}
	drained := make(chan struct{})
	go func() { engine.drainExecutions(); close(drained) }()
	select {
	case <-drained:
		close(release)
		t.Fatal("shutdown returned before probe cleanup finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("probe result = %v", err)
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join probe")
	}
	if acquired, err := engine.TryProjectExecution(context.Background(), func(context.Context) error { t.Error("probe started after shutdown"); return nil }); acquired || !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped project acquired=%v err=%v", acquired, err)
	}
}
