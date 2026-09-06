package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/review"
	"github.com/monshunter/xgoal/internal/supervisor"
)

type reviewerProbe struct {
	adapter.Adapter
	status string
	err    error
	calls  int
	cancel context.CancelFunc
}

func (p *reviewerProbe) Probe(context.Context, adapter.ProbeSpec) (adapter.Capabilities, error) {
	p.calls++
	if p.cancel != nil {
		p.cancel()
	}
	return adapter.Capabilities{Version: "fixture", CredentialStatus: p.status}, p.err
}
func (p *reviewerProbe) Review(context.Context, review.Invocation, adapter.EventSink) (review.Execution, error) {
	panic("selection must not invoke the provider review")
}

func TestDefaultReviewerSkipsMissingCredentialsWithoutChangingExplicitBinding(t *testing.T) {
	for _, tc := range []string{"default", "explicit", "unknown", "all missing", "unconfirmed", "cancelled", "cancelled unconfirmed"} {
		t.Run(tc, func(t *testing.T) {
			impl := config.Agent{ID: "codex", Adapter: "codex-cli", Roles: []string{"implementer", "reviewer"}}
			other := config.Agent{ID: "claude", Adapter: "claude-cli", Roles: []string{"reviewer"}}
			worker := &reviewerProbe{status: "available"}
			preferred := &reviewerProbe{status: "missing"}
			engine := &Engine{adapters: map[string]adapter.Adapter{"codex": worker, "claude": preferred}, reviewers: map[string]review.Adapter{"codex": worker, "claude": preferred}}
			engine.config.Agents = []config.Agent{impl, other}
			engine.config.Review.PreferDifferentProvider = true
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch tc {
			case "explicit":
				engine.config.Orchestration.RoleProfiles = map[string]string{"reviewer": "claude"}
			case "unknown":
				preferred.status = "unknown"
			case "all missing":
				worker.status = "missing"
			case "unconfirmed":
				preferred.err = supervisor.ErrProcessUnconfirmed
			case "cancelled":
				cancel()
			case "cancelled unconfirmed":
				preferred.cancel = cancel
				preferred.err = supervisor.ErrProcessUnconfirmed
			}
			selected, _, _, err := engine.reviewerProfile(ctx, impl)
			switch tc {
			case "default":
				if err != nil || selected.ID != "codex" {
					t.Fatalf("default failed: %s %v", selected.ID, err)
				}
			case "unknown":
				if err != nil || selected.ID != "claude" {
					t.Fatalf("unknown treated as missing: %s %v", selected.ID, err)
				}
			case "explicit":
				if err == nil || !strings.Contains(err.Error(), "explicit Reviewer") || worker.calls != 0 {
					t.Fatalf("explicit binding replaced: %s %v calls=%d", selected.ID, err, worker.calls)
				}
			case "all missing":
				if err == nil || !strings.Contains(err.Error(), "no available independent Reviewer") {
					t.Fatalf("missing accepted: %v", err)
				}
			case "unconfirmed", "cancelled unconfirmed":
				if !errors.Is(err, supervisor.ErrProcessUnconfirmed) || worker.calls != 0 {
					t.Fatalf("ownership loss ignored: %v", err)
				}
			case "cancelled":
				if !errors.Is(err, context.Canceled) || worker.calls+preferred.calls != 0 {
					t.Fatal("continued after cancellation")
				}
			}
			if len(engine.config.Agents[1].Roles) != 1 {
				t.Fatal("mutated frozen configuration")
			}
		})
	}
}
