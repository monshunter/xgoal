package acceptance

import (
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/protocol"
)

// Request is journaled before invocation. No inherited environment is persisted.
type Request struct {
	Packet               Packet                 `json:"packet"`
	PacketPath           string                 `json:"packet_path"`
	PacketHash           string                 `json:"packet_hash"`
	GoalVersion          int64                  `json:"goal_version"`
	ExecutionConfig      config.ExecutionConfig `json:"execution_config"`
	PreviousInvocationID string                 `json:"previous_invocation_id,omitempty"`
	ReplaySafe           bool                   `json:"replay_safe"`
	RecoveryLimit        int                    `json:"recovery_limit"`
}

type Observation struct {
	Result           *protocol.AgentResult `json:"result,omitempty"`
	SessionID        string                `json:"session_id,omitempty"`
	FailureCode      string                `json:"failure_code,omitempty"`
	Reason           string                `json:"reason,omitempty"`
	ExecutionStopped bool                  `json:"execution_stopped"`
	Historical       bool                  `json:"historical"`
}

func ProcessID(invocationID string) string { return "process_" + invocationID }
func EffectID(invocationID string) string  { return "effect_" + invocationID }

const ReplayReason = "acceptance_replay_required"
