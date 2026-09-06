package control

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestExportCleanupLockIsCancellableAndDoesNotBlockControlState(t *testing.T) {
	ctx := context.Background()
	project := t.TempDir()
	s, err := sqlite.Open(ctx, filepath.Join(project, "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	service, err := New(s, project)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGoal(ctx, domain.Goal{ID: "goal", State: domain.GoalDraft, Version: 1}, sqlite.EventInput{Type: "GoalCreated", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	release, err := service.lockArtifacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, name := range []string{"project.clean", "goal.exports"} {
		bounded, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		body := json.RawMessage(`{"dry_run":false}`)
		if name == "goal.exports" {
			body, _ = json.Marshal(map[string]string{"output": filepath.Join(t.TempDir(), "output")})
		}
		_, _, err := service.Execute(bounded, api.Operation{Name: name, ResourceID: "goal", Body: body})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s escaped shared lock: %v", name, err)
		}
	}
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, _, err := service.Query(bounded, api.Operation{Name: "goal.get", ResourceID: "goal"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Execute(bounded, api.Operation{Name: "goal.cancel", ResourceID: "goal", Body: json.RawMessage(`{"expected_version":1}`)}); err != nil {
		t.Fatalf("export blocked cancellation: %v", err)
	}
	goal, err := s.Goal(ctx, "goal")
	if err != nil || goal.State != domain.GoalCancelled {
		t.Fatalf("control did not progress: %+v %v", goal, err)
	}
}
