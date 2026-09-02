package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Command struct {
	Argv        []string
	Dir         string
	Env         []string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	GracePeriod time.Duration
}

type Execution struct {
	PID        int
	ExitCode   int
	StartedAt  time.Time
	FinishedAt time.Time
	Terminated bool
}

type processOutcome struct {
	execution Execution
	err       error
}

type Running struct {
	command *exec.Cmd
	done    chan struct{}
	grace   time.Duration

	mu      sync.RWMutex
	outcome processOutcome
}

func Start(spec Command) (*Running, error) {
	if err := validateCommand(spec); err != nil {
		return nil, err
	}
	command := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	command.Dir = spec.Dir
	command.Env = append([]string(nil), spec.Env...)
	command.Stdin = spec.Stdin
	command.Stdout = spec.Stdout
	command.Stderr = spec.Stderr
	configureProcessGroup(command)
	startedAt := time.Now().UTC()
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", spec.Argv[0], err)
	}
	running := &Running{command: command, done: make(chan struct{}), grace: spec.GracePeriod}
	go func() {
		err := command.Wait()
		exitCode := 0
		if err != nil {
			var exitError *exec.ExitError
			if errors.As(err, &exitError) {
				exitCode = exitError.ExitCode()
			} else {
				exitCode = -1
			}
		}
		running.mu.Lock()
		running.outcome = processOutcome{execution: Execution{
			PID: command.Process.Pid, ExitCode: exitCode, StartedAt: startedAt, FinishedAt: time.Now().UTC(),
		}, err: err}
		running.mu.Unlock()
		close(running.done)
	}()
	return running, nil
}

func Run(ctx context.Context, spec Command) (Execution, error) {
	running, err := Start(spec)
	if err != nil {
		return Execution{}, err
	}
	execution, err := running.Wait(ctx)
	if err == nil || !errors.Is(err, ctx.Err()) {
		return execution, err
	}
	if terminateErr := running.Terminate(); terminateErr != nil {
		return execution, errors.Join(err, terminateErr)
	}
	execution, waitErr := running.Wait(context.Background())
	execution.Terminated = true
	if waitErr != nil {
		var exitError *exec.ExitError
		if !errors.As(waitErr, &exitError) {
			return execution, errors.Join(err, waitErr)
		}
	}
	return execution, err
}

func (running *Running) PID() int { return running.command.Process.Pid }

func (running *Running) Wait(ctx context.Context) (Execution, error) {
	select {
	case <-ctx.Done():
		return Execution{PID: running.PID()}, ctx.Err()
	case <-running.done:
		running.mu.RLock()
		defer running.mu.RUnlock()
		return running.outcome.execution, running.outcome.err
	}
}

func (running *Running) Terminate() error {
	select {
	case <-running.done:
		return nil
	default:
	}
	if err := terminateProcessGroup(running.PID()); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	timer := time.NewTimer(running.grace)
	defer timer.Stop()
	select {
	case <-running.done:
		return nil
	case <-timer.C:
	}
	if err := killProcessGroup(running.PID()); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	<-running.done
	return nil
}

type Probe func(context.Context) error

type ServiceSpec struct {
	ID            string
	Command       Command
	Probe         Probe
	ProbeTimeout  time.Duration
	ProbeInterval time.Duration
}

type Group struct {
	mu       sync.Mutex
	services map[string]*Running
}

func NewGroup() *Group { return &Group{services: make(map[string]*Running)} }

func (group *Group) Start(ctx context.Context, spec ServiceSpec) (int, error) {
	if !validID(spec.ID) || spec.Probe == nil || spec.ProbeTimeout <= 0 || spec.ProbeInterval <= 0 {
		return 0, errors.New("invalid service specification")
	}
	group.mu.Lock()
	if _, exists := group.services[spec.ID]; exists {
		group.mu.Unlock()
		return 0, fmt.Errorf("service %q already exists", spec.ID)
	}
	process, err := Start(spec.Command)
	if err != nil {
		group.mu.Unlock()
		return 0, err
	}
	group.services[spec.ID] = process
	group.mu.Unlock()

	probeContext, cancel := context.WithTimeout(ctx, spec.ProbeTimeout)
	defer cancel()
	ticker := time.NewTicker(spec.ProbeInterval)
	defer ticker.Stop()
	var lastProbeError error
	for {
		if err := spec.Probe(probeContext); err == nil {
			return process.PID(), nil
		} else {
			lastProbeError = err
		}
		select {
		case <-process.done:
			execution, waitErr := process.Wait(context.Background())
			group.remove(spec.ID, process)
			if waitErr != nil {
				return 0, fmt.Errorf("service %q exited before becoming healthy: %w", spec.ID, waitErr)
			}
			return 0, fmt.Errorf("service %q exited %d before becoming healthy", spec.ID, execution.ExitCode)
		case <-probeContext.Done():
			_ = process.Terminate()
			group.remove(spec.ID, process)
			return 0, fmt.Errorf("service %q health probe failed: %w", spec.ID, errors.Join(probeContext.Err(), lastProbeError))
		case <-ticker.C:
		}
	}
}

func (group *Group) Stop(id string) error {
	group.mu.Lock()
	process, exists := group.services[id]
	group.mu.Unlock()
	if !exists {
		return fmt.Errorf("service %q is not running", id)
	}
	if err := process.Terminate(); err != nil {
		return err
	}
	group.remove(id, process)
	return nil
}

func (group *Group) StopAll() error {
	group.mu.Lock()
	ids := make([]string, 0, len(group.services))
	for id := range group.services {
		ids = append(ids, id)
	}
	group.mu.Unlock()
	sort.Strings(ids)
	var result error
	for _, id := range ids {
		if err := group.Stop(id); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (group *Group) PIDs() map[string]int {
	group.mu.Lock()
	defer group.mu.Unlock()
	result := make(map[string]int, len(group.services))
	for id, process := range group.services {
		result[id] = process.PID()
	}
	return result
}

func (group *Group) remove(id string, process *Running) {
	group.mu.Lock()
	defer group.mu.Unlock()
	if group.services[id] == process {
		delete(group.services, id)
	}
}

func validateCommand(spec Command) error {
	if len(spec.Argv) == 0 || spec.Env == nil || spec.GracePeriod <= 0 || !filepath.IsAbs(spec.Dir) || filepath.Clean(spec.Dir) != spec.Dir {
		return errors.New("command requires argv, explicit environment, positive grace period, and clean absolute cwd")
	}
	info, err := os.Stat(spec.Dir)
	if err != nil || !info.IsDir() {
		return errors.New("command cwd is not an accessible directory")
	}
	for _, argument := range spec.Argv {
		if argument == "" || !utf8.ValidString(argument) || strings.ContainsRune(argument, '\x00') {
			return errors.New("command argv contains an invalid argument")
		}
	}
	for _, value := range spec.Env {
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || !strings.Contains(value, "=") {
			return errors.New("command environment contains an invalid entry")
		}
	}
	return nil
}

func validID(value string) bool {
	return value != "" && value != "." && value != ".." && utf8.ValidString(value) && !strings.ContainsAny(value, "/\\\r\n\x00")
}
