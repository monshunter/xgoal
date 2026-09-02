package goalcompile

import "testing"

func TestCompileValidContractAndPlan(t *testing.T) {
	contract := validContract()
	plan := validPlan()
	compiled, err := Compile("goal-1", "revision-1", "plan-1", contract, plan, map[string]bool{"go-test": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.WorkItems) != 2 || len(compiled.Dependencies) != 1 || compiled.ContractHash == "" || compiled.PlanHash == "" {
		t.Fatalf("compiled = %+v", compiled)
	}
	if compiled.Dependencies[0].FromID == "prepare" || compiled.Dependencies[0].ToID == "implement" {
		t.Fatal("client keys leaked into persisted identities")
	}
}

func TestCompileRejectsCriticalAmbiguityAndUnsafePlan(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Contract, *Plan)
	}{
		{"unverifiable criterion", func(contract *Contract, _ *Plan) { contract.AcceptanceCriteria[0].Validators = nil }},
		{"scope contradiction", func(contract *Contract, _ *Plan) {
			contract.OutOfScope = append(contract.OutOfScope, contract.InScope[0])
		}},
		{"unknown validator", func(_ *Contract, plan *Plan) { plan.WorkItems[0].Validators = []string{"agent-injected"} }},
		{"uncovered criterion", func(_ *Contract, plan *Plan) { plan.WorkItems[1].AcceptanceCriteria = []string{"AC-OTHER"} }},
		{"parallel write conflict", func(_ *Contract, plan *Plan) { plan.WorkItems[1].DependsOn = nil }},
		{"unbounded work", func(_ *Contract, plan *Plan) { plan.WorkItems[0].WriteScope = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract, plan := validContract(), validPlan()
			test.mutate(&contract, &plan)
			if _, err := Compile("goal-1", "revision-1", "plan-1", contract, plan, map[string]bool{"go-test": true}); err == nil {
				t.Fatal("unsafe proposal unexpectedly compiled")
			}
		})
	}
}

func validContract() Contract {
	return Contract{
		Summary: "implement safely", Rationale: "user value", InScope: []string{"local repository"}, OutOfScope: []string{"production"},
		Constraints: []string{"no remote push"}, AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC-1", Statement: "tests pass", Validators: []string{"go-test"}}},
		QualityAttributes: []string{"correctness"}, HumanGates: []string{"expand scope"},
		CompletionPolicy: CompletionPolicy{RequireAllRequiredItems: true, RequireNoBlockingFindings: true, RequireFinalValidation: true},
	}
}

func validPlan() Plan {
	return Plan{Summary: "two steps", WorkItems: []PlanWork{
		{ClientKey: "prepare", Title: "prepare", Objective: "prepare change", ReadScope: []string{"/**"}, WriteScope: []string{"/internal/**"}, AcceptanceCriteria: []string{"AC-1"}, Validators: []string{"go-test"}, RecommendedRole: "implementer", Required: true},
		{ClientKey: "implement", Title: "implement", Objective: "finish change", DependsOn: []string{"prepare"}, ReadScope: []string{"/**"}, WriteScope: []string{"/internal/**"}, AcceptanceCriteria: []string{"AC-1"}, Validators: []string{"go-test"}, RecommendedRole: "implementer", Required: true},
	}}
}
