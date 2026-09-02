package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/protocol"
)

type Script struct {
	Events           []protocol.AgentEvent
	Result           protocol.AgentResult
	Err              error
	BlockUntilCancel bool
}

type Adapter struct {
	mu          sync.Mutex
	id          string
	script      Script
	executions  map[string]*execution
	invocations []adapter.Invocation
}

type execution struct {
	done   chan struct{}
	once   sync.Once
	result protocol.AgentResult
	err    error
}

func New(id string, script Script) *Adapter {
	return &Adapter{id: id, script: script, executions: make(map[string]*execution)}
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Probe(_ context.Context, spec adapter.ProbeSpec) (adapter.Capabilities, error) {
	if spec.Mode != adapter.ProbePassive && spec.Mode != adapter.ProbeActiveContract {
		return adapter.Capabilities{}, fmt.Errorf("unsupported probe mode %q", spec.Mode)
	}
	return adapter.Capabilities{
		Version:           "fake/v1",
		StructuredOutput:  true,
		StreamingEvents:   true,
		ResumeSession:     true,
		UsageReporting:    true,
		CostReporting:     true,
		SandboxModes:      []string{"read-only", "workspace-write"},
		ToolAllowlist:     true,
		ApprovalModes:     []string{"never"},
		ProbeMode:         spec.Mode,
		ProviderTransport: "not-required",
		CredentialStatus:  "not-required",
	}, nil
}

func (a *Adapter) Start(_ context.Context, invocation adapter.Invocation, sink adapter.EventSink) (adapter.Handle, error) {
	if invocation.InvocationID == "" || invocation.AttemptID == "" || !invocation.Role.Valid() {
		return adapter.Handle{}, fmt.Errorf("invocation id, attempt id, and valid role are required")
	}

	a.mu.Lock()
	if _, exists := a.executions[invocation.InvocationID]; exists {
		a.mu.Unlock()
		return adapter.Handle{}, fmt.Errorf("invocation %q already exists", invocation.InvocationID)
	}
	execution := &execution{done: make(chan struct{}), result: a.script.Result, err: a.script.Err}
	a.executions[invocation.InvocationID] = execution
	a.invocations = append(a.invocations, invocation)
	events := append([]protocol.AgentEvent(nil), a.script.Events...)
	block := a.script.BlockUntilCancel
	a.mu.Unlock()

	for _, event := range events {
		if sink != nil {
			if err := sink(event); err != nil {
				execution.finish(protocol.AgentResult{}, err)
				return adapter.Handle{ID: invocation.InvocationID}, nil
			}
		}
	}
	if !block {
		execution.finish(execution.result, execution.err)
	}
	return adapter.Handle{ID: invocation.InvocationID}, nil
}

func (a *Adapter) Resume(ctx context.Context, invocation adapter.Invocation, _ string, sink adapter.EventSink) (adapter.Handle, error) {
	return a.Start(ctx, invocation, sink)
}

func (a *Adapter) Cancel(_ context.Context, handle adapter.Handle) error {
	execution, err := a.execution(handle)
	if err != nil {
		return err
	}
	execution.finish(protocol.AgentResult{}, context.Canceled)
	return nil
}

func (a *Adapter) Wait(ctx context.Context, handle adapter.Handle) (protocol.AgentResult, error) {
	execution, err := a.execution(handle)
	if err != nil {
		return protocol.AgentResult{}, err
	}
	select {
	case <-ctx.Done():
		return protocol.AgentResult{}, ctx.Err()
	case <-execution.done:
		return execution.result, execution.err
	}
}

func (a *Adapter) InvocationCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.invocations)
}

func (a *Adapter) execution(handle adapter.Handle) (*execution, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	execution, exists := a.executions[handle.ID]
	if !exists {
		return nil, fmt.Errorf("unknown invocation handle %q", handle.ID)
	}
	return execution, nil
}

func (e *execution) finish(result protocol.AgentResult, err error) {
	e.once.Do(func() {
		e.result = result
		e.err = err
		close(e.done)
	})
}
