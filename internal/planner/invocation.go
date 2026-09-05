package planner

import (
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/protocol"
)

// InvocationRecord is an immutable input reference, not a second planning state.
// Initial planning has request/generation/tree provenance before a Goal Revision exists.
type InvocationRecord struct {
	DelegationHash  string                  `json:"delegation_hash"`
	ProtocolVersion string                  `json:"protocol_version"`
	InvocationID    string                  `json:"invocation_id"`
	ProfileID       string                  `json:"profile_id"`
	RequestHash     string                  `json:"request_hash,omitempty"`
	InputTree       string                  `json:"input_tree,omitempty"`
	Generation      int64                   `json:"generation,omitempty"`
	PacketHash      string                  `json:"packet_hash"`
	PacketPath      string                  `json:"packet_path"`
	ExecutionConfig *config.ExecutionConfig `json:"execution_config,omitempty"`
}

func (i Invocation) Record() InvocationRecord {
	return InvocationRecord{DelegationHash: protocol.DelegationHash(), ProtocolVersion: "xgoal.planner-invocation/v1alpha1", InvocationID: i.InvocationID, ProfileID: i.ProfileID, RequestHash: i.RequestHash, InputTree: i.InputTree, Generation: i.Generation, PacketHash: i.PacketHash, PacketPath: i.PacketPath, ExecutionConfig: i.ExecutionConfig}
}
