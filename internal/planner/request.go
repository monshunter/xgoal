package planner

import (
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/gitrepo"
)

const RequestVersion = "xgoal.planning-request/v1"

// Request is the immutable input of one durable planning generation.
type Request struct {
	ValidationCapabilities *config.ValidationCapabilities `json:"validation_capabilities,omitempty"`
	ProtocolVersion        string                         `json:"protocol_version"`
	GoalID                 string                         `json:"goal_id"`
	RawGoal                string                         `json:"raw_goal"`
	Mode                   string                         `json:"mode"`
	CreatedBy              string                         `json:"created_by"`
	ConfigHash             string                         `json:"config_hash"`
	ProfileID              string                         `json:"profile_id"`
	Generation             int64                          `json:"generation"`
	TrustedValidatorIDs    []string                       `json:"trusted_validator_ids"`
	Proposal               *Proposal                      `json:"proposal,omitempty"`
	BlockedReason          string                         `json:"blocked_reason,omitempty"`
}

// Observation preserves the input identity when adding a result or failure.
// A nil Proposal records a start or interruption, never a publishable result.
type Observation struct {
	InvocationID     string                   `json:"invocation_id"`
	InputTree        string                   `json:"input_tree"`
	CheckoutIdentity gitrepo.CheckoutIdentity `json:"checkout_identity"`
	Proposal         *Proposal                `json:"proposal,omitempty"`
	SessionID        string                   `json:"session_id,omitempty"`
	FailureCode      string                   `json:"failure_code,omitempty"`
	FailureReason    string                   `json:"failure_reason,omitempty"`
	ExecutionStopped bool                     `json:"execution_stopped"`
}
