package orchestrator

import (
	"context"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/validator"
)

func (engine *Engine) validationRegistry(ctx context.Context, revision domain.GoalRevision, commit string) (*validator.Registry, error) {
	frozen, err := decodeFrozenContract(revision.ContractJSON)
	if err != nil {
		return nil, err
	}
	registry, err := validator.LoadRegistry(ctx, engine.repository, commit, "xgoal.yaml")
	if err != nil {
		return nil, err
	}
	return registry.WithAcceptance(frozen.Contract.GeneratedValidators, frozen.Contract.AcceptanceInputs)
}
