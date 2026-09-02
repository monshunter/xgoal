package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
	reviewartifact "github.com/monshunter/xgoal/internal/review"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestReviewAndFindingPersistenceRejectTamper(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, request, _ := seedPromotionFixture(t, "review")
	t.Cleanup(func() { _ = store.Close() })
	reviewStore, err := reviewartifact.NewStore(store.Info().ProjectDir)
	if err != nil {
		t.Fatal(err)
	}
	packet := protocol.ReviewPacket{ProtocolVersion: protocol.ReviewPacketVersion, ID: "review_1", GoalRevisionHash: request.GoalRevisionHash, PlanRevisionHash: strings.Repeat("7", 64), WorkItemID: request.WorkItemID, ImplementationAttemptID: request.AttemptID, ImplementationProfileID: "codex-implementer", ImplementationSessionID: "codex-session-1", BaseTree: request.OldTree, CandidateTree: request.CandidateTree, ConfigHash: request.ConfigHash, Workspace: request.ValidationWorktree, PatchBundlePath: filepath.Join(store.Info().ProjectDir, "patches", request.AttemptID), PatchBundleHash: request.BundleHash, ValidatorReceipts: []protocol.ReviewReceiptRef{{ID: "validator_run_1", Path: filepath.Join(store.Info().ProjectDir, "validator", "receipts", "validator_run_1.json"), Hash: strings.Repeat("8", 64)}}, RequiredChecks: []string{"correctness", "scope"}, ReviewerProfileID: "claude-reviewer"}
	packetArtifact, err := reviewStore.SavePacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	result := protocol.ReviewResult{ProtocolVersion: protocol.ReviewResultVersion, ReviewStatus: protocol.ReviewChangesRequested, Findings: []protocol.ReviewFinding{{ID: "finding_1", Severity: protocol.FindingHigh, Category: protocol.FindingCorrectness, Path: "internal/state.go", Line: 42, Claim: "state can drift", Basis: "transition is unchecked", RecommendedFix: "validate transition"}}, SuggestedValidators: []string{"state-race"}}
	resultArtifact, err := reviewStore.SaveResult(packet.ID, result)
	if err != nil {
		t.Fatal(err)
	}
	recorded, created, err := store.RecordReview(ctx, packetArtifact, resultArtifact, "claude-review-session-1")
	if err != nil || !created || len(recorded.Findings) != 1 || recorded.Findings[0].State != domain.FindingOpen {
		t.Fatalf("RecordReview() = %+v, %v, %v", recorded, created, err)
	}
	if _, created, err := store.RecordReview(ctx, packetArtifact, resultArtifact, "claude-review-session-1"); err != nil || created {
		t.Fatalf("idempotent RecordReview() = %v, %v", created, err)
	}
	if _, _, err := store.RecordReview(ctx, packetArtifact, resultArtifact, "different-session"); !errors.Is(err, basestore.ErrIdempotencyConflict) {
		t.Fatalf("conflicting RecordReview() = %v", err)
	}
	resolved, err := store.TransitionFinding(ctx, "finding_1", domain.FindingDisprovedByEvidence, "deterministic validator disproved claim", EventInput{Type: "ReviewFindingDisproved", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil || resolved.State != domain.FindingDisprovedByEvidence || resolved.StateSequence != 2 {
		t.Fatalf("TransitionFinding() = %+v, %v", resolved, err)
	}
	if _, err := store.TransitionFinding(ctx, "finding_1", domain.FindingWaivedByHuman, "late waiver", EventInput{Type: "Invalid", ActorType: "human", Payload: map[string]any{}}); err == nil {
		t.Fatal("terminal finding transitioned twice")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, store.Info().ProjectDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	read, err := reopened.Review(ctx, packet.ID)
	if err != nil || read.Findings[0].State != domain.FindingDisprovedByEvidence {
		t.Fatalf("reopened Review() = %+v, %v", read, err)
	}
	if err := os.Chmod(resultArtifact.Path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultArtifact.Path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Review(ctx, packet.ID); err == nil {
		t.Fatal("tampered review result was accepted")
	}
}
