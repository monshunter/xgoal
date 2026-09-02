package finalize

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/completion"
	"github.com/monshunter/xgoal/internal/report"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

type fakeStore struct {
	record sqlite.FinalReportRecord
	result completion.Result
}

func (store *fakeStore) FinalizeGoal(_ context.Context, goalID string, _ int64, facts sqlite.CompletionFacts, files report.PreparedFiles, _ sqlite.EventInput) (completion.Result, sqlite.FinalReportRecord, error) {
	store.record = sqlite.FinalReportRecord{Files: files, State: sqlite.FinalReportPending, Version: 1}
	return store.result, store.record, nil
}

func (store *fakeStore) MarkFinalReportCommitted(_ context.Context, _ string, version int64) (sqlite.FinalReportRecord, error) {
	if version != store.record.Version {
		return sqlite.FinalReportRecord{}, errors.New("version conflict")
	}
	store.record.State = sqlite.FinalReportCommitted
	store.record.Version++
	return store.record, nil
}

func (store *fakeStore) PendingFinalReports(context.Context) ([]sqlite.FinalReportRecord, error) {
	if store.record.State == sqlite.FinalReportPending {
		return []sqlite.FinalReportRecord{store.record}, nil
	}
	return nil, nil
}

func (store *fakeStore) FinalReport(context.Context, string) (sqlite.FinalReportRecord, error) {
	return store.record, nil
}

func TestRecoverRebuildsPendingReportAndCommitsState(t *testing.T) {
	root := t.TempDir()
	files, err := report.NewFileManager(root)
	if err != nil {
		t.Fatal(err)
	}
	artifact := testArtifact(t)
	prepared, err := files.Prepare("goal-1", artifact)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{record: sqlite.FinalReportRecord{Files: prepared, State: sqlite.FinalReportPending, Version: 1}}
	manager, _ := New(store, files)
	if err := manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.record.State != sqlite.FinalReportCommitted {
		t.Fatalf("state = %s", store.record.State)
	}
	if _, _, _, err := manager.Read(context.Background(), "goal-1"); err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(prepared.JSONPath) {
		t.Fatal("persisted report path must be relative")
	}
}

func testArtifact(t *testing.T) report.Artifact {
	t.Helper()
	h := func(value string, count int) string { return strings.Repeat(value, count) }
	value := report.Report{
		ProtocolVersion: report.ProtocolVersion,
		Goal:            report.GoalTrace{ID: "goal-1", Raw: "goal", Revision: 1, RevisionHash: h("1", 64), ConfigHash: h("2", 64), CreatedBy: "human", Authority: "DECISION"},
		Work:            []report.WorkTrace{{ID: "work-1", State: "COMPLETED", Title: "work", Required: true, Authority: "FACT"}},
		Final:           report.FinalTrace{Commit: h("3", 40), Tree: h("4", 64), EvidenceSetID: "set-1", Scope: []string{"internal/**"}, Authority: "FACT"},
		Criteria:        []report.CriterionTrace{{ID: "AC-1", Description: "done", Status: "PASS", EvidenceIDs: []string{"e-1"}, Authority: "DETERMINISTIC"}},
		Validators:      []report.ValidatorTrace{{ID: "v-1", Command: []string{"true"}, ReceiptHash: h("5", 64), Result: "PASSED", Reproduction: []string{"true"}, Authority: "DETERMINISTIC"}},
		Timestamps:      report.TimestampTrace{StartedAt: "2026-09-02T00:00:00Z", CompletedAt: "2026-09-02T00:01:00Z", Authority: "FACT"},
	}
	artifact, err := report.Render(value)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}
