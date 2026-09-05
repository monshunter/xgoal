package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
)

const (
	ReviewPacketVersion = "xgoal.review-packet/v1alpha1"
	ReviewResultVersion = "xgoal.review-result/v1alpha1"

	SchemaReviewPacket = "review-packet"
	SchemaReviewResult = "review-result"
)

type ReviewPacket struct {
	Harness                 *HarnessInput      `json:"harness,omitempty"`
	ProtocolVersion         string             `json:"protocol_version"`
	ID                      string             `json:"id"`
	GoalRevisionHash        string             `json:"goal_revision_hash"`
	PlanRevisionHash        string             `json:"plan_revision_hash"`
	WorkItemID              string             `json:"work_item_id"`
	ImplementationAttemptID string             `json:"implementation_attempt_id"`
	ImplementationProfileID string             `json:"implementation_profile_id"`
	ImplementationSessionID string             `json:"implementation_session_id"`
	BaseTree                string             `json:"base_tree"`
	CandidateTree           string             `json:"candidate_tree"`
	ConfigHash              string             `json:"config_hash"`
	Workspace               string             `json:"workspace"`
	PatchBundlePath         string             `json:"patch_bundle_path"`
	PatchBundleHash         string             `json:"patch_bundle_hash"`
	ValidatorReceipts       []ReviewReceiptRef `json:"validator_receipts"`
	RequiredChecks          []string           `json:"required_checks"`
	ReviewerProfileID       string             `json:"reviewer_profile_id"`
}

type ReviewReceiptRef struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Hash string `json:"hash"`
}

func (packet ReviewPacket) Validate() error {
	if packet.Harness != nil {
		if err := packet.Harness.Validate(); err != nil {
			return err
		}
	}
	if packet.ProtocolVersion != ReviewPacketVersion || !validLabel(packet.ID) || !validSHA256(packet.GoalRevisionHash) ||
		!validSHA256(packet.PlanRevisionHash) || !validLabel(packet.WorkItemID) || !validLabel(packet.ImplementationAttemptID) ||
		!validLabel(packet.ImplementationProfileID) || !validLabel(packet.ImplementationSessionID) || !validGitObjectID(packet.BaseTree) ||
		!validGitObjectID(packet.CandidateTree) || packet.BaseTree == packet.CandidateTree || !validSHA256(packet.ConfigHash) ||
		!cleanAbsolute(packet.Workspace) || !cleanAbsolute(packet.PatchBundlePath) || !validSHA256(packet.PatchBundleHash) ||
		!validLabel(packet.ReviewerProfileID) {
		return errors.New("review packet identity, revisions, workspace, patch, and reviewer profile are required")
	}
	if len(packet.ValidatorReceipts) == 0 || len(packet.RequiredChecks) == 0 {
		return errors.New("review packet requires validator receipts and review checks")
	}
	seenReceipts := make(map[string]struct{}, len(packet.ValidatorReceipts))
	for _, receipt := range packet.ValidatorReceipts {
		if !validLabel(receipt.ID) || !cleanAbsolute(receipt.Path) || !validSHA256(receipt.Hash) {
			return errors.New("review packet contains an invalid validator receipt")
		}
		if _, exists := seenReceipts[receipt.ID]; exists {
			return fmt.Errorf("review packet repeats validator receipt %q", receipt.ID)
		}
		seenReceipts[receipt.ID] = struct{}{}
	}
	seenChecks := make(map[string]struct{}, len(packet.RequiredChecks))
	for _, check := range packet.RequiredChecks {
		if !validLabel(check) {
			return errors.New("review packet contains an invalid required check")
		}
		if _, exists := seenChecks[check]; exists {
			return fmt.Errorf("review packet repeats required check %q", check)
		}
		seenChecks[check] = struct{}{}
	}
	return nil
}

func (packet ReviewPacket) Hash() (string, error) {
	if err := packet.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash(SchemaReviewPacket, ReviewPacketVersion, packet)
}

type ReviewStatus string

const (
	ReviewApproved         ReviewStatus = "approved"
	ReviewChangesRequested ReviewStatus = "changes_requested"
	ReviewBlocked          ReviewStatus = "blocked"
)

type FindingSeverity string

