package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	command      *exec.Cmd
	done         chan struct{}
	grace        time.Duration
	identity     ProcessIdentity
	journal      Journal
	invocationID string
	termMu       sync.Mutex
	terminated   bool

	mu      sync.RWMutex
	outcome processOutcome
}

func Start(spec Command) (*Running, error) {
	return StartContext(context.Background(), spec)
}

func StartContext(ctx context.Context, spec Command) (*Running, error) {
	if err := validateCommand(spec); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owned, hasOwner := ctx.Value(ownerKey{}).(ownerContext)
	if hasOwner && (owned.journal == nil || owned.owner.Validate() != nil) {
		return nil, errors.New("invalid durable process ownership")
	}
	invocationBytes := make([]byte, 16)
	if _, err := rand.Read(invocationBytes); err != nil {
		return nil, err
	}
	invocationID := "process_" + hex.EncodeToString(invocationBytes)
	if hasOwner {
		if err := owned.journal.BeginProcess(ctx, ProcessIntent{ID: invocationID, Owner: owned.owner}); err != nil {
			return nil, err
		}
	}
	finishStartFailure := func(reason string) error {
		if !hasOwner {
			return nil
		}
		cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		return owned.journal.FinishProcess(cleanCtx, invocationID, ProcessTerminated, reason)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, errors.Join(err, finishStartFailure("startup pipe failed"))
	}
	defer reader.Close()
	defer writer.Close()
	command, err := wrapperCommand(spec, reader)
	if err != nil {
		return nil, errors.Join(err, finishStartFailure("wrapper preparation failed"))
	}
	startedAt := time.Now().UTC()
	if err := command.Start(); err != nil {
		return nil, errors.Join(fmt.Errorf("start %q: %w", spec.Argv[0], err), finishStartFailure("wrapper start failed"))
	}
	_ = reader.Close()
	identity, identityErr := InspectProcess(command.Process.Pid)
	registrationErr := identityErr
	if registrationErr == nil && hasOwner {
		registrationErr = owned.journal.RegisterProcess(ctx, invocationID, identity)
	}
	if registrationErr == nil {
		registrationErr = ctx.Err()
	}
	if registrationErr != nil {
		_ = writer.Close()
		_ = command.Wait()
		return nil, errors.Join(registrationErr, finishStartFailure("wrapper was not released"))
	}
	if _, err := writer.Write([]byte{1}); err != nil {
		_ = writer.Close()
		_ = command.Wait()
		return nil, errors.Join(err, finishStartFailure("wrapper release failed"))
	}
	_ = writer.Close()
	running := &Running{command: command, done: make(chan struct{}), grace: spec.GracePeriod, identity: identity, journal: owned.journal, invocationID: invocationID}
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
		alive, groupErr := ProcessGroupAlive(identity.PGID)
		state := ProcessExited
		if groupErr != nil || alive {
			err = errors.Join(err, ErrProcessUnconfirmed, groupErr)
			state = ProcessUnknown
		}
		running.mu.Lock()
		if running.terminated && state != ProcessUnknown {
			state = ProcessTerminated
		}
		terminated := running.terminated
		running.mu.Unlock()
		if running.journal != nil {
			cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			journalErr := running.journal.FinishProcess(cleanCtx, invocationID, state, "process group and output observation finished")
			cancel()
			if journalErr != nil {
				err = errors.Join(err, ErrProcessUnconfirmed, journalErr)
			}
		}
		running.mu.Lock()
		running.outcome = processOutcome{execution: Execution{
			PID: command.Process.Pid, ExitCode: exitCode, StartedAt: startedAt, FinishedAt: time.Now().UTC(), Terminated: terminated,
		}, err: err}
		running.mu.Unlock()
		close(running.done)
	}()
	return running, nil
}

func Run(ctx context.Context, spec Command) (Execution, error) {
	running, err := StartContext(ctx, spec)
	if err != nil {
		return Execution{}, err
	}
	return running.WaitAndStop(ctx)
}

// WaitAndStop owns cancellation as well as observation. A failed cleanup returns
// its ownership error without waiting indefinitely for an unconfirmed process.
func (running *Running) WaitAndStop(ctx context.Context) (Execution, error) {
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
	running.termMu.Lock()
	defer running.termMu.Unlock()
	select {
	case <-running.done:
		running.mu.RLock()
		defer running.mu.RUnlock()
		if errors.Is(running.outcome.err, ErrProcessUnconfirmed) {
			return running.outcome.err
		}
		return nil
	default:
	}
	running.mu.Lock()
	running.terminated = true
	running.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), running.grace+3*time.Second)
	defer cancel()
	if err := TerminateOwnedGroup(ctx, running.identity, running.grace); err != nil {
		return err
	}
	select {
	case <-running.done:
		running.mu.RLock()
		defer running.mu.RUnlock()
		if errors.Is(running.outcome.err, ErrProcessUnconfirmed) {
			return running.outcome.err
		}
		return nil
	case <-ctx.Done():
		return errors.Join(ErrProcessUnconfirmed, ctx.Err())
	}
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
	process, err := StartContext(ctx, spec.Command)
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
			if !errors.Is(waitErr, ErrProcessUnconfirmed) {
				group.remove(spec.ID, process)
			}
			if waitErr != nil {
				return 0, fmt.Errorf("service %q exited before becoming healthy: %w", spec.ID, waitErr)
			}
			return 0, fmt.Errorf("service %q exited %d before becoming healthy", spec.ID, execution.ExitCode)
		case <-probeContext.Done():
			terminateErr := process.Terminate()
			if terminateErr == nil {
				group.remove(spec.ID, process)
			}
			return 0, fmt.Errorf("service %q health probe failed: %w", spec.ID, errors.Join(probeContext.Err(), lastProbeError, terminateErr))
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
