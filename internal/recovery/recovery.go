package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/monshunter/xgoal/internal/store/sqlite"
)

type WorkerStore interface {
	RecoverableWorkers(context.Context) ([]sqlite.WorkerProcess, error)
	ResolveWorkerRecovery(context.Context, string, int64, sqlite.WorkerState, string, sqlite.EventInput) (sqlite.WorkerProcess, error)
}

type Inspector interface {
	Identity(int) (identity string, pgid int, err error)
	TerminateGroup(context.Context, int) error
}

type Manager struct {
	store     WorkerStore
	inspector Inspector
}

func New(store WorkerStore, inspector Inspector) (*Manager, error) {
	if store == nil || inspector == nil {
		return nil, errors.New("worker store and process inspector are required")
	}
	return &Manager{store: store, inspector: inspector}, nil
}

// Recover resolves every worker whose ownership was persisted by the previous daemon.
func (manager *Manager) Recover(ctx context.Context) error {
	workers, err := manager.store.RecoverableWorkers(ctx)
	if err != nil {
		return err
	}
	var result error
	for _, worker := range workers {
		identity, pgid, inspectErr := manager.inspector.Identity(worker.PID)
		state := sqlite.WorkerLost
		reason := "worker process no longer exists"
		if inspectErr == nil && identity == worker.StartIdentity && pgid == worker.PGID {
			if terminateErr := manager.inspector.TerminateGroup(ctx, pgid); terminateErr != nil {
				result = errors.Join(result, fmt.Errorf("terminate worker %s: %w", worker.AttemptID, terminateErr))
				continue
			}
			state = sqlite.WorkerTerminated
			reason = "matching persisted worker terminated during daemon recovery"
		} else if inspectErr == nil {
			reason = "pid identity or process group changed; process was not signaled"
		}
		if _, err := manager.store.ResolveWorkerRecovery(ctx, worker.AttemptID, worker.Version, state, reason, sqlite.EventInput{Type: "WorkerRecovered", ActorType: "daemon", Payload: map[string]any{"state": state, "reason": reason}}); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

type OSInspector struct {
	GracePeriod time.Duration
}

func (inspector OSInspector) Identity(pid int) (string, int, error) {
	if pid <= 0 {
		return "", 0, errors.New("invalid pid")
	}
	command := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	output, err := command.Output()
	if err != nil {
		return "", 0, os.ErrProcessDone
	}
	identity := strings.Join(strings.Fields(string(output)), " ")
	if identity == "" {
		return "", 0, os.ErrProcessDone
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return "", 0, err
	}
	return identity, pgid, nil
}

func (inspector OSInspector) TerminateGroup(ctx context.Context, pgid int) error {
	if pgid <= 0 {
		return errors.New("invalid process group")
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	grace := inspector.GracePeriod
	if grace <= 0 {
		grace = 2 * time.Second
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
			return nil
		case <-ticker.C:
		}
	}
}
