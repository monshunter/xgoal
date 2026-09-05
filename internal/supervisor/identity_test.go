package supervisor_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/supervisor"
)

func TestProcessIdentityIsStableAndRejectsForeignIdentity(t *testing.T) {
	first, err := supervisor.InspectProcess(os.Getpid())
	if err != nil || first.PID != os.Getpid() || first.PGID <= 0 || first.StartID == "" {
		t.Fatalf("identity=%+v %v", first, err)
	}
	second, err := supervisor.InspectProcess(os.Getpid())
	if err != nil || second != first {
		t.Fatalf("identity changed=%+v %v", second, err)
	}
	first.StartID = "different-process"
	err = supervisor.TerminateOwnedGroup(context.Background(), first, 20*time.Millisecond)
	if !errors.Is(err, supervisor.ErrProcessUnconfirmed) {
		t.Fatalf("foreign identity was accepted: %v", err)
	}
	child, err := supervisor.Start(supervisor.Command{Argv: []string{"/bin/sh", "-c", "sleep 10"}, Dir: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin"}, GracePeriod: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Terminate()
	identity, err := supervisor.InspectProcess(child.PID())
	if err != nil {
		t.Fatal(err)
	}
	foreign := identity
	foreign.StartID = "reused-pid-with-different-start"
	if err := supervisor.TerminateOwnedGroup(context.Background(), foreign, 20*time.Millisecond); !errors.Is(err, supervisor.ErrProcessUnconfirmed) {
		t.Fatalf("mismatch accepted: %v", err)
	}
	if after, err := supervisor.InspectProcess(child.PID()); err != nil || after != identity {
		t.Fatalf("foreign process was signaled: %+v %v", after, err)
	}
}
