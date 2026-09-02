package adapter

import (
	"context"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
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
	Budget            ProbeBudget
	Timeout           time.Duration
}

type ProbeBudget struct {
	MaxWallTime   time.Duration
	MaxTokens     int64
	MaxCostMicros int64
}

type Capabilities struct {
	Version           string
	StructuredOutput  bool
	StreamingEvents   bool
	ResumeSession     bool
	UsageReporting    bool
	CostReporting     bool
	SandboxModes      []string
	ToolAllowlist     bool
	ApprovalModes     []string
	ProbeMode         ProbeMode
	ProviderTransport string
	CredentialStatus  string
}

type Invocation struct {
	InvocationID   string
	AttemptID      string
	Role           domain.Role
	WorkDir        string
	PacketPath     string
	Prompt         string
	OutputSchema   []byte
	Environment    map[string]string
	SandboxPolicy  string
	ToolPolicy     []string
	Timeout        time.Duration
	MaxOutputBytes int64
	SessionPolicy  string
}

type Handle struct {
	ID string
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
