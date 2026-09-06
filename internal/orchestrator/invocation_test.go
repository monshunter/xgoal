package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/monshunter/xgoal/internal/clock"
	callindex "github.com/monshunter/xgoal/internal/invocation"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestObservationFailureCannotChangeProviderOutcome(t *testing.T) {
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	store.Close() // Inject an unavailable observation store at the return boundary.
	engine := &Engine{store: store}
	for _, cause := range []error{nil, errors.New("native provider failed")} {
		tracker := &invocationTracker{engine: engine, id: "invoke", notifier: callindex.NewNotifier(func(context.Context) {})}
		got := tracker.finish(context.Background(), "session", "completed", cause)
		if got != cause {
			t.Fatalf("index maintenance changed Provider outcome: %v -> %v", cause, got)
		}
	}
}
