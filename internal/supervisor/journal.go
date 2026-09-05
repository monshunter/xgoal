package supervisor

import (
	"context"
	"errors"
)

var ErrProcessUnconfirmed = errors.New("process group termination is unconfirmed")

type Owner struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	GoalID     string `json:"goal_id"`
	Generation int64  `json:"generation"`
}

type ProcessIntent struct {
	ID    string
	Owner Owner
}

type ProcessIdentity struct {
	PID     int
	PGID    int
	StartID string
}

type ProcessState string

const (
	ProcessIntentState ProcessState = "INTENT"
	ProcessRegistered  ProcessState = "REGISTERED"
	ProcessExited      ProcessState = "EXITED"
	ProcessTerminated  ProcessState = "TERMINATED"
	ProcessUnknown     ProcessState = "UNKNOWN"
)

type ProcessRecord struct {
	ProcessIntent
	Identity ProcessIdentity
	State    ProcessState
	Reason   string
}

type Journal interface {
	BeginProcess(context.Context, ProcessIntent) error
	RegisterProcess(context.Context, string, ProcessIdentity) error
	FinishProcess(context.Context, string, ProcessState, string) error
}

type ownerContext struct {
	journal Journal
	owner   Owner
}

type ownerKey struct{}

// WithOwner binds leaf processes to a durable business owner. It records no
// command arguments, prompts, credentials or environment values.
func WithOwner(ctx context.Context, journal Journal, owner Owner) context.Context {
	return context.WithValue(ctx, ownerKey{}, ownerContext{journal, owner})
}

func (owner Owner) Validate() error {
	if (owner.Kind != "planning" && owner.Kind != "attempt" && owner.Kind != "probe") || !validID(owner.ID) || owner.Generation <= 0 || (owner.Kind != "probe" && !validID(owner.GoalID)) {
		return errors.New("invalid process owner")
	}
	return nil
}
