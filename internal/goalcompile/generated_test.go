package goalcompile

import (
	"encoding/json"
	"strings"
	"testing"
)

func generatedFixture(t *testing.T) (Contract, Plan) {
	t.Helper()
	c, p := validContract(), validPlan()
	if err := json.Unmarshal([]byte(`{"generated_validators":[{"id":"behavior","description":"exact required output","runtime":"sh","script":"test \"$(cat output.txt)\" = accepted","timeout_seconds":5}]}`), &c); err != nil {
		t.Fatal(err)
	}
	c.AcceptanceCriteria[0].Validators = []string{"behavior"}
	for n := range p.WorkItems {
		p.WorkItems[n].Validators = []string{"behavior"}
	}
	return c, p
}

func TestCompileGeneratedAcceptanceUsesGoalNamespace(t *testing.T) {
	c, p := generatedFixture(t)
	trusted := map[string]bool{"git-diff-check": true}
	a, err := Compile("goal-a", "revision-a", "plan-a", c, p, trusted)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Compile("goal-b", "revision-b", "plan-b", c, p, trusted)
	if err != nil {
		t.Fatal(err)
	}
	aID, bID := a.Contract.AcceptanceCriteria[0].Validators[0], b.Contract.AcceptanceCriteria[0].Validators[0]
	if aID == bID || !strings.HasPrefix(aID, "goal-a__") || a.WorkItems[0].ValidatorIDs[0] != aID || len(trusted) != 1 {
		t.Fatalf("bad namespace or mutated authority: %s %s %#v", aID, bID, trusted)
	}
	if c.AcceptanceCriteria[0].Validators[0] != "behavior" {
		t.Fatal("compiler mutated the input proposal")
	}
}

func TestCompileGeneratedAcceptanceRejectsAuthorityCollisionAndUnusedChecks(t *testing.T) {
	for _, tc := range []string{"existing ID", "namespace collision", "unused criterion", "unused Work"} {
		t.Run(tc, func(t *testing.T) {
			c, p := generatedFixture(t)
			trusted := map[string]bool{"check": true}
			switch tc {
			case "existing ID":
				trusted["behavior"] = true
			case "namespace collision":
				trusted["goal-a__behavior"] = true
			case "unused criterion":
				c.AcceptanceCriteria[0].Validators = []string{"check"}
			case "unused Work":
				for n := range p.WorkItems {
					p.WorkItems[n].Validators = []string{"check"}
				}
			}
			if _, err := Compile("goal-a", "revision-a", "plan-a", c, p, trusted); err == nil {
				t.Fatal("invalid generated authority accepted")
			}
		})
	}
}
