package environment

import (
	"context"
	"io"
	"time"

	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/supervisor"
)

type Project struct {
	Root string
}

type Capabilities struct {
	Provider              string
	IsolationLevel        string
	FilesystemIsolation   bool
	NetworkIsolation      bool
	CredentialIsolation   string
	TrustedRepositoryOnly bool
}

type ToolProbe struct {
	Name     string
	Argv     []string
	Required bool
}

type Spec struct {
	ID                   string
	WorktreePath         string
	BaseCommit           string
	BaseTree             string
	ConfigHash           string
	GoalRevisionHash     string
	EnvironmentAllowlist []string
	Lockfiles            []string
	ToolProbes           []ToolProbe
	BootstrapHash        string
}

type Handle struct {
	ID       string
	Worktree string
	Root     string
	token    string
}

type ServiceSpec struct {
	ID                   string
	Argv                 []string
	CWD                  string
	EnvironmentAllowlist []string
	GracePeriod          time.Duration
	Probe                supervisor.Probe
	ProbeTimeout         time.Duration
	ProbeInterval        time.Duration
}

type CommandSpec struct {
	Argv                 []string
	CWD                  string
	EnvironmentAllowlist []string
	Stdin                io.Reader
	Stdout               io.Writer
	Stderr               io.Writer
	GracePeriod          time.Duration
}

type Provider interface {
	Probe(context.Context, Project) (Capabilities, error)
	Prepare(context.Context, Spec) (Handle, error)
	Snapshot(context.Context, Handle) (protocol.EnvironmentSnapshot, error)
	StartServices(context.Context, Handle, []ServiceSpec) error
	StopServices(context.Context, Handle) error
	Cleanup(context.Context, Handle) error
}
