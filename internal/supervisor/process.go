package supervisor

import (
	"context"
	"sync"
)

type ProcessResult struct {
	ExitCode int
}

type Process interface {
	PID() int
	Wait(context.Context) (ProcessResult, error)
	Terminate() error
}

type FakeProcess struct {
	pid    int
	done   chan struct{}
	once   sync.Once
	result ProcessResult
	err    error
}

func NewFakeProcess(pid int) *FakeProcess {
	return &FakeProcess{pid: pid, done: make(chan struct{})}
}

func (p *FakeProcess) PID() int { return p.pid }

func (p *FakeProcess) Complete(result ProcessResult) {
	p.finish(result, nil)
}

func (p *FakeProcess) Terminate() error {
	p.finish(ProcessResult{}, context.Canceled)
	return nil
}

func (p *FakeProcess) Wait(ctx context.Context) (ProcessResult, error) {
	select {
	case <-ctx.Done():
		return ProcessResult{}, ctx.Err()
	case <-p.done:
		return p.result, p.err
	}
}

func (p *FakeProcess) finish(result ProcessResult, err error) {
	p.once.Do(func() {
		p.result = result
		p.err = err
		close(p.done)
	})
}
