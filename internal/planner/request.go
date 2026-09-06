package planner

import (
	"errors"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/protocol"
)

const RequestVersion = "xgoal.planning-request/v1"

// Request is the immutable input of one durable planning generation.
type Request struct {
	// Nil marks legacy requests; a non-nil empty list preserves an explicit absence of CLI inputs.
	ExplicitAcceptanceFiles   *[]string                      `json:"explicit_acceptance_files,omitempty"`
	ApprovedValidationHash    string                         `json:"approved_validation_hash,omitempty"`
	AcceptanceFiles           []string                       `json:"acceptance_files,omitempty"`
	GeneratedValidationPolicy string                         `json:"generated_validation_policy,omitempty"`
	Prior                     *PriorContext                  `json:"prior,omitempty"`
	ValidationCapabilities    *config.ValidationCapabilities `json:"validation_capabilities,omitempty"`
	ProtocolVersion           string                         `json:"protocol_version"`
	GoalID                    string                         `json:"goal_id"`
	RawGoal                   string                         `json:"raw_goal"`
	Mode                      string                         `json:"mode"`
	CreatedBy                 string                         `json:"created_by"`
	ConfigHash                string                         `json:"config_hash"`
	ProfileID                 string                         `json:"profile_id"`
	Generation                int64                          `json:"generation"`
	TrustedValidatorIDs       []string                       `json:"trusted_validator_ids"`
	Proposal                  *Proposal                      `json:"proposal,omitempty"`
	BlockedReason             string                         `json:"blocked_reason,omitempty"`
}

// PriorContext carries one failed generation and its scoped answer. It is input,
// not permission to change the frozen project configuration or validator trust.
type PriorContext struct {
	EffectID    string                  `json:"effect_id"`
	RequestHash string                  `json:"request_hash"`
	Generation  int64                   `json:"generation"`
	Observation Observation             `json:"observation"`
	Decision    protocol.PacketDecision `json:"decision"`
}

func (p PriorContext) Validate() error {
	if !validComponent(p.EffectID) || !validHex(p.RequestHash, 64) || p.Generation < 1 || !validComponent(p.Observation.InvocationID) || (!validHex(p.Observation.InputTree, 40) && !validHex(p.Observation.InputTree, 64)) || p.Observation.CheckoutIdentity.Validate() != nil || !p.Observation.ExecutionStopped || p.Observation.FailureCode == "" || !validComponent(p.Decision.GateID) || p.Decision.GateVersion < 1 || p.Decision.Answer == "" {
		return errors.New("invalid prior planning context")
	}
	return nil
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
