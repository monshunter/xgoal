package review_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/review"
)

func TestStorePersistsCanonicalReviewArtifactsAndRejectsTampering(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "runtime")
	store, err := review.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	packet := validReviewPacket(t)
	artifact, err := store.SavePacket(packet)
	if err != nil || artifact.Hash == "" {
		t.Fatalf("SavePacket() = %+v, %v", artifact, err)
	}
	result := protocol.ReviewResult{ProtocolVersion: protocol.ReviewResultVersion, ReviewStatus: protocol.ReviewApproved, Findings: []protocol.ReviewFinding{}, SuggestedValidators: []string{}}
	resultArtifact, err := store.SaveResult(packet.ID, result)
	if err != nil || resultArtifact.Hash == "" {
		t.Fatalf("SaveResult() = %+v, %v", resultArtifact, err)
	}
	if _, hash, err := review.ReadPacket(artifact.Path); err != nil || hash != artifact.Hash {
		t.Fatalf("ReadPacket() = %q, %v", hash, err)
	}
	if _, hash, err := review.ReadResult(resultArtifact.Path); err != nil || hash != resultArtifact.Hash {
		t.Fatalf("ReadResult() = %q, %v", hash, err)
	}
	if err := os.Chmod(resultArtifact.Path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, _, err := review.ReadResult(resultArtifact.Path); err == nil {
		t.Fatal("ReadResult accepted writable artifact")
	}
}

func validReviewPacket(t *testing.T) protocol.ReviewPacket {
	t.Helper()
	return protocol.ReviewPacket{
		ProtocolVersion: protocol.ReviewPacketVersion, ID: "review_1",
		GoalRevisionHash: hex64("a"), PlanRevisionHash: hex64("b"), WorkItemID: "work_1",
		ImplementationAttemptID: "attempt_1", ImplementationProfileID: "codex-impl", ImplementationSessionID: "session-1",
		BaseTree: hex40("c"), CandidateTree: hex40("d"), ConfigHash: hex64("e"), Workspace: filepath.Join(t.TempDir(), "workspace"),
		PatchBundlePath: filepath.Join(t.TempDir(), "patch"), PatchBundleHash: hex64("f"),
		ValidatorReceipts: []protocol.ReviewReceiptRef{{ID: "receipt_1", Path: filepath.Join(t.TempDir(), "receipt"), Hash: hex64("1")}},
		RequiredChecks:    []string{"correctness"}, ReviewerProfileID: "claude-review",
	}
}

func hex64(character string) string { return repeat(character, 64) }
func hex40(character string) string { return repeat(character, 40) }
func repeat(character string, count int) string {
	result := ""
	for range count {
		result += character
	}
	return result
}
