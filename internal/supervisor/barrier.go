package supervisor

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const barrierArgument = "__xgoal_process_barrier_v1"
const barrierEnvironment = "XGOAL_INTERNAL_PROCESS_BARRIER"

// The registered wrapper remains the group leader until the target and its
// descendants stop. This preserves a recoverable identity if the target exits
// before its children. The private pipe is never inherited by the target.
func init() {
	if len(os.Args) < 4 || os.Args[1] != barrierArgument || os.Getenv(barrierEnvironment) != "1" {
		return
	}
	grace, err := time.ParseDuration(os.Args[2])
	if err != nil || grace <= 0 {
		os.Exit(125)
	}
	gate := os.NewFile(3, "xgoal-start-barrier")
	if gate == nil {
		os.Exit(125)
	}
	var permission [1]byte
	_, err = io.ReadFull(gate, permission[:])
	_ = gate.Close()
	if err != nil || permission[0] != 1 {
		os.Exit(125)
	}
	// Catch, rather than ignore, TERM: exec resets caught signals for the
	// target, while the anchor survives long enough to retain group identity.
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM, syscall.SIGINT)
	command := exec.Command(os.Args[3], os.Args[4:]...)
	command.Env = withoutBarrierEnvironment(os.Environ())
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = command.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
			if exitCode < 0 {
				exitCode = 128
			}
		} else {
			exitCode = 127
		}
	}
	if err := stopDescendants(os.Getpid(), grace); err != nil {
		os.Exit(125)
	}
	os.Exit(exitCode)
}

func withoutBarrierEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if key != barrierEnvironment {
			result = append(result, entry)
		}
	}
	return result
}

func stopDescendants(pgid int, grace time.Duration) error {
	deadline := time.Now().Add(grace)
	killDeadline := deadline.Add(2 * time.Second)
	for {
		members, err := groupMembers(pgid, os.Getpid())
		if err != nil {
			return err
		}
		if len(members) == 0 {
			return nil
		}
		if time.Now().After(killDeadline) {
			return ErrProcessUnconfirmed
		}
		sig := syscall.SIGTERM
		if !time.Now().Before(deadline) {
			sig = syscall.SIGKILL
		}
		for _, member := range members {
			current, err := InspectProcess(member.PID)
			if errors.Is(err, os.ErrProcessDone) {
				continue
			}
			if err != nil || current != member {
				return ErrProcessUnconfirmed
			}
			if err := syscall.Kill(member.PID, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func wrapperCommand(spec Command, gate *os.File) (*exec.Cmd, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	target := spec.Argv[0]
	if strings.ContainsRune(target, filepath.Separator) {
		if !filepath.IsAbs(target) {
			target = filepath.Join(spec.Dir, target)
		}
	} else {
		target, err = exec.LookPath(target)
		if err != nil {
			return nil, err
		}
	}
	args := []string{barrierArgument, strconv.FormatInt(int64(spec.GracePeriod), 10) + "ns", target}
	args = append(args, spec.Argv[1:]...)
	command := exec.Command(binary, args...)
	command.Dir = spec.Dir
	command.Env = append(withoutBarrierEnvironment(spec.Env), barrierEnvironment+"=1")
	command.Stdin, command.Stdout, command.Stderr = spec.Stdin, spec.Stdout, spec.Stderr
	command.ExtraFiles = []*os.File{gate}
	command.WaitDelay = 250 * time.Millisecond
	configureProcessGroup(command)
	return command, nil
}
