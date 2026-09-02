package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/completion"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/evidence"
	"github.com/monshunter/xgoal/internal/protocol"
	finalreport "github.com/monshunter/xgoal/internal/report"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestFinalizeGoalCommitsReportAndGoalTupleAtomically(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	source := clock.NewFake(time.Date(2026, 9, 2, 20, 0, 0, 0, time.UTC))
	store, err := Open(ctx, root, source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	goal, facts, files := seedFinalizableReport(t, store, root, source)

	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER reject_final_report
BEFORE INSERT ON final_reports
BEGIN SELECT RAISE(ABORT, 'injected report failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.FinalizeGoal(ctx, goal.ID, goal.Version, facts, files, EventInput{Type: "GoalCompleted", ActorType: "kernel", Payload: map[string]any{}}); err == nil {
		t.Fatal("injected report insertion unexpectedly succeeded")
	}
	unchanged, _ := store.Goal(ctx, goal.ID)
	if unchanged.State != domain.GoalVerifying || unchanged.FinalReportHash != "" {
		t.Fatalf("goal escaped rolled-back finalization: %+v", unchanged)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_final_report`); err != nil {
		t.Fatal(err)
	}

	result, record, err := store.FinalizeGoal(ctx, goal.ID, goal.Version, facts, files, EventInput{Type: "GoalCompleted", ActorType: "kernel", Payload: map[string]any{"report_hash": files.ReportHash}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || record.State != FinalReportPending || record.Version != 1 {
		t.Fatalf("finalization = %+v, %+v", result, record)
	}
	completed, _ := store.Goal(ctx, goal.ID)
	if completed.State != domain.GoalCompleted || completed.FinalTree != facts.IntegrationTree || completed.FinalEvidenceSetID != facts.FinalEvidenceSetID || completed.FinalReportHash != files.ReportHash {
		t.Fatalf("completed Goal tuple = %+v", completed)
	}
	manager, _ := finalreport.NewFileManager(root)
	if err := manager.Commit(files); err != nil {
		t.Fatal(err)
	}
	committed, err := store.MarkFinalReportCommitted(ctx, goal.ID, record.Version)
	if err != nil {
		t.Fatal(err)
	}
	if committed.State != FinalReportCommitted || committed.Version != 2 {
		t.Fatalf("committed report = %+v", committed)
	}
	replayed, err := store.MarkFinalReportCommitted(ctx, goal.ID, record.Version)
	if err != nil || replayed.State != FinalReportCommitted || replayed.Version != committed.Version {
		t.Fatalf("idempotent report commit = %+v, %v", replayed, err)
	}
}

func TestFinalizeGoalRechecksCurrentEvidenceAndCriteriaMapping(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	source := clock.NewFake(time.Date(2026, 9, 2, 20, 0, 0, 0, time.UTC))
	store, err := Open(ctx, root, source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	goal, facts, files := seedFinalizableReport(t, store, root, source)
	if _, err := store.TransitionEvidence(ctx, "evidence_final", domain.EvidenceStale, "tree changed"); err != nil {
		t.Fatal(err)
	}
	result, _, err := store.FinalizeGoal(ctx, goal.ID, goal.Version, facts, files, EventInput{Type: "GoalCompleted", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !containsReason(result.Reasons, "final validation set is not current") {
		t.Fatalf("stale evidence result = %+v", result)
	}
	if _, err := store.FinalReport(ctx, goal.ID); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("FinalReport after rejected completion = %v", err)
	}
}

func TestFinalizeGoalRejectsReportCriteriaDifferentFromCompletionFacts(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	source := clock.NewFake(time.Date(2026, 9, 2, 20, 0, 0, 0, time.UTC))
	store, err := Open(ctx, root, source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	goal, facts, files := seedFinalizableReport(t, store, root, source)
	facts.Criteria[0].ID = "AC-DIFFERENT"
	if _, _, err := store.FinalizeGoal(ctx, goal.ID, goal.Version, facts, files, EventInput{Type: "GoalCompleted", ActorType: "kernel", Payload: map[string]any{}}); err == nil {
		t.Fatal("different criteria mapping unexpectedly finalized")
	}
}

func seedFinalizableReport(t *testing.T, store *Store, root string, source *clock.Fake) (domain.Goal, CompletionFacts, finalreport.PreparedFiles) {
	t.Helper()
	ctx := context.Background()
	goal, work := seedReadyWork(t, store, "work_final")
	lease := claimAndCompleteWork(t, store, work, "attempt_final", "lease_final")
	if _, err := store.ReleaseLease(ctx, lease.ID, lease.Generation, lease.Version, EventInput{Type: "LeaseReleased", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateGoalState(ctx, goal.ID, goal.Version, domain.GoalVerifying, EventInput{Type: "GoalVerifying", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	goal.State = domain.GoalVerifying
	goal.Version++
	revision, err := store.GoalRevision(ctx, goal.ActiveRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	tree := strings.Repeat("a", 64)
	configHash := strings.Repeat("b", 64)
	record := evidence.Record{
		Evidence: protocol.Evidence{
			ProtocolVersion: protocol.EvidenceVersion, ID: "evidence_final", Kind: "validator", SubjectID: goal.ID,
			Producer: "validator/go-test", Authority: domain.AuthorityDeterministic, GoalRevisionHash: revision.Hash,
			ConfigHash: configHash, TreeHash: tree, PayloadHash: strings.Repeat("c", 64), State: domain.EvidenceCurrent, CreatedAt: source.Now(),
		},
		DefinitionHash: strings.Repeat("d", 64), EnvironmentHash: strings.Repeat("e", 64), ReceiptHash: strings.Repeat("f", 64),
	}
	if err := store.AppendEvidence(ctx, record); err != nil {
		t.Fatal(err)
	}
	set, err := evidence.NewSet("evidence_set_final", evidence.SetFinal, revision.Hash, configHash, tree, []string{record.Evidence.ID}, source.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEvidenceSet(ctx, set); err != nil {
		t.Fatal(err)
	}
	reportValue := finalreport.Report{
		ProtocolVersion: finalreport.ProtocolVersion,
		Goal:            finalreport.GoalTrace{ID: goal.ID, Raw: revision.RawGoal, Revision: revision.Revision, RevisionHash: revision.Hash, ConfigHash: configHash, CreatedBy: "human", Authority: domain.AuthorityDecision},
		Work:            []finalreport.WorkTrace{{ID: work.ID, State: string(domain.WorkCompleted), Title: work.Title, Required: true, Authority: domain.AuthorityFact}},
		Attempts:        []finalreport.AttemptTrace{{ID: "attempt_final", WorkID: work.ID, Role: "implementer", Provider: "test", State: string(domain.AttemptSucceeded), PacketHash: strings.Repeat("1", 64), ResultTree: tree, Authority: domain.AuthorityFact}},
		Final:           finalreport.FinalTrace{Commit: strings.Repeat("2", 40), Tree: tree, EvidenceSetID: set.ID, Scope: []string{"internal/**"}, Authority: domain.AuthorityFact},
		Criteria:        []finalreport.CriterionTrace{{ID: "AC-1", Description: "tests pass", Status: "PASS", EvidenceIDs: []string{record.Evidence.ID}, ValidatorIDs: []string{"go-test"}, Authority: domain.AuthorityDeterministic}},
		Validators:      []finalreport.ValidatorTrace{{ID: "go-test", Command: []string{"go", "test", "./..."}, ReceiptHash: record.ReceiptHash, Result: "PASSED", Reproduction: []string{"go", "test", "./..."}, Authority: domain.AuthorityDeterministic}},
		Execution:       []finalreport.ExecutionMetric{{Name: "attempts", Unit: "attempt", Known: true, Value: 1, Authority: domain.AuthorityFact}},
		Limitations:     []finalreport.Statement{{Text: "L0 isolation", Authority: domain.AuthorityFact}},
		Timestamps:      finalreport.TimestampTrace{StartedAt: source.Now().Add(-time.Minute).Format(time.RFC3339Nano), CompletedAt: source.Now().Format(time.RFC3339Nano), Authority: domain.AuthorityFact},
	}
	artifact, err := finalreport.Render(reportValue)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := finalreport.NewFileManager(root)
	if err != nil {
		t.Fatal(err)
	}
	files, err := manager.Prepare(goal.ID, artifact)
	if err != nil {
		t.Fatal(err)
	}
	facts := CompletionFacts{
		IntegrationTree: tree, ExpectedTree: tree,
		Criteria:          []completion.CriterionStatus{{ID: "AC-1", Satisfied: true, Current: true, TreeHash: tree}},
		ScopePolicyPassed: true, FinalValidationSetCurrent: true, FinalEvidenceSetID: set.ID,
		FinalReportHash: artifact.ReportHash, HumanAcceptanceSatisfied: true,
	}
	return goal, facts, files
}

func containsReason(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
