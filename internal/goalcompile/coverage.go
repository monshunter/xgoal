package goalcompile

import (
	"fmt"
	"slices"

	"github.com/monshunter/xgoal/internal/config"
)

// ValidateCoverage checks declared relationships, never guesses the meaning of
// a command name. A scenario's assertions must appear in the same criterion.
func ValidateCoverage(contract Contract, trusted map[string]bool, capabilities *config.ValidationCapabilities) error {
	scenarios := map[string]config.Scenario{}
	validators := map[string]config.ValidatorCapability{}
	if capabilities != nil {
		for _, s := range capabilities.Scenarios {
			scenarios[s.ID] = s
		}
		for _, v := range capabilities.Validators {
			validators[v.ID] = v
		}
	}
	covered := map[string]bool{}
	for _, criterion := range contract.AcceptanceCriteria {
		for _, id := range criterion.Validators {
			if !trusted[id] {
				return fmt.Errorf("criterion %q references untrusted validator %q", criterion.ID, id)
			}
			if capabilities != nil {
				v, exists := validators[id]
				if !exists || !slices.Contains(v.Phases, "final") {
					return fmt.Errorf("criterion %q validator %q must support final validation", criterion.ID, id)
				}
			}
		}
		for _, id := range criterion.ScenarioIDs {
			scenario, exists := scenarios[id]
			if !exists {
				return fmt.Errorf("criterion %q references unknown scenario %q", criterion.ID, id)
			}
			for _, validator := range scenario.Validators {
				if !slices.Contains(criterion.Validators, validator) {
					return fmt.Errorf("criterion %q scenario %q requires business validator %q", criterion.ID, id, validator)
				}
			}
			covered[id] = true
		}
	}
	if capabilities != nil {
		for _, id := range capabilities.RequiredScenarioIDs {
			if !covered[id] {
				return fmt.Errorf("required acceptance scenario %q has no criterion mapping", id)
			}
		}
	}
	return nil
}
