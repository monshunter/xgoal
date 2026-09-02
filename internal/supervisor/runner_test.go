package supervisor_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/supervisor"
)

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
	switch os.Getenv("XGOAL_SUPERVISOR_MODE") {
	case "exit":
		_, _ = os.Stdout.WriteString("stdout\n")
		_, _ = os.Stderr.WriteString("stderr\n")
	case "service":
		if err := os.WriteFile(os.Getenv("XGOAL_SUPERVISOR_READY"), []byte("ready"), 0o600); err != nil {
			os.Exit(3)
		}
		time.Sleep(time.Hour)
	case "sleep":
		time.Sleep(time.Hour)
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
