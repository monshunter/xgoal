package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestEnvironmentFailureCreatesCurrentBoundEvidence(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	goal := domain.Goal{ID: "goal_environment_failure", State: domain.GoalDraft, Version: 1}
	if err := store.CreateGoal(ctx, goal, sqlite.EventInput{Type: "GoalCreated", ActorType: "human", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	revision, err := store.FreezeGoalRevision(ctx, sqlite.GoalRevisionDraft{ID: "revision_environment_failure", GoalID: goal.ID, Revision: 1, RawGoal: "prepare environment", Contract: map[string]any{"summary": "prepare"}}, 1, sqlite.EventInput{Type: "GoalRevisionFrozen", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{store: store, configHash: strings.Repeat("a", 64), config: config.Config{Runtime: config.Runtime{Provider: "local-process", IsolationLevelRequired: "L0", ProjectNetwork: "deny", ProjectSecrets: "deny"}}}
	id, err := engine.recordEnvironmentFailureEvidence(ctx, "work_environment", revision, strings.Repeat("b", 40), "attempt", errors.New("bootstrap failed in /tmp/random/path at 2026-09-02T01:02:03Z"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Evidence(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Evidence.Kind != "ENVIRONMENT_FAILURE" || snapshot.Evidence.SubjectID != "work_environment" || snapshot.Evidence.GoalRevisionHash != revision.Hash || snapshot.Evidence.ConfigHash != engine.configHash || snapshot.State != domain.EvidenceCurrent {
		t.Fatalf("environment evidence = %+v", snapshot)
	}
}
