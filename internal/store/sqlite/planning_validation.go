package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/planner"
)

var ErrGeneratedValidationApproval = errors.New("generated_validation_approval: approve the exact prepared acceptance plan to continue")

func preparedValidationHash(request planner.Request, proposal planner.Proposal) (string, error) {
	compiled, err := compilePlanning(request, proposal)
	if err != nil {
		return "", err
	}
	return canonical.Hash("prepared-validation", "v1", map[string]any{"contract": compiled.Contract, "plan_hash": compiled.PlanHash})
}

func checkGeneratedApproval(request planner.Request, proposal planner.Proposal) error {
	if len(proposal.Contract.GeneratedValidators) == 0 {
		return nil
	}
	if request.GeneratedValidationPolicy == "deny" {
		return errors.New("generated validation is disabled by planning.generatedValidators; configure existing business checks or allow generated validation")
	}
	if request.GeneratedValidationPolicy == "human-gate" {
		hash, err := preparedValidationHash(request, proposal)
		if err != nil {
			return err
		}
		if request.ApprovedValidationHash != hash {
			return fmt.Errorf("%w; prepared_plan_hash=%s; inspect gate get for the complete prepared_proposal, then use approve --resume", ErrGeneratedValidationApproval, hash)
		}
	}
	return nil
}

// PreparedAcceptance returns the exact retained proposal behind an approval Gate,
// including after later generations have advanced. It creates no new authority.
func (s *Store) PreparedAcceptance(ctx context.Context, gate domain.Gate) (planner.Proposal, string, error) {
	var facts struct {
		Owner      string `json:"owner"`
		Generation int64  `json:"generation"`
	}
	if gate.ReasonCode != "generated_validation_approval" || json.Unmarshal(gate.FactsJSON, &facts) != nil || facts.Owner != "planning" || facts.Generation < 1 {
		return planner.Proposal{}, "", ErrPlanningBlocked
	}
	id, _ := planningIDs(gate.GoalID, facts.Generation)
	effect, err := s.Effect(ctx, id)
	if err != nil {
		return planner.Proposal{}, "", err
	}
	var request planner.Request
	var observation planner.Observation
	if err := json.Unmarshal(effect.RequestJSON, &request); err != nil {
		return planner.Proposal{}, "", err
	}
	if err := json.Unmarshal(effect.ObservationJSON, &observation); err != nil {
		return planner.Proposal{}, "", err
	}
	if observation.Proposal == nil || request.GoalID != gate.GoalID {
		return planner.Proposal{}, "", ErrPlanningBlocked
	}
	hash, err := preparedValidationHash(request, *observation.Proposal)
	return *observation.Proposal, hash, err
}
