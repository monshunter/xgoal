package orchestrator

import (
	"context"
	"fmt"
	"github.com/monshunter/xgoal/internal/scope"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/validationplan"
)

func (engine *Engine) planningInputs(ctx context.Context, record sqlite.PlanningRecord) ([]validationplan.Input, error) {
	paths, err := validationplan.Paths(record.Request.AcceptanceFiles)
	if err != nil {
		return nil, err
	}
	policy, err := scope.NewPolicy([]string{"/**"}, engine.config.ScopePolicy.Deny)
	if err != nil {
		return nil, err
	}
	if err := policy.CheckWrite(paths); err != nil {
		return nil, fmt.Errorf("acceptance inputs are outside project read permissions: %w", err)
	}
	var inputs []validationplan.Input
	for _, p := range paths {
		entry, content, err := engine.repository.ReadFileAtRevision(ctx, record.Observation.InputTree, p, validationplan.MaxInputBytes)
		if err != nil {
			return nil, fmt.Errorf("read acceptance file %s from planning input tree: %w", p, err)
		}
		i, err := validationplan.NewInput(p, entry.Mode, content)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, i)
	}
	return inputs, nil
}
