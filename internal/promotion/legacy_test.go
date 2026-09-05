package promotion_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/promotion"
)

func legacyPromotionRequest() promotion.Request {
	return promotion.Request{
		ID: "promotion_legacy", EffectID: "effect_legacy", EffectKey: "project/goal_legacy/promotion",
		GoalID: "goal_legacy", GoalRevision: 1, GoalRevisionHash: strings.Repeat("a", 64), ConfigHash: strings.Repeat("b", 64),
		WorkItemID: "work_legacy", AttemptID: "attempt_legacy", LeaseID: "lease_legacy", LeaseGeneration: 1,
		BundleHash: strings.Repeat("c", 64), EvidenceSetID: "evidence_legacy",
		IntegrationRef: "refs/heads/xgoal/goal_legacy/integration", OldCommit: strings.Repeat("1", 40),
		OldTree: strings.Repeat("2", 40), CandidateTree: strings.Repeat("3", 40), ValidationWorktree: "/legacy/validation/tree",
		CommitAt: time.Date(2026, 9, 2, 15, 0, 0, 0, time.UTC),
	}
}

func TestLegacyPromotionRequestHashIsStable(t *testing.T) {
	request := legacyPromotionRequest()
	got, err := request.Hash()
	const want = "b600f02a0e758ce4f95c658d8a9fd27f42f4374d476a196adee627121bae1f83"
	if err != nil || got != want {
		t.Fatalf("legacy request hash = %q, %v; want %s", got, err, want)
	}
	if !(promotion.Record{Request: request, State: promotion.Observed, IntegrationCommit: strings.Repeat("4", 40), Version: 4}).Valid() {
		t.Fatal("historical observed promotion is no longer readable")
	}
}

func TestLegacyPromotionCannotExecute(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo")
	initializePromotionRepository(t, root)
	repository, err := gitrepo.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	journal := &fakeJournal{}
	manager, err := promotion.NewManager(filepath.Join(t.TempDir(), "runtime"), repository, journal)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Promote(ctx, legacyPromotionRequest())
	if err == nil || !strings.Contains(err.Error(), "EXECUTION_MIGRATION_REQUIRED") {
		t.Fatalf("legacy promotion did not enter migration wait: %v", err)
	}
	if journal.record.Version != 0 {
		t.Fatal("legacy promotion changed its journal before migration rejection")
	}
}