const (
	FindingBlocker FindingSeverity = "blocker"
	FindingHigh    FindingSeverity = "high"
	FindingMedium  FindingSeverity = "medium"
	FindingLow     FindingSeverity = "low"
	FindingNote    FindingSeverity = "note"
)

type FindingCategory string

const (
	FindingCorrectness     FindingCategory = "correctness"
	FindingRegression      FindingCategory = "regression"
	FindingTestGap         FindingCategory = "test_gap"
	FindingScope           FindingCategory = "scope"
	FindingSecurity        FindingCategory = "security"
	FindingMaintainability FindingCategory = "maintainability"
)

type ReviewResult struct {
	ProtocolVersion     string          `json:"protocol_version"`
	ReviewStatus        ReviewStatus    `json:"review_status"`
	Findings            []ReviewFinding `json:"findings"`
	SuggestedValidators []string        `json:"suggested_validators"`
}

type ReviewFinding struct {
	ID             string          `json:"id"`
	Severity       FindingSeverity `json:"severity"`
	Category       FindingCategory `json:"category"`
	Path           string          `json:"path"`
	Line           int64           `json:"line"`
	Claim          string          `json:"claim"`
	Basis          string          `json:"basis"`
	RecommendedFix string          `json:"recommended_fix"`
}

func (result ReviewResult) Validate() error {
	if result.ProtocolVersion != ReviewResultVersion || !validReviewStatus(result.ReviewStatus) {
		return errors.New("review result protocol version or status is invalid")
	}
	if result.ReviewStatus == ReviewChangesRequested && len(result.Findings) == 0 {
		return errors.New("changes_requested review requires findings")
	}
	seen := make(map[string]struct{}, len(result.Findings))
	for _, finding := range result.Findings {
		if !validLabel(finding.ID) || !validFindingSeverity(finding.Severity) || !validFindingCategory(finding.Category) ||
			(finding.Path != "" && !validBundlePath(finding.Path)) || finding.Line < 0 || !validLabel(finding.Claim) ||
			!validLabel(finding.Basis) || !validLabel(finding.RecommendedFix) {
			return fmt.Errorf("review finding %q is invalid", finding.ID)
		}
		if finding.Path == "" && finding.Line != 0 {
			return fmt.Errorf("review finding %q has a line without a path", finding.ID)
		}
		if _, exists := seen[finding.ID]; exists {
			return fmt.Errorf("review finding id %q is duplicated", finding.ID)
		}
		seen[finding.ID] = struct{}{}
	}
	seenValidators := make(map[string]struct{}, len(result.SuggestedValidators))
	for _, validatorID := range result.SuggestedValidators {
		if !validLabel(validatorID) {
			return errors.New("review result contains an invalid suggested validator")
		}
		if _, exists := seenValidators[validatorID]; exists {
			return fmt.Errorf("review result repeats suggested validator %q", validatorID)
		}
		seenValidators[validatorID] = struct{}{}
	}
	return nil
}

func (result ReviewResult) Hash() (string, error) {
	if err := result.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash(SchemaReviewResult, ReviewResultVersion, result)
}

func DecodeReviewResult(reader io.Reader, limit int64) (ReviewResult, error) {
	if limit <= 0 {
		return ReviewResult{}, errors.New("review result size limit must be positive")
	}
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return ReviewResult{}, err
	}
	if int64(len(content)) > limit {
		return ReviewResult{}, errors.New("review result exceeds its configured limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var result ReviewResult
	if err := decoder.Decode(&result); err != nil {
		return ReviewResult{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ReviewResult{}, errors.New("review result contains trailing JSON")
	}
	if err := result.Validate(); err != nil {
		return ReviewResult{}, err
	}
	return result, nil
}

func (ReviewResult) Authority() domain.Authority  { return domain.AuthorityInference }
func (ReviewFinding) Authority() domain.Authority { return domain.AuthorityInference }

func validReviewStatus(value ReviewStatus) bool {
	return value == ReviewApproved || value == ReviewChangesRequested || value == ReviewBlocked
}

func validFindingSeverity(value FindingSeverity) bool {
	return value == FindingBlocker || value == FindingHigh || value == FindingMedium || value == FindingLow || value == FindingNote
}

func validFindingCategory(value FindingCategory) bool {
	return value == FindingCorrectness || value == FindingRegression || value == FindingTestGap || value == FindingScope || value == FindingSecurity || value == FindingMaintainability
}

func cleanAbsolute(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
