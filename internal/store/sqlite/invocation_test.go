package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/invocation"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestInvocationIndexIsImmutableCASAndDoesNotChangeGoal(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	s, err := Open(ctx, root, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	goal := domain.Goal{ID: "goal_observe", State: domain.GoalDraft, Version: 1}
	if err := s.CreateGoal(ctx, goal, EventInput{Type: "GoalDrafted", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	in := invocation.Input{ID: "invoke_1", GoalID: goal.ID, OwnerKind: "planning", OwnerID: "effect_1", Generation: 1, Role: "planner", ProfileID: "p", Provider: "codex-cli", RequestHash: "request", InputTree: "tree", PacketPath: "planner/packet.json", PacketHash: "packet", PacketSHA256: strings.Repeat("a", 64), SchemaSHA256: strings.Repeat("b", 64), DelegationHash: "delegation", ProviderDir: "adapters/codex/plans/invoke_1", ExecutionConfig: config.ExecutionConfig{ProfileID: "p", Provider: "codex-cli", Role: "planner"}}
	record, err := s.RegisterInvocation(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.RegisterInvocation(ctx, in)
	if err != nil || again.InputHash != record.InputHash {
		t.Fatalf("same: %+v %v", again, err)
	}
	changed := in
	changed.Generation++
	if _, err := s.RegisterInvocation(ctx, changed); !errors.Is(err, basestore.ErrConflict) {
		t.Fatalf("identity overwrite: %v", err)
	}
	observation := record.Observation
	observation.Cursor = 1
	observation.Bytes = 100
	observation.SessionID = "session"
	if err := s.UpdateInvocation(ctx, in.ID, record.Version, observation); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateInvocation(ctx, in.ID, record.Version, record.Observation); !errors.Is(err, basestore.ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
	current, _ := s.Invocation(ctx, in.ID)
	observation.Cursor = 0
	if err := s.UpdateInvocation(ctx, in.ID, current.Version, observation); err == nil {
		t.Fatal("cursor regressed")
	}
	observation = current.Observation
	observation.Status = "returned"
	if err := s.UpdateInvocation(ctx, in.ID, current.Version, observation); err != nil {
		t.Fatal(err)
	}
	current, _ = s.Invocation(ctx, in.ID)
	observation.Status = "running"
	if err := s.UpdateInvocation(ctx, in.ID, current.Version, observation); err == nil {
		t.Fatal("terminal observation revived")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, root, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	records, err := s.Invocations(ctx, goal.ID, "")
	if err != nil || len(records) != 1 || records[0].Observation.Cursor != 1 {
		t.Fatalf("reopen: %+v %v", records, err)
	}
	got, err := s.Goal(ctx, goal.ID)
	if err != nil || got.Version != 1 || got.State != domain.GoalDraft {
		t.Fatalf("observation changed authority: %+v %v", got, err)
	}
	events, err := s.Events(ctx, "goal", goal.ID)
	if err != nil || len(events) != 1 {
		t.Fatalf("logs copied to business events: %d %v", len(events), err)
	}
}
