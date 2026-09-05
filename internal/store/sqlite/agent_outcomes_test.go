package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/reconcile"
)

func TestAgentOutcomeMigrationPreservesFailureDecisionHistory(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	source := clock.NewFake(time.Now().UTC())
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	old, err := openWithMigrations(ctx, root, source, migrations[:10])
	if err != nil {
		t.Fatal(err)
	}
	goal, work := seedReadyWork(t, old, "outcome_work")
	failure := reconcile.Failure{Class: reconcile.ValidatorFailed, PrimaryError: "original failure", ValidatorDefinitionHash: "validator", BaseTree: "base", ResultTree: "result", GoalRevisionHash: "revision", RelevantConfigHash: "config"}
	event := EventInput{Type: "OutcomeRecorded", ActorType: "kernel", Payload: map[string]any{}}
	record, err := old.RecordFailure(ctx, FailureDraft{ID: "old_failure", GoalID: goal.ID, WorkItemID: work.ID, Failure: failure, Strategy: "fixture"}, event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.RecordReconcileDecision(ctx, ReconcileRecord{ID: "old_decision", FailureID: record.ID, Decision: reconcile.Decision{Action: reconcile.WaitGate, Reason: "original decision"}}, event); err != nil {
		t.Fatal(err)
	}
	// Removed accounting classes can remain in historical rows. Migration must
	// preserve them without making them legal for new Kernel writes.
	if _, err := old.db.ExecContext(ctx, `UPDATE failure_records SET failure_class='BUDGET_EXHAUSTED' WHERE id='old_failure'`); err != nil {
		t.Fatal(err)
	}
	before, err := old.Events(ctx, "goal", goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := Open(ctx, root, source)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	var class, fingerprint, reason string
	if err := current.db.QueryRowContext(ctx, `SELECT f.failure_class,f.fingerprint,d.reason FROM failure_records f JOIN reconcile_decisions d ON d.failure_id=f.id WHERE f.id='old_failure'`).Scan(&class, &fingerprint, &reason); err != nil {
		t.Fatal(err)
	}
	if class != "BUDGET_EXHAUSTED" || fingerprint != record.Fingerprint || reason != "original decision" {
		t.Fatalf("history changed: %s %s %s", class, fingerprint, reason)
	}
	after, err := current.Events(ctx, "goal", goal.ID)
	if err != nil || len(before) != len(after) {
		t.Fatalf("events changed: %d -> %d, %v", len(before), len(after), err)
	}
	for _, class := range []reconcile.FailureClass{reconcile.AgentBlocked, reconcile.AgentFailed} {
		failure.Class = class
		if _, err := current.RecordFailure(ctx, FailureDraft{ID: string(class), GoalID: goal.ID, WorkItemID: work.ID, Failure: failure, Strategy: "fixture"}, event); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := current.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left a broken foreign key")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
