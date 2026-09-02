package protocol_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
)

func TestReviewPacketAndResultContracts(t *testing.T) {
	t.Parallel()
	packet := protocol.ReviewPacket{
		ProtocolVersion: protocol.ReviewPacketVersion, ID: "review_packet_1",
		GoalRevisionHash: strings.Repeat("a", 64), PlanRevisionHash: strings.Repeat("b", 64),
		WorkItemID: "work_1", ImplementationAttemptID: "attempt_1", ImplementationProfileID: "codex-impl",
		ImplementationSessionID: "session-1", BaseTree: strings.Repeat("c", 40), CandidateTree: strings.Repeat("d", 40),
		ConfigHash: strings.Repeat("e", 64), Workspace: filepath.Join(t.TempDir(), "workspace"),
		PatchBundlePath: filepath.Join(t.TempDir(), "patch"), PatchBundleHash: strings.Repeat("f", 64),
		ValidatorReceipts: []protocol.ReviewReceiptRef{{ID: "receipt_1", Path: filepath.Join(t.TempDir(), "receipt.json"), Hash: strings.Repeat("1", 64)}},
		RequiredChecks:    []string{"correctness", "scope"}, ReviewerProfileID: "claude-review",
	}
	if hash, err := packet.Hash(); err != nil || hash == "" {
		t.Fatalf("ReviewPacket.Hash() = %q, %v", hash, err)
	}
	packet.ReviewerProfileID = packet.ImplementationProfileID
	if err := packet.Validate(); err != nil {
		t.Fatalf("ReviewPacket rejected a shared runtime profile with an independently enforced session: %v", err)
	}

	result := protocol.ReviewResult{
		ProtocolVersion: protocol.ReviewResultVersion, ReviewStatus: protocol.ReviewChangesRequested,
		Findings: []protocol.ReviewFinding{{
			ID: "client-finding-1", Severity: protocol.FindingHigh, Category: protocol.FindingCorrectness,
			Path: "internal/lease.go", Line: 42, Claim: "lease can be reused", Basis: "generation is not compared", RecommendedFix: "compare the generation",
		}},
		SuggestedValidators: []string{"lease-race"},
	}
	if hash, err := result.Hash(); err != nil || hash == "" || result.Authority() != domain.AuthorityInference || result.Findings[0].Authority() != domain.AuthorityInference {
		t.Fatalf("ReviewResult contract = %q, %v, %s", hash, err, result.Authority())
	}
	result.Findings = nil
	if err := result.Validate(); err == nil {
		t.Fatal("changes_requested ReviewResult accepted no findings")
	}
}

func TestDecodeReviewResultRejectsUnknownAndTrailingFields(t *testing.T) {
	t.Parallel()
	valid := `{"protocol_version":"xgoal.review-result/v1alpha1","review_status":"approved","findings":[],"suggested_validators":[]}`
	if _, err := protocol.DecodeReviewResult(bytes.NewBufferString(valid), int64(len(valid))); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{"protocol_version":"xgoal.review-result/v1alpha1","review_status":"approved","findings":[],"suggested_validators":[],"extra":true}`,
		valid + ` {}`,
	} {
		if _, err := protocol.DecodeReviewResult(bytes.NewBufferString(invalid), int64(len(invalid))); err == nil {
			t.Fatalf("accepted invalid review result %q", invalid)
		}
	}
}
