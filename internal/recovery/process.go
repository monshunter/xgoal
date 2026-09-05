package recovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/monshunter/xgoal/internal/supervisor"
)

type ProcessStore interface {
	RecoverableProcesses(context.Context) ([]supervisor.ProcessRecord, error)
	FinishProcess(context.Context, string, supervisor.ProcessState, string) error
}

// ProcessManager recovers all roles before another daemon starts execution.
// Unverifiable processes remain explicit UNKNOWN blockers; API inspection and
// cancellation remain available, and no identity is inferred from a PID file.
type ProcessManager struct {
	Store       ProcessStore
	GracePeriod time.Duration
}

func (manager ProcessManager) Recover(ctx context.Context) error {
	if manager.Store == nil {
		return errors.New("process recovery requires a Store")
	}
	records, err := manager.Store.RecoverableProcesses(ctx)
	if err != nil {
		return err
	}
	grace := manager.GracePeriod
	if grace <= 0 {
		grace = 2 * time.Second
	}
	for _, record := range records {
		state, reason := supervisor.ProcessTerminated, "unreleased startup intent cannot execute after the daemon pipe closes"
		if record.Identity.PID > 0 {
			recoveryContext, cancel := context.WithTimeout(ctx, grace+3*time.Second)
			err := supervisor.TerminateOwnedGroup(recoveryContext, record.Identity, grace)
			cancel()
			reason = "persisted process group is confirmed stopped during daemon recovery"
			if err != nil {
				state, reason = supervisor.ProcessUnknown, "process identity or group exit remains unconfirmed; no new execution is permitted"
			}
		} else if record.State != supervisor.ProcessIntentState {
			state, reason = supervisor.ProcessUnknown, "non-intent process record has no verifiable process identity"
		}
		if err := manager.Store.FinishProcess(ctx, record.ID, state, reason); err != nil {
			return fmt.Errorf("recover process %s: %w", record.ID, err)
		}
	}
	return nil
}
