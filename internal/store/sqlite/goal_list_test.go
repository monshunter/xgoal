package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/domain"
)

func TestGoalListPaginationAndReadOnly(t *testing.T) {
	s, _ := planningFixture(t)
	ctx := context.Background()
	page, err := s.ListGoals(ctx, GoalListQuery{Limit: 100})
	if err != nil || page.Items == nil || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("empty=%+v %v", page, err)
	}
	var expected []string
	for i := 0; i < 105; i++ {
		id := fmt.Sprintf("goal_%03d", i)
		expected = append(expected, id)
		if err := s.CreateGoal(ctx, domain.Goal{ID: id, State: domain.GoalDraft, Version: 1}, EventInput{Type: "GoalCreated", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	var eventsBefore int
	s.db.QueryRow(`SELECT count(*) FROM events`).Scan(&eventsBefore)
	var got []string
	after := ""
	for {
		page, err = s.ListGoals(ctx, GoalListQuery{Limit: 100, After: after})
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range page.Items {
			got = append(got, g.GoalID)
			if g.State != domain.GoalDraft || g.Version != 1 || g.Summary != "" || g.PlanningState != "WAITING" || g.CreatedAt.IsZero() || g.UpdatedAt.Before(g.CreatedAt) {
				t.Fatalf("item=%+v", g)
			}
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == after {
			t.Fatal("cursor did not advance")
		}
		after = page.NextCursor
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("pagination got %d ids want %d", len(got), len(expected))
	}
	if err := s.UpdateGoalState(ctx, "goal_050", 1, domain.GoalCancelled, EventInput{Type: "Cancelled", ActorType: "human", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	page, err = s.ListGoals(ctx, GoalListQuery{Limit: 1, State: domain.GoalCancelled, After: encodeGoalCursor(50, "goal_049")})
	if err != nil || len(page.Items) != 1 || page.Items[0].GoalID != "goal_050" || page.Items[0].Version != 2 || page.NextCursor != "" {
		t.Fatalf("filter=%+v %v", page, err)
	}
	page, err = s.ListGoals(ctx, GoalListQuery{Limit: 1, After: encodeGoalCursor(105, "goal_104")})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("after end=%+v %v", page, err)
	}
	var eventsAfter int
	s.db.QueryRow(`SELECT count(*) FROM events`).Scan(&eventsAfter)
	if eventsAfter != eventsBefore+1 {
		t.Fatal("list wrote events")
	}
	for _, q := range []GoalListQuery{{Limit: 0}, {Limit: 101}, {Limit: 1, State: "FAILED"}, {Limit: 1, After: "a\n"}, {Limit: 1, After: strings.Repeat("x", 257)}} {
		if _, err := s.ListGoals(ctx, q); !errors.Is(err, ErrInvalidGoalListQuery) {
			t.Fatalf("invalid query accepted: %+v %v", q, err)
		}
	}
}

func TestGoalListSummaryAndPlanningProjection(t *testing.T) {
	s, r := planningFixture(t)
	ctx := context.Background()
	r.RawGoal = "编写游戏\nAPI_KEY=secret-value " + strings.Repeat("界", 200)
	p := acceptPlanning(t, s, r)
	check := func() GoalSummary {
		t.Helper()
		page, err := s.ListGoals(ctx, GoalListQuery{Limit: 100})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("list=%+v %v", page, err)
		}
		actual, err := s.Planning(ctx, r.GoalID)
		if err != nil {
			t.Fatal(err)
		}
		g := page.Items[0]
		if g.PlanningState != actual.State {
			t.Fatalf("list planning=%s status=%s", g.PlanningState, actual.State)
		}
		return g
	}
	g := check()
	if g.PlanningState != "QUEUED" || strings.Contains(g.Summary, "secret-value") || strings.Contains(g.Summary, "\n") || !strings.Contains(g.Summary, "[REDACTED]") || utf8.RuneCountInString(g.Summary) != 160 || !strings.HasSuffix(g.Summary, "…") {
		t.Fatalf("summary=%+v", g)
	}
	if _, err := s.SetPlanningPaused(ctx, r.GoalID, p.Goal.Version, true, "operator pause"); err != nil {
		t.Fatal(err)
	}
	if g = check(); g.PlanningState != "PAUSED" {
		t.Fatalf("paused=%+v", g)
	}
	// A frozen active revision owns the summary, including after replanning.
	if _, err := s.db.Exec(`INSERT INTO goal_revisions(id,goal_id,revision,raw_goal,contract_json,contract_hash,frozen_at) VALUES(?,?,1,'old raw',?,?,?)`, "revision", r.GoalID, []byte(`{"summary":"冻结目标"}`), strings.Repeat("a", 64), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE goals SET active_revision_id='revision',state='READY' WHERE id=?`, r.GoalID); err != nil {
		t.Fatal(err)
	}
	if g = check(); g.Summary != "冻结目标" {
		t.Fatalf("active summary=%+v", g)
	}
}

func TestGoalListLiteralCursorAndStates(t *testing.T) {
	s, _ := planningFixture(t)
	ctx := context.Background()
	states := []domain.GoalState{domain.GoalDraft, domain.GoalReady, domain.GoalRunning, domain.GoalWaiting, domain.GoalVerifying, domain.GoalCompleted, domain.GoalCancelled}
	for i, state := range states {
		id := fmt.Sprintf("goal_%d?&#%%'", i)
		if err := s.CreateGoal(ctx, domain.Goal{ID: id, State: domain.GoalDraft, Version: 1}, EventInput{Type: "GoalCreated", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
		tree, evidence, report := "", "", ""
		if state == domain.GoalCompleted {
			tree = "tree"
			evidence = "evidence"
			report = "report"
		}
		if _, err := s.db.Exec(`UPDATE goals SET state=?,final_tree=?,final_evidence_set_id=?,final_report_hash=? WHERE id=?`, state, tree, evidence, report, id); err != nil {
			t.Fatal(err)
		}
		page, err := s.ListGoals(ctx, GoalListQuery{Limit: 1, State: state})
		if err != nil || len(page.Items) != 1 || page.Items[0].GoalID != id {
			t.Fatalf("state=%s page=%+v %v", state, page, err)
		}
		page, err = s.ListGoals(ctx, GoalListQuery{Limit: 1, State: state, After: encodeGoalCursor(int64(i+1), id)})
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("literal cursor=%+v %v", page, err)
		}
	}
}

func TestGoalListCursorRoundTripsLegacyIDs(t *testing.T) {
	s, _ := planningFixture(t)
	ctx := context.Background()
	ids := []string{"a" + strings.Repeat("长", 12000) + "\t", "b_last"}
	for _, id := range ids {
		if err := s.CreateGoal(ctx, domain.Goal{ID: id, State: domain.GoalDraft, Version: 1}, EventInput{Type: "GoalCreated", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListGoals(ctx, GoalListQuery{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].GoalID != ids[0] || first.NextCursor == "" {
		t.Fatalf("first=%+v %v", first, err)
	}
	second, err := s.ListGoals(ctx, GoalListQuery{Limit: 1, After: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.Items[0].GoalID != ids[1] || second.NextCursor != "" {
		t.Fatalf("second=%+v %v", second, err)
	}
}

func TestGoalListRejectsStaleCursor(t *testing.T) {
	s, _ := planningFixture(t)
	ctx := context.Background()
	if err := s.CreateGoal(ctx, domain.Goal{ID: "one", State: domain.GoalDraft, Version: 1}, EventInput{Type: "GoalCreated", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	for _, cursor := range []string{encodeGoalCursor(99, "missing"), encodeGoalCursor(1, "previous")} {
		if _, err := s.ListGoals(ctx, GoalListQuery{Limit: 1, After: cursor}); !errors.Is(err, ErrInvalidGoalListQuery) {
			t.Fatalf("stale cursor=%v", err)
		}
	}
}
