package adapter

import (
	"context"
	"errors"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
)

var (
	ErrInvalidOutput   = errors.New("agent adapter returned invalid output")
	ErrUnavailable     = errors.New("agent adapter is unavailable")
	ErrSessionMismatch = errors.New("agent session binding mismatch")
)

type ProbeMode string

const (
	ProbePassive        ProbeMode = "PASSIVE"
	ProbeActiveContract ProbeMode = "ACTIVE_CONTRACT"
)

type ProbeSpec struct {
	Mode              ProbeMode
	ProfileID         string
	ProviderTransport bool
	Timeout           time.Duration
}

type Capabilities struct {
	Version           string    `json:"version"`
	StructuredOutput  bool      `json:"structured_output"`
	StreamingEvents   bool      `json:"streaming_events"`
	ResumeSession     bool      `json:"resume_session"`
	SandboxModes      []string  `json:"sandbox_modes"`
	ToolAllowlist     bool      `json:"tool_allowlist"`
	ApprovalModes     []string  `json:"approval_modes"`
	ProbeMode         ProbeMode `json:"probe_mode"`
	ProviderTransport string    `json:"provider_transport"`
	CredentialStatus  string    `json:"credential_status"`
	ProbeRef          string    `json:"probe_ref,omitempty"`
}

type SessionPolicy string

const (
	SessionFresh            SessionPolicy = "fresh"
	SessionResumeCompatible SessionPolicy = "resume-compatible"
)

type Invocation struct {
	InvocationID     string
	AttemptID        string
	WorkItemID       string
	ProfileID        string
	GoalRevisionHash string
	PlanRevisionHash string
	BaseTree         string
	PacketHash       string
	Role             domain.Role
	WorkDir          string
	PacketPath       string
	Prompt           string
	OutputSchema     []byte
	Environment      map[string]string
	SandboxPolicy    string
	PermissionMode   string
	ToolPolicy       []string
	Timeout          time.Duration
	MaxOutputBytes   int64
	SessionPolicy    SessionPolicy
}

type Handle struct {
	ID  string
	PID int
}

type EventSink func(protocol.AgentEvent) error

type Adapter interface {
	ID() string
	Probe(context.Context, ProbeSpec) (Capabilities, error)
	Start(context.Context, Invocation, EventSink) (Handle, error)
	Resume(context.Context, Invocation, string, EventSink) (Handle, error)
	Cancel(context.Context, Handle) error
	Wait(context.Context, Handle) (protocol.AgentResult, error)
}
