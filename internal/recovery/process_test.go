package recovery_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/recovery"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/supervisor"
)

func TestProcessRecoveryHelper(t *testing.T) {
	root := os.Getenv("XGOAL_PROCESS_RECOVERY_HELPER")
	if root == "" {
		return
	}
	s, err := sqlite.Open(context.Background(), filepath.Join(root, "state"), clock.Real{})
	if err != nil {
		os.Exit(20)
	}
	ctx := supervisor.WithOwner(context.Background(), s, supervisor.Owner{Kind: "probe", ID: "abandoned_probe", Generation: 1})
	running, err := supervisor.StartContext(ctx, supervisor.Command{Argv: []string{"/bin/sh", "-c", "trap '' TERM; printf ready > ready; while :; do sleep 1; done"}, Dir: root, Env: []string{"PATH=/usr/bin:/bin"}, GracePeriod: 100 * time.Millisecond})
	if err != nil {
		os.Exit(21)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = running.Terminate()
	os.Exit(22)
}

func TestRecoveryReclaimsRegisteredGroupAfterOwningProcessExits(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessRecoveryHelper$")
	command.Env = append(os.Environ(), "XGOAL_PROCESS_RECOVERY_HELPER="+root)
	command.WaitDelay = time.Second
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("owner helper: %s %v", output, err)
	}
	s, err := sqlite.Open(ctx, filepath.Join(root, "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	records, err := s.RecoverableProcesses(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("registered=%+v err=%v", records, err)
	}
	identity := records[0].Identity
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = supervisor.TerminateOwnedGroup(cleanup, identity, 100*time.Millisecond)
	})
	if alive, err := supervisor.ProcessGroupAlive(identity.PGID); err != nil || !alive {
		t.Fatalf("no orphan to recover: %t %v", alive, err)
	}
	manager := recovery.ProcessManager{Store: s, GracePeriod: 100 * time.Millisecond}
	if err := manager.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if alive, err := supervisor.ProcessGroupAlive(identity.PGID); err != nil || alive {
		t.Fatalf("recovery left execution: %t %v", alive, err)
	}
	if err := manager.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	records, err = s.RecoverableProcesses(ctx)
	if err != nil || len(records) != 0 {
		t.Fatalf("recovery not terminal: %+v %v", records, err)
	}
	if err := s.CheckExecutionAvailable(ctx); err != nil {
		t.Fatalf("recovered project remains blocked: %v", err)
	}
}

func TestUnknownProcessIdentityRemainsInspectableAndBlocksExecution(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	intent := supervisor.ProcessIntent{ID: "unknown", Owner: supervisor.Owner{Kind: "probe", ID: "foreign", Generation: 1}}
	if err := s.BeginProcess(ctx, intent); err != nil {
		t.Fatal(err)
	}
	// The current test process is deliberately a foreign identity. Recovery
	// must neither signal it nor turn the absence of proof into a free slot.
	if err := s.RegisterProcess(ctx, intent.ID, supervisor.ProcessIdentity{PID: os.Getpid(), PGID: os.Getpid(), StartID: "foreign-identity"}); err != nil {
		t.Fatal(err)
	}
	manager := recovery.ProcessManager{Store: s, GracePeriod: 20 * time.Millisecond}
	if err := manager.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	records, err := s.RecoverableProcesses(ctx)
	if err != nil || len(records) != 1 || records[0].State != supervisor.ProcessUnknown {
		t.Fatalf("unknown=%+v %v", records, err)
	}
	if err := s.CheckExecutionAvailable(ctx); !errors.Is(err, sqlite.ErrCheckoutBusy) {
		t.Fatalf("unknown execution not fenced: %v", err)
	}
	if blocker, err := s.ExecutionRecoveryBlocker(ctx); err != nil || blocker == "" {
		t.Fatalf("missing diagnostic: %s %v", blocker, err)
	}
}
