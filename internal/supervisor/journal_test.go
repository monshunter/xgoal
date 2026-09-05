package supervisor_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/supervisor"
)

type processJournal struct {
	mu        sync.Mutex
	calls     []string
	identity  supervisor.ProcessIdentity
	register  func() error
	finishErr error
}

func TestUnconfirmedServiceCleanupRetainsOwnershipAndError(t *testing.T) {
	for _, exits := range []bool{false, true} {
		t.Run(map[bool]string{false: "probe_timeout", true: "early_exit"}[exits], func(t *testing.T) {
			journal := &processJournal{finishErr: errors.New("journal unavailable")}
			ctx := supervisor.WithOwner(context.Background(), journal, supervisor.Owner{Kind: "probe", ID: "failed_service", Generation: 1})
			group := supervisor.NewGroup()
			command := "sleep 10"
			if exits {
				command = "exit 0"
			}
			_, err := group.Start(ctx, supervisor.ServiceSpec{ID: "service", Command: supervisor.Command{Argv: []string{"/bin/sh", "-c", command}, Dir: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin"}, GracePeriod: 30 * time.Millisecond}, Probe: func(context.Context) error { return errors.New("unhealthy") }, ProbeTimeout: 200 * time.Millisecond, ProbeInterval: 10 * time.Millisecond})
			if !errors.Is(err, supervisor.ErrProcessUnconfirmed) || len(group.PIDs()) != 1 || !errors.Is(group.StopAll(), supervisor.ErrProcessUnconfirmed) {
				t.Fatalf("ownership lost: pids=%v error=%v", group.PIDs(), err)
			}
			if alive, err := supervisor.ProcessGroupAlive(journal.identity.PGID); err != nil || alive {
				t.Fatalf("group survived=%t %v", alive, err)
			}
		})
	}
}

func TestBarrierResolvesRelativeExecutableInWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\nprintf relative\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	execution, err := supervisor.Run(context.Background(), supervisor.Command{Argv: []string{"./run.sh"}, Dir: dir, Env: []string{"PATH=/usr/bin:/bin"}, Stdout: &stdout, GracePeriod: 30 * time.Millisecond})
	if err == nil || execution.ExitCode != 7 || stdout.String() != "relative" {
		t.Fatalf("execution=%+v output=%q err=%v", execution, stdout.String(), err)
	}
}

func TestBarrierReclaimsTermIgnoringDescendantHoldingOutputAfterParentExit(t *testing.T) {
	var stdout bytes.Buffer
	started := time.Now()
	journal := &processJournal{}
	ctx := supervisor.WithOwner(context.Background(), journal, supervisor.Owner{Kind: "probe", ID: "orphan_probe", Generation: 1})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	execution, err := supervisor.Run(ctx, supervisor.Command{Argv: []string{"/bin/sh", "-c", "(trap '' TERM; while :; do sleep 1; done) & printf parent-exited; exit 7"}, Dir: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin"}, Stdout: &stdout, GracePeriod: 50 * time.Millisecond})
	if err == nil || execution.ExitCode != 7 || stdout.String() != "parent-exited" || time.Since(started) > 4*time.Second {
		t.Fatalf("execution=%+v output=%q err=%v elapsed=%s", execution, stdout.String(), err, time.Since(started))
	}
	if alive, err := supervisor.ProcessGroupAlive(journal.identity.PGID); err != nil || alive {
		t.Fatalf("unreclaimed descendant: %t %v", alive, err)
	}
	if !reflect.DeepEqual(journal.calls, []string{"intent", "registered", "EXITED"}) {
		t.Fatalf("journal=%v", journal.calls)
	}
}

func (j *processJournal) BeginProcess(_ context.Context, intent supervisor.ProcessIntent) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.calls = append(j.calls, "intent")
	return intent.Owner.Validate()
}
func (j *processJournal) RegisterProcess(_ context.Context, _ string, identity supervisor.ProcessIdentity) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.calls = append(j.calls, "registered")
	j.identity = identity
	if j.register != nil {
		return j.register()
	}
	return nil
}
func (j *processJournal) FinishProcess(_ context.Context, _ string, state supervisor.ProcessState, _ string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.calls = append(j.calls, string(state))
	return j.finishErr
}

func TestDurableStartupBarrierPreventsUnregisteredExecution(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "registered", true: "registration_failed"}[reject], func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "executed")
			journal := &processJournal{register: func() error {
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					return errors.New("target ran before registration")
				}
				if reject {
					return errors.New("journal unavailable")
				}
				return nil
			}}
			ctx := supervisor.WithOwner(context.Background(), journal, supervisor.Owner{Kind: "probe", ID: "probe_test", Generation: 1})
			running, err := supervisor.StartContext(ctx, supervisor.Command{Argv: []string{"/bin/sh", "-c", "printf ran > executed"}, Dir: dir, Env: []string{"PATH=/usr/bin:/bin"}, GracePeriod: 30 * time.Millisecond})
			if reject {
				if err == nil {
					_ = running.Terminate()
					t.Fatal("registration failure permitted target start")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("unregistered target ran")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := running.Wait(context.Background()); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(marker); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(journal.calls, []string{"intent", "registered", "EXITED"}) {
					t.Fatalf("journal order=%v", journal.calls)
				}
			}
			if journal.identity.PID <= 0 || journal.identity.PID != journal.identity.PGID || journal.identity.StartID == "" {
				t.Fatalf("identity=%+v", journal.identity)
			}
			if alive, err := supervisor.ProcessGroupAlive(journal.identity.PGID); err != nil || alive {
				t.Fatalf("group remained alive=%t err=%v", alive, err)
			}
		})
	}
}
