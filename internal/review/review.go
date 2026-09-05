package review

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	baseadapter "github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/patch"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/validator"
	"github.com/monshunter/xgoal/internal/workspace"
)

type Invocation struct {
	ExecutionConfig         *config.ExecutionConfig
	InvocationID            string
	ReviewID                string
	ReviewerProfileID       string
	ImplementationProfileID string
	ImplementationSessionID string
	GoalRevisionHash        string
	PlanRevisionHash        string
	BaseTree                string
	CandidateTree           string
	PacketHash              string
	WorkDir                 string
	PacketPath              string
	Prompt                  string
	OutputSchema            []byte
	Environment             map[string]string
	PermissionMode          string
	Tools                   []string
	Timeout                 time.Duration
	MaxOutputBytes          int64
}

type Execution struct {
	Result    protocol.ReviewResult
	SessionID string
}

type Adapter interface {
	ID() string
	Review(context.Context, Invocation, baseadapter.EventSink) (Execution, error)
}

type PrepareInput struct {
	Harness                 *protocol.HarnessInput
	ID                      string
	GoalRevisionHash        string
	PlanRevisionHash        string
	WorkItemID              string
	ImplementationAttemptID string
	ImplementationProfileID string
	ImplementationSessionID string
	ReviewerProfileID       string
	CandidateTree           string
	ValidationWorkspace     workspace.Snapshot
	ValidatorRunIDs         []string
	RequiredChecks          []string
}

type Coordinator struct {
	root  string
	store *Store
}

func NewCoordinator(runtimeRoot string) (*Coordinator, error) {
	store, err := NewStore(runtimeRoot)
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(runtimeRoot)
	if err != nil {
		return nil, err
	}
	return &Coordinator{root: root, store: store}, nil
}

func (coordinator *Coordinator) Prepare(ctx context.Context, input PrepareInput) (PacketArtifact, error) {
	if !component(input.ID) || !component(input.WorkItemID) || !component(input.ImplementationAttemptID) ||
		!component(input.ImplementationProfileID) || !component(input.ReviewerProfileID) ||
		input.ImplementationSessionID == "" || input.CandidateTree == "" || len(input.ValidatorRunIDs) == 0 {
		return PacketArtifact{}, errors.New("review preparation identity and reviewer profile are required")
	}
	marker, err := workspace.ReadMarkerSnapshot(input.ValidationWorkspace.MarkerPath)
	if err != nil || marker.ID != input.ValidationWorkspace.ID || marker.Kind != workspace.Validation || marker.AttemptID != input.ImplementationAttemptID || marker.Path != input.ValidationWorkspace.Path || marker.ExecutionModel != workspace.ExecutionCurrentDirectory || marker.Identity != input.ValidationWorkspace.Identity || !slices.Equal(marker.ExcludePaths, input.ValidationWorkspace.ExcludePaths) {
		return PacketArtifact{}, errors.New("review validation workspace does not match its immutable marker")
	}
	repository, err := gitrepo.Open(ctx, marker.Path)
	if err != nil {
		return PacketArtifact{}, err
	}
	if err := repository.CheckSnapshot(ctx, gitrepo.SnapshotSpec{BaseTree: marker.BaseTree, ExcludePaths: marker.ExcludePaths}, marker.Identity, input.CandidateTree); err != nil {
		return PacketArtifact{}, err
	}
	patchStore, err := patch.NewStore(coordinator.root)
	if err != nil {
		return PacketArtifact{}, err
	}
	captured, err := patchStore.Load(input.ImplementationAttemptID)
	if err != nil || captured.Bundle.BaseTree != marker.BaseTree {
		return PacketArtifact{}, errors.New("review patch bundle does not match the validation base")
	}
	receipts := make([]protocol.ReviewReceiptRef, 0, len(input.ValidatorRunIDs))
	for _, runID := range input.ValidatorRunIDs {
		receipt, err := validator.ReadReceipt(coordinator.root, runID)
		if err != nil || receipt.TreeHash != input.CandidateTree || receipt.GoalRevisionHash != input.GoalRevisionHash || receipt.ConfigHash != marker.ConfigHash {
			return PacketArtifact{}, errors.New("review validator receipt does not match goal/config/candidate tree")
		}
		hash, err := receipt.Hash()
		if err != nil {
			return PacketArtifact{}, err
		}
		receipts = append(receipts, protocol.ReviewReceiptRef{
			ID: runID, Path: filepath.Join(coordinator.root, "validator", "receipts", runID+".json"), Hash: hash,
		})
	}
	packet := protocol.ReviewPacket{
		Harness:         input.Harness,
		ProtocolVersion: protocol.ReviewPacketVersion, ID: input.ID,
		GoalRevisionHash: input.GoalRevisionHash, PlanRevisionHash: input.PlanRevisionHash,
		WorkItemID: input.WorkItemID, ImplementationAttemptID: input.ImplementationAttemptID,
		ImplementationProfileID: input.ImplementationProfileID, ImplementationSessionID: input.ImplementationSessionID,
		BaseTree: marker.BaseTree, CandidateTree: input.CandidateTree, ConfigHash: marker.ConfigHash,
		Workspace: marker.Path, PatchBundlePath: filepath.Join(coordinator.root, "patches", input.ImplementationAttemptID),
		PatchBundleHash: captured.Bundle.BundleHash, ValidatorReceipts: receipts,
		RequiredChecks: append([]string(nil), input.RequiredChecks...), ReviewerProfileID: input.ReviewerProfileID,
	}
	return coordinator.store.SavePacket(packet)
}

