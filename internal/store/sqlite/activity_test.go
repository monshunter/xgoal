package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/invocation"
)

func TestGoalActivitySeparatesHeartbeatOutputAndMaterialProgress(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
	source := clock.NewFake(start)
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	goal, work := seedReadyWork(t, s, "activity")
	read := func() map[string]any {
		t.Helper()
		status, err := s.GoalStatus(ctx, goal.ID)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(status)
		var decoded struct {
			Activity map[string]any `json:"activity"`
		}
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded.Activity
	}
	initial := read()
	if initial["last_material_progress_at"] != start.Format(time.RFC3339Nano) {
		t.Fatalf("initial activity=%v", initial)
	}
	attempt := domain.Attempt{ID: "attempt_activity", WorkItemID: work.ID, AgentProfileID: "p", State: domain.AttemptCreated, BaseTree: "tree", PacketHash: "packet", Version: 1}
	lease, err := s.ClaimWork(ctx, work.ID, work.Version, LeaseDraft{ID: "lease_activity", Holder: "daemon", TTL: time.Minute}, attempt, EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	input := invocation.Input{ID: "invocation_activity", GoalID: goal.ID, OwnerKind: "attempt", OwnerID: attempt.ID, Generation: 1, Role: "implementer", ProfileID: "p", Provider: "codex-cli", GoalRevisionHash: "revision", InputTree: "tree", PacketPath: "packet.json", PacketHash: "packet", PacketSHA256: strings.Repeat("a", 64), SchemaSHA256: strings.Repeat("b", 64), DelegationHash: "delegation", ProviderDir: "adapters/codex/invocations/invocation_activity", ExecutionConfig: config.ExecutionConfig{ProfileID: "p", Provider: "codex-cli", Role: "implementer"}}
	r, err := s.RegisterInvocation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	source.Advance(10 * time.Second)
	outputAt := source.Now()
	r.Observation.LastOutputAt = &outputAt
	r.Observation.Cursor = 1
	r.Observation.Bytes = 20
	if err := s.UpdateInvocation(ctx, input.ID, r.Version, r.Observation); err != nil {
		t.Fatal(err)
	}
	source.Advance(10 * time.Second)
	if _, err := s.HeartbeatLease(ctx, lease.ID, lease.Generation, lease.Version, time.Minute, EventInput{Type: "LeaseHeartbeat", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	activity := read()
	if activity["last_material_progress_at"] != initial["last_material_progress_at"] || activity["last_output_at"] != start.Add(10*time.Second).Format(time.RFC3339Nano) || activity["heartbeat_at"] != source.Now().Format(time.RFC3339Nano) {
		t.Fatalf("signals mixed: %v", activity)
	}
	gate, err := s.CreateGate(ctx, GateDraft{ID: "gate_activity", GoalID: goal.ID, WorkItemID: work.ID, ReasonCode: "SCOPE", Facts: []any{}, Unknowns: []any{}, Options: []any{"deny"}, Recommendation: "deny", Action: domain.ActionExpandScope, Scope: []string{"docs"}, ExpiresAt: source.Now().Add(time.Hour), MaxUses: 1, Revocable: true}, EventInput{Type: "GateOpened", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if activity := read(); activity["last_material_progress_at"] != initial["last_material_progress_at"] {
		t.Fatalf("opening Gate counted as progress: %v", activity)
	}
	source.Advance(10 * time.Second)
	if _, err := s.DecideGate(ctx, gate.ID, gate.Version, domain.GateDeny, "human", "keep scope", EventInput{Type: "GateDecided", ActorType: "human", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if activity := read(); activity["last_material_progress_at"] != source.Now().Format(time.RFC3339Nano) || activity["material_progress_kind"] != "gate_decision" {
		t.Fatalf("decision missing: %v", activity)
	}
	current, err := s.Goal(ctx, goal.ID)
	if err != nil || current.Version != goal.Version {
		t.Fatalf("observation changed Goal: %+v %v", current, err)
	}
}

func TestGoalActivityIgnoresRepeatedValidationAndOtherGoals(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC)
	source := clock.NewFake(start)
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seed := func(id string) domain.Goal {
		t.Helper()
		goal, work := seedReadyWork(t, s, id)
		attempt := domain.Attempt{ID: "attempt_" + id, WorkItemID: work.ID, AgentProfileID: "p", State: domain.AttemptCreated, BaseTree: strings.Repeat("1", 40), PacketHash: "packet", Version: 1}
		if _, err := s.ClaimWork(ctx, work.ID, work.Version, LeaseDraft{ID: "lease_" + id, Holder: "daemon", TTL: time.Hour}, attempt, EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReleaseLease(ctx, "lease_"+id, 1, 1, EventInput{Type: "LeaseReleased", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
		// Schema-valid rows isolate the activity projection from file/runner tests.
		if _, err := s.db.ExecContext(ctx, `INSERT INTO workspaces(id,attempt_id,kind,path,common_dir,base_commit,base_tree,config_hash,marker_hash,state,version,created_at,updated_at) VALUES (?,?,'VALIDATION',?,'git','commit','tree',?,?,'ACTIVE',1,?,?)`, id, attempt.ID, "/fixture/"+id, strings.Repeat("c", 64), fmt.Sprintf("%064s", id), start.Format(time.RFC3339Nano), start.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		return goal
	}
	goal := seed("one")
	other := seed("two")
	revision, _ := s.GoalRevision(ctx, goal.ActiveRevisionID)
	otherRevision, _ := s.GoalRevision(ctx, other.ActiveRevisionID)
	if revision.Hash != otherRevision.Hash {
		t.Fatal("fixture must share contract hash across Goals")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO validator_definitions VALUES (?,'check','command',1,CAST('{}' AS BLOB),?)`, strings.Repeat("d", 64), start.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	insert := func(id, owner, result string, at time.Time) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, `INSERT INTO validator_runs(id,definition_hash,attempt_id,workspace_id,receipt_hash,goal_revision_hash,config_hash,environment_hash,tree_hash,result,receipt_json,created_at) VALUES (?,?,NULL,?,?,?,?,?,?,?,CAST('{}' AS BLOB),?)`, id, strings.Repeat("d", 64), owner, fmt.Sprintf("%064s", id), revision.Hash, strings.Repeat("c", 64), fmt.Sprintf("%064s", id), strings.Repeat("1", 40), result, at.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	for i, result := range []string{"FAILED", "FAILED", "PASSED", "PASSED", "FAILED"} {
		at := start.Add(time.Duration(i+1) * time.Second)
		insert(fmt.Sprintf("run_%d", i), "one", result, at)
		want := []int{1, 1, 3, 3, 5}[i]
		activity, err := s.goalActivity(ctx, GoalStatus{Goal: goal})
		if err != nil || activity.LastMaterialProgressAt == nil || !activity.LastMaterialProgressAt.Equal(start.Add(time.Duration(want)*time.Second)) {
			t.Fatalf("step %d activity=%+v err=%v", i, activity, err)
		}
	}
	insert("other_run", "two", "PASSED", start.Add(time.Minute))
	activity, err := s.goalActivity(ctx, GoalStatus{Goal: goal})
	if err != nil || !activity.LastMaterialProgressAt.Equal(start.Add(5*time.Second)) {
		t.Fatalf("other Goal advanced activity=%+v err=%v", activity, err)
	}
	for i, version := range []string{"git 1", "git 1", "git 2"} {
		at := start.Add(time.Duration(20+i) * time.Second)
		payload, _ := json.Marshal(map[string]any{"id": fmt.Sprint(i), "captured_at": at, "git_version": version})
		if _, err := s.db.ExecContext(ctx, `INSERT INTO environment_snapshots(id,workspace_id,snapshot_hash,goal_revision_hash,config_hash,base_tree,isolation_level,payload_json,created_at) VALUES (?,'one',?,?,?,'tree','L0',?,?)`, fmt.Sprintf("env_%d", i), fmt.Sprintf("%064d", i), revision.Hash, strings.Repeat("c", 64), payload, at.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		activity, err := s.goalActivity(ctx, GoalStatus{Goal: goal})
		want := []int{20, 20, 22}[i]
		if err != nil || activity.LastMaterialProgressAt == nil || !activity.LastMaterialProgressAt.Equal(start.Add(time.Duration(want)*time.Second)) || activity.MaterialProgressKind != "environment_changed" {
			t.Fatalf("environment %d activity=%+v err=%v", i, activity, err)
		}
	}
}
