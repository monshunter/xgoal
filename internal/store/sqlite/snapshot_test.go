package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
)

func TestReadSnapshotIncludesWALWithoutTakingControlConnection(t *testing.T) {
	ctx := context.Background()
	s, r := planningFixture(t)
	p := acceptPlanning(t, s, r)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	// Holding the sole control connection must not block the independent reader.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	snapshot, boundary, err := s.ReadSnapshot(bounded, filepath.Join(parent, "state.db"))
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if boundary.EventCount == 0 || len(boundary.Events) == 0 {
		t.Fatalf("missing WAL events: %+v", boundary)
	}
	before, err := snapshot.Goal(ctx, r.GoalID)
	if err != nil || before.Version != p.Goal.Version {
		t.Fatalf("WAL goal missing: %+v %v", before, err)
	}
	if err := s.UpdateGoalState(ctx, r.GoalID, p.Goal.Version, domain.GoalCancelled, EventInput{Type: "GoalCancelled", ActorType: "human", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	after, err := snapshot.Goal(ctx, r.GoalID)
	if err != nil || after != before {
		t.Fatalf("snapshot changed with live state: %+v %v", after, err)
	}
	if err := snapshot.UpdateGoalState(ctx, r.GoalID, before.Version, domain.GoalCancelled, EventInput{Type: "Forbidden", ActorType: "human", Payload: map[string]any{}}); err == nil {
		t.Fatal("snapshot Store is writable")
	}
	if _, _, err := s.ReadSnapshot(ctx, filepath.Join(parent, "state.db")); err == nil {
		t.Fatal("snapshot overwrote an existing destination")
	}
}
