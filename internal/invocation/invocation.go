// Package invocation describes observations of Provider calls. It never owns
// execution, process recovery, Gate decisions or completion authority.
package invocation

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/config"
)

const Version = "xgoal.invocation/v1alpha1"

type Input struct {
	ConfigHash       string                 `json:"config_hash,omitempty"`
	ID               string                 `json:"id"`
	GoalID           string                 `json:"goal_id"`
	OwnerKind        string                 `json:"owner_kind"`
	OwnerID          string                 `json:"owner_id"`
	Generation       int64                  `json:"generation"`
	Role             string                 `json:"role"`
	ProfileID        string                 `json:"profile_id"`
	Provider         string                 `json:"provider"`
	RequestHash      string                 `json:"request_hash,omitempty"`
	GoalRevisionHash string                 `json:"goal_revision_hash,omitempty"`
	PlanRevisionHash string                 `json:"plan_revision_hash,omitempty"`
	InputTree        string                 `json:"input_tree"`
	PacketPath       string                 `json:"packet_path"`
	PacketHash       string                 `json:"packet_hash"`
	PacketSHA256     string                 `json:"packet_sha256"`
	ProviderDir      string                 `json:"provider_dir"`
	Prompt           string                 `json:"prompt"`
	SchemaSHA256     string                 `json:"schema_sha256"`
	DelegationHash   string                 `json:"delegation_hash"`
	ExecutionConfig  config.ExecutionConfig `json:"execution_config"`
}

type Observation struct {
	Unavailable   string     `json:"log_unavailable,omitempty"`
	StderrCursor  int64      `json:"stderr_durable_cursor"`
	StderrBytes   int64      `json:"stderr_durable_bytes"`
	Status        string     `json:"status"`
	SessionID     string     `json:"session_id,omitempty"`
	ObservedModel string     `json:"observed_model"`
	ResultStatus  string     `json:"result_status,omitempty"`
	Cursor        int64      `json:"durable_cursor"`
	Bytes         int64      `json:"durable_bytes"`
	LastOutputAt  *time.Time `json:"last_output_at,omitempty"`
	LogError      string     `json:"log_error,omitempty"`
	Failure       string     `json:"failure,omitempty"`
	Truncated     bool       `json:"truncated"`
}

type Record struct {
	ProtocolVersion string      `json:"protocol_version"`
	Input           Input       `json:"input"`
	InputHash       string      `json:"input_hash"`
	Observation     Observation `json:"observation"`
	Version         int64       `json:"version"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

func Component(s string) bool {
	return s != "" && len(s) <= 160 && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\r\n\x00")
}
func Relative(s string) bool {
	return s != "." && filepath.IsLocal(s) && filepath.Clean(s) == s && !strings.ContainsAny(s, "\\\r\n\x00")
}
func Directory(provider, role, id string) (string, error) {
	if !Component(id) || (provider != "codex-cli" && provider != "claude-cli") {
		return "", errors.New("invalid invocation identity")
	}
	kind := map[string]string{"planner": "plans", "implementer": "invocations", "reviewer": "reviews", "acceptance": "acceptances"}[role]
	if kind == "" {
		return "", errors.New("invalid invocation role")
	}
	return filepath.Join("adapters", strings.TrimSuffix(provider, "-cli"), kind, id), nil
}
func (in Input) Validate() error {
	dir, err := Directory(in.Provider, in.Role, in.ID)
	if err != nil || dir != in.ProviderDir || !Component(in.GoalID) || !Component(in.OwnerID) || in.Generation <= 0 || !Relative(in.PacketPath) || in.InputTree == "" || in.PacketHash == "" || len(in.PacketSHA256) != 64 || len(in.SchemaSHA256) != 64 || in.DelegationHash == "" || len(in.Prompt) > 1<<20 {
		return errors.New("invalid invocation input identity")
	}
	if in.OwnerKind != "planning" && in.OwnerKind != "attempt" && in.OwnerKind != "final" {
		return errors.New("invalid invocation owner")
	}
	if (in.Role == "planner" && (in.OwnerKind != "planning" || in.RequestHash == "")) || (in.Role != "planner" && in.GoalRevisionHash == "") {
		return errors.New("missing invocation revision or planning request")
	}
	if in.ExecutionConfig.ProfileID != in.ProfileID || in.ExecutionConfig.Provider != in.Provider || in.ExecutionConfig.Role != in.Role {
		return errors.New("invocation execution configuration mismatch")
	}
	return nil
}
