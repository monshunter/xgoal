package goalcompile

import (
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/config"
)

func TestCompileRequiresEveryScenarioBusinessAssertion(t *testing.T) {
	contract, plan := validContract(), validPlan()
	capabilities := &config.ValidationCapabilities{
		Validators:          []config.ValidatorCapability{{ID: "go-test", Phases: []string{"change", "final"}}, {ID: "business", Phases: []string{"final"}}},
		Scenarios:           []config.Scenario{{ID: "workflow", Validators: []string{"business"}}},
		RequiredScenarioIDs: []string{"workflow"},
	}
	contract.AcceptanceCriteria[0].ScenarioIDs = []string{"workflow"}
	trusted := map[string]bool{"go-test": true, "business": true}
	if _, err := Compile("goal", "revision", "plan", contract, plan, trusted, capabilities); err == nil || !strings.Contains(err.Error(), "business") {
		t.Fatalf("format/unit check accepted in place of business assertion: %v", err)
	}
	contract.AcceptanceCriteria[0].Validators = append(contract.AcceptanceCriteria[0].Validators, "business")
	if _, err := Compile("goal", "revision", "plan", contract, plan, trusted, capabilities); err != nil {
		t.Fatal(err)
	}
	plan.WorkItems[0].Validators = []string{"business"}
	if _, err := Compile("goal", "revision", "plan", contract, plan, trusted, capabilities); err == nil {
		t.Fatal("final-only assertion accepted for change validation")
	}
	plan = validPlan()
	contract.AcceptanceCriteria[0].ScenarioIDs = nil
	if _, err := Compile("goal", "revision", "plan", contract, plan, trusted, capabilities); err == nil {
		t.Fatal("configured acceptance scenario omitted from frozen goal")
	}
	contract.AcceptanceCriteria[0].ScenarioIDs = []string{"invented"}
	if _, err := Compile("goal", "revision", "plan", contract, plan, trusted, capabilities); err == nil {
		t.Fatal("unknown scenario accepted")
	}
}
