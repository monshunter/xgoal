package goalcompile

import (
	"fmt"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/validationplan"
	"strings"
)

// Generated checks are namespaced at compilation, never installed in project configuration.
func prepareGenerated(goalID string, c Contract, p Plan, trusted map[string]bool, coverage *config.ValidationCapabilities) (Contract, Plan, map[string]bool, *config.ValidationCapabilities, error) {
	if err := validationplan.ValidateGenerated(c.GeneratedValidators); err != nil {
		return c, p, nil, nil, err
	}
	if len(c.GeneratedValidators) == 0 {
		return c, p, trusted, coverage, nil
	}
	c.GeneratedValidators = append([]validationplan.Generated(nil), c.GeneratedValidators...)
	p = normalizePlan(p)
	allowed := map[string]bool{}
	for id, v := range trusted {
		allowed[id] = v
	}
	if coverage != nil {
		copy := *coverage
		copy.Validators = append([]config.ValidatorCapability(nil), coverage.Validators...)
		coverage = &copy
	}
	for n := range c.GeneratedValidators {
		g := &c.GeneratedValidators[n]
		old := g.ID
		if trusted[old] {
			return c, p, nil, nil, fmt.Errorf("generated validator cannot replace project validator %s", old)
		}
		if !strings.HasPrefix(g.ID, goalID+"__") {
			g.ID = goalID + "__" + old
		}
		if allowed[g.ID] {
			return c, p, nil, nil, fmt.Errorf("generated validator namespace collision: %s", g.ID)
		}
		allowed[g.ID] = true
		criteria, works := 0, 0
		for i := range c.AcceptanceCriteria {
			for j, id := range c.AcceptanceCriteria[i].Validators {
				if id == old {
					c.AcceptanceCriteria[i].Validators[j] = g.ID
					criteria++
				}
			}
		}
		for i := range p.WorkItems {
			for j, id := range p.WorkItems[i].Validators {
				if id == old {
					p.WorkItems[i].Validators[j] = g.ID
					works++
				}
			}
		}
		if criteria == 0 || works == 0 {
			return c, p, nil, nil, fmt.Errorf("generated validator %s must cover a criterion and Work", old)
		}
		if coverage != nil {
			coverage.Validators = append(coverage.Validators, config.ValidatorCapability{ID: g.ID, Description: g.Description, Coverage: "generated", Type: "command", Phases: []string{"change", "final"}})
		}
	}
	if err := validationplan.ValidateGenerated(c.GeneratedValidators); err != nil {
		return c, p, nil, nil, err
	}
	return c, p, allowed, coverage, nil
}
