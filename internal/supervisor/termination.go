package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

func ProcessGroupAlive(pgid int) (bool, error) {
	if pgid <= 0 {
		return false, errors.New("invalid process group")
	}
	members, err := groupMembers(pgid, 0)
	return len(members) > 0, err
}

// TerminateOwnedGroup signals only a matching group leader, then confirms that
// no executing members remain. Zombies cannot execute and may await OS reaping.
func TerminateOwnedGroup(ctx context.Context, expected ProcessIdentity, grace time.Duration) error {
	if expected.PID <= 0 || expected.PID != expected.PGID || expected.PID == os.Getpid() || expected.StartID == "" || grace <= 0 {
		return fmt.Errorf("%w: invalid owned group", ErrProcessUnconfirmed)
	}
	current, err := InspectProcess(expected.PID)
	if errors.Is(err, os.ErrProcessDone) {
		alive, groupErr := ProcessGroupAlive(expected.PGID)
		if groupErr == nil && !alive {
			return nil
		}
		return fmt.Errorf("%w: leader absent with unproven descendants: %v", ErrProcessUnconfirmed, groupErr)
	}
	if err != nil || current != expected {
		return fmt.Errorf("%w: process identity mismatch: %v", ErrProcessUnconfirmed, err)
	}
	if err := terminateProcessGroup(expected.PGID); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return errors.Join(ErrProcessUnconfirmed, err)
	}
	deadline := time.Now().Add(grace)
	killed := false
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive, err := ProcessGroupAlive(expected.PGID)
		if err != nil {
			return errors.Join(ErrProcessUnconfirmed, err)
		}
		if !alive {
			return nil
		}
		if !time.Now().Before(deadline) {
			if killed {
				return fmt.Errorf("%w: members remain after SIGKILL", ErrProcessUnconfirmed)
			}
			identity, err := InspectProcess(expected.PID)
			if err != nil || identity != expected {
				return fmt.Errorf("%w: group leader identity changed before SIGKILL", ErrProcessUnconfirmed)
			}
			if err := syscall.Kill(-expected.PGID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return errors.Join(ErrProcessUnconfirmed, err)
			}
			killed = true
			deadline = time.Now().Add(2 * time.Second)
		}
		select {
		case <-ctx.Done():
			return errors.Join(ErrProcessUnconfirmed, ctx.Err())
		case <-ticker.C:
		}
	}
}
