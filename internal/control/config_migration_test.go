package control_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/control"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestLegacyConfigPreservesReadsAndCancellationWhileBlockingExecution(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateGoal(ctx, domain.Goal{ID: "retained", State: domain.GoalDraft, Version: 1}, sqlite.EventInput{Type: "GoalCreated", ActorType: "human", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	configuration, err := os.ReadFile("../../xgoal.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(string(configuration), "provider: current-directory", "provider: git-worktree", 1)
	if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	service, err := control.New(store, root)
	if err != nil {
		t.Fatalf("legacy configuration blocked historical reads: %v", err)
	}
	code, result, err := service.Query(ctx, api.Operation{Name: "goal.get", ResourceID: "retained"})
	if err != nil || code != http.StatusOK {
		t.Fatalf("historical status: %d %v", code, err)
	}
	status := result.(map[string]any)
	if status["execution_available"] != false || !strings.Contains(status["execution_blocker"].(string), "CONFIG_MIGRATION_REQUIRED") || status["execution_model"] != "current-directory" {
		t.Fatalf("legacy execution availability is ambiguous: %+v", status)
	}
	for _, operation := range []string{"goal.create", "goal.pause", "goal.resume", "work.retry", "goal.replan", "goal.finalize", "gate.decide", "doctor.active-probe", "project.clean"} {
		_, _, err := service.Execute(ctx, api.Operation{Name: operation, ResourceID: "retained", Body: raw(map[string]any{"goal_id": "must_not_exist", "raw_goal": "no", "mode": "standard"})})
		var failure *api.APIError
		if !errors.As(err, &failure) || failure.Code != "CONFIG_MIGRATION_REQUIRED" {
			t.Fatalf("legacy config accepted execution operation %s: %v", operation, err)
		}
	}
	if _, err := store.Goal(ctx, "must_not_exist"); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("rejected request created a Goal: %v", err)
	}
	code, _, err = service.Execute(ctx, api.Operation{Name: "goal.cancel", ResourceID: "retained", Body: raw(map[string]any{"expected_version": 1, "reason": "retain history and stop"})})
	if err != nil || code != http.StatusOK {
		t.Fatalf("safe Goal cancellation was blocked: %d %v", code, err)
	}
	_, _, err = service.Execute(ctx, api.Operation{Name: "work.cancel", ResourceID: "missing_work", Body: raw(map[string]any{"expected_version": 1, "reason": "safe cancellation"})})
	var failure *api.APIError
	if !errors.As(err, &failure) || failure.Code != "NOT_FOUND" {
		t.Fatalf("safe Work cancellation did not reach its metadata operation: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), []byte(legacy+"\nunknownField: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := control.New(store, root); err == nil || !strings.Contains(err.Error(), "unknownField") {
		t.Fatalf("legacy migration hid an unknown configuration field: %v", err)
	}
}
