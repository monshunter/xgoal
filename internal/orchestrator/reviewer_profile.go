package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/review"
	"github.com/monshunter/xgoal/internal/supervisor"
)

// Only default selection may skip unavailable candidates, before any review is
// invoked. A rejected review never causes selection of a different reviewer.
func (engine *Engine) reviewerProfile(ctx context.Context, implementer config.Agent) (config.Agent, review.Adapter, adapter.Capabilities, error) {
	remaining := engine.config
	remaining.Agents = append([]config.Agent(nil), engine.config.Agents...)
	var unavailable []string
	for {
		if err := ctx.Err(); err != nil {
			return config.Agent{}, nil, adapter.Capabilities{}, err
		}
		selected, source, err := remaining.SelectProfile("reviewer", implementer.ID)
		if err != nil {
			return config.Agent{}, nil, adapter.Capabilities{}, fmt.Errorf("no available independent Reviewer: %s; configure or sign in to a trusted Reviewer Profile: %w", strings.Join(unavailable, "; "), err)
		}
		runtime, reviewer := engine.adapters[selected.ID], engine.reviewers[selected.ID]
		var capabilities adapter.Capabilities
		if runtime == nil || reviewer == nil {
			err = errors.New("adapter is unavailable")
		} else {
			capabilities, err = runtime.Probe(ctx, adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: selected.ID, Timeout: 10 * time.Second})
		}
		if errors.Is(err, supervisor.ErrProcessUnconfirmed) {
			return config.Agent{}, nil, adapter.Capabilities{}, err
		}
		if ctx.Err() != nil {
			return config.Agent{}, nil, adapter.Capabilities{}, ctx.Err()
		}
		if err == nil && capabilities.CredentialStatus != "missing" {
			return selected, reviewer, capabilities, nil
		}
		if err == nil {
			err = errors.New("credentials are missing")
		}
		reason := fmt.Sprintf("%s: %v", selected.ID, err)
		if source == "explicit-role-binding" {
			return config.Agent{}, nil, adapter.Capabilities{}, fmt.Errorf("explicit Reviewer Profile %s; sign in or update the explicit role binding", reason)
		}
		unavailable = append(unavailable, reason)
		for n := range remaining.Agents {
			if remaining.Agents[n].ID != selected.ID {
				continue
			}
			roles := []string{}
			for _, role := range remaining.Agents[n].Roles {
				if role != "reviewer" {
					roles = append(roles, role)
				}
			}
			remaining.Agents[n].Roles = roles
		}
	}
}