func ValidateInvocation(invocation Invocation) (protocol.ReviewPacket, error) {
	if e := invocation.ExecutionConfig; e != nil {
		if err := baseadapter.ValidateExecution(e, invocation.ReviewerProfileID, e.Provider, "reviewer"); err != nil {
			return protocol.ReviewPacket{}, err
		}
		if e.PermissionMode != invocation.PermissionMode || !slices.Equal(e.Tools, invocation.Tools) {
			return protocol.ReviewPacket{}, errors.New("review effective permissions differ from invocation")
		}
	} else if invocation.PermissionMode != "dontAsk" || len(invocation.Tools) == 0 {
		return protocol.ReviewPacket{}, errors.New("invalid review permissions")
	}
	if !component(invocation.InvocationID) || !component(invocation.ReviewID) || !component(invocation.ReviewerProfileID) ||
		!component(invocation.ImplementationProfileID) ||
		invocation.ImplementationSessionID == "" || invocation.GoalRevisionHash == "" || invocation.PlanRevisionHash == "" ||
		invocation.BaseTree == "" || invocation.CandidateTree == "" || invocation.PacketHash == "" ||
		!cleanAbsolute(invocation.WorkDir) || !cleanAbsolute(invocation.PacketPath) || strings.TrimSpace(invocation.Prompt) == "" ||
		invocation.Timeout <= 0 || invocation.MaxOutputBytes <= 0 {
		return protocol.ReviewPacket{}, errors.New("invalid review invocation")
	}
	for _, tool := range invocation.Tools {
		if tool != "Read" && tool != "Glob" && tool != "Grep" {
			return protocol.ReviewPacket{}, errors.New("review invocation contains a write-capable tool")
		}
	}
	packet, hash, err := ReadPacket(invocation.PacketPath)
	if err != nil || hash != invocation.PacketHash || packet.ID != invocation.ReviewID || packet.ReviewerProfileID != invocation.ReviewerProfileID ||
		packet.ImplementationProfileID != invocation.ImplementationProfileID || packet.ImplementationSessionID != invocation.ImplementationSessionID ||
		packet.GoalRevisionHash != invocation.GoalRevisionHash || packet.PlanRevisionHash != invocation.PlanRevisionHash || packet.BaseTree != invocation.BaseTree ||
		packet.CandidateTree != invocation.CandidateTree || packet.Workspace != invocation.WorkDir {
		return protocol.ReviewPacket{}, errors.New("review packet does not match invocation")
	}
	return packet, nil
}

func component(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 160 && utf8.ValidString(value) && !strings.ContainsAny(value, "/\\\r\n\x00")
}

func cleanAbsolute(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
