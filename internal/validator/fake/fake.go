package fake

import (
	"context"
	"sync"

	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/validator"
)

type Runner struct {
	mu       sync.Mutex
	evidence protocol.Evidence
	err      error
	requests []validator.Request
}

func New(evidence protocol.Evidence, err error) *Runner {
	return &Runner{evidence: evidence, err: err}
}

func (r *Runner) Run(ctx context.Context, request validator.Request) (protocol.Evidence, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Evidence{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
	return r.evidence, r.err
}

func (r *Runner) Requests() []validator.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]validator.Request(nil), r.requests...)
}
