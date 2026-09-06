package supervisor_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/supervisor"
)

type delayedOutput struct{ io.Writer }

func (w delayedOutput) Write(data []byte) (int, error) {
	time.Sleep(750 * time.Millisecond)
	return w.Writer.Write(data)
}

func TestSuccessfulExitWaitsForDurableOutputDrain(t *testing.T) {
	// A local immutable event write may fsync after the Provider has already
	// exited. Process exit alone must not truncate a healthy bounded sink.
	var stdout bytes.Buffer
	command := helperCommand(t.TempDir(), "exit", "", nil, nil)
	command.Stdout, command.Stderr = delayedOutput{&stdout}, io.Discard
	execution, err := supervisor.Run(context.Background(), command)
	if err != nil || execution.ExitCode != 0 || stdout.String() != "stdout\n" {
		t.Fatalf("drained execution=%+v output=%q err=%v", execution, stdout.String(), err)
	}
}

func TestRunCapturesExitAndTerminatesTimedOutProcessGroup(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	var stdout, stderr bytes.Buffer
	execution, err := supervisor.Run(context.Background(), helperCommand(directory, "exit", "", &stdout, &stderr))
	if err != nil || execution.ExitCode != 0 || execution.PID <= 0 || execution.StartedAt.IsZero() || execution.FinishedAt.Before(execution.StartedAt) {
		t.Fatalf("Run(exit) = %+v, %v", execution, err)
	}
	if stdout.String() != "stdout\n" || stderr.String() != "stderr\n" {
		t.Fatalf("helper output = %q / %q", stdout.String(), stderr.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	execution, err = supervisor.Run(ctx, helperCommand(directory, "sleep", "", nil, nil))
	if !errors.Is(err, context.DeadlineExceeded) || !execution.Terminated || execution.FinishedAt.IsZero() {
		t.Fatalf("Run(timeout) = %+v, %v", execution, err)
	}
}

func TestServiceGroupRequiresHealthAndStopsManagedService(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	readyPath := filepath.Join(directory, "ready")
	group := supervisor.NewGroup()
	pid, err := group.Start(context.Background(), supervisor.ServiceSpec{
		ID: "service_1", Command: helperCommand(directory, "service", readyPath, nil, nil),
		ProbeTimeout: time.Second, ProbeInterval: 10 * time.Millisecond,
		Probe: func(context.Context) error {
			_, err := os.Stat(readyPath)
			return err
		},
	})
	if err != nil || pid <= 0 || group.PIDs()["service_1"] != pid {
		t.Fatalf("Start() = %d, %v, pids=%v", pid, err, group.PIDs())
	}
	if err := group.StopAll(); err != nil {
		t.Fatalf("StopAll() error = %v", err)
	}
	if len(group.PIDs()) != 0 {
		t.Fatalf("services remain after stop: %v", group.PIDs())
	}

	failing := supervisor.NewGroup()
	_, err = failing.Start(context.Background(), supervisor.ServiceSpec{
		ID: "service_unhealthy", Command: helperCommand(directory, "sleep", "", nil, nil),
		ProbeTimeout: 80 * time.Millisecond, ProbeInterval: 10 * time.Millisecond,
		Probe: func(context.Context) error { return errors.New("not ready") },
	})
	if err == nil || len(failing.PIDs()) != 0 {
		t.Fatalf("unhealthy Start() error = %v, pids=%v", err, failing.PIDs())
	}
}

func TestSupervisorProcessHelper(t *testing.T) {
	if os.Getenv("XGOAL_SUPERVISOR_HELPER") != "1" {
		return
	}
	// Cancellation tests normally stop these children within seconds. A broken
	// parent must not leave an hour-long helper behind or turn timeout into PASS.
	watchdog := time.AfterFunc(30*time.Second, func() { os.Exit(124) })
	defer watchdog.Stop()
	switch os.Getenv("XGOAL_SUPERVISOR_MODE") {
	case "exit":
		_, _ = os.Stdout.WriteString("stdout\n")
		_, _ = os.Stderr.WriteString("stderr\n")
	case "service":
		if err := os.WriteFile(os.Getenv("XGOAL_SUPERVISOR_READY"), []byte("ready"), 0o600); err != nil {
			os.Exit(3)
		}
		select {}
	case "ordered-service":
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer cancel()
		if err := os.WriteFile(os.Getenv("XGOAL_SUPERVISOR_READY"), []byte("ready"), 0600); err != nil {
			os.Exit(6)
		}
		<-ctx.Done()
		f, err := os.OpenFile(os.Getenv("XGOAL_STOP_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(7)
		}
		_, _ = f.WriteString(os.Getenv("XGOAL_SERVICE_ID") + "\n")
		_ = f.Close()
	case "sleep":
		select {}
	default:
		os.Exit(4)
	}
	os.Exit(0)
}

func helperCommand(directory, mode, readyPath string, stdout, stderr *bytes.Buffer) supervisor.Command {
	command := supervisor.Command{
		Argv: []string{os.Args[0], "-test.run=^TestSupervisorProcessHelper$"}, Dir: directory,
		Env: []string{
			"XGOAL_SUPERVISOR_HELPER=1",
			"XGOAL_SUPERVISOR_MODE=" + mode,
			"XGOAL_SUPERVISOR_READY=" + readyPath,
		},
		GracePeriod: 50 * time.Millisecond,
	}
	if stdout != nil {
		command.Stdout = stdout
	}
	if stderr != nil {
		command.Stderr = stderr
	}
	return command
}

func TestServiceGroupStopsInReverseStartOrder(t *testing.T) {
	root := t.TempDir()
	group := supervisor.NewGroup()
	defer group.StopAll()
	for _, id := range []string{"a-database", "z-application"} {
		ready := filepath.Join(root, id+".ready")
		command := helperCommand(root, "ordered-service", ready, nil, nil)
		command.GracePeriod = 3 * time.Second
		command.Env = append(command.Env, "XGOAL_STOP_LOG="+filepath.Join(root, "stopped"), "XGOAL_SERVICE_ID="+id)
		_, err := group.Start(context.Background(), supervisor.ServiceSpec{ID: id, Command: command, ProbeTimeout: 5 * time.Second, ProbeInterval: 20 * time.Millisecond, Probe: func(context.Context) error { _, err := os.Stat(ready); return err }})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := group.StopAll(); err != nil {
		t.Fatal(err)
	}
	stopped, err := os.ReadFile(filepath.Join(root, "stopped"))
	if err != nil || string(stopped) != "z-application\na-database\n" {
		t.Fatalf("wrong stop order: %q %v", stopped, err)
	}
}

func TestServiceCannotBecomeReadyAfterItsOwnedProcessExited(t *testing.T) {
	group := supervisor.NewGroup()
	defer group.StopAll()
	_, err := group.Start(context.Background(), supervisor.ServiceSpec{ID: "exited", Command: helperCommand(t.TempDir(), "exit", "", nil, nil), ProbeTimeout: 3 * time.Second, ProbeInterval: 10 * time.Millisecond, Probe: func(ctx context.Context) error {
		pid := group.PIDs()["exited"]
		for {
			alive, err := supervisor.ProcessGroupAlive(pid)
			if err != nil {
				return err
			}
			if !alive {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
	}})
	if err == nil {
		t.Fatal("readiness from an old endpoint accepted after owned service exited")
	}
}
