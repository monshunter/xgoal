package kernel_test

import (
	"testing"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/kernel"
)

func TestCompletionPredicateRequiresEveryClosureFact(t *testing.T) {
	baseline := kernel.CompletionInput{
		GoalState:                 domain.GoalVerifying,
		RequiredWorkStates:        []domain.WorkState{domain.WorkCompleted},
		IntegrationTree:           "tree-final",
		ExpectedTree:              "tree-final",
		Criteria:                  []kernel.CriterionStatus{{ID: "AC-1", Satisfied: true, Current: true, TreeHash: "tree-final"}},
		OpenBlockingFindings:      0,
		OpenRequiredGates:         0,
		ScopePolicyPassed:         true,
		FinalValidationSetCurrent: true,
		FinalReportGenerated:      true,
		HumanAcceptanceSatisfied:  true,
	}
	if result := kernel.EvaluateCompletion(baseline); !result.Complete || len(result.Reasons) != 0 {
		t.Fatalf("EvaluateCompletion() = %+v", result)
	}

	tests := []struct {
		name   string
		mutate func(*kernel.CompletionInput)
	}{
		{name: "goal state", mutate: func(input *kernel.CompletionInput) { input.GoalState = domain.GoalRunning }},
		{name: "required work", mutate: func(input *kernel.CompletionInput) { input.RequiredWorkStates[0] = domain.WorkVerifying }},
		{name: "tree mismatch", mutate: func(input *kernel.CompletionInput) { input.IntegrationTree = "tree-old" }},
		{name: "criterion unknown", mutate: func(input *kernel.CompletionInput) { input.Criteria[0].Satisfied = false }},
		{name: "criterion stale", mutate: func(input *kernel.CompletionInput) { input.Criteria[0].Current = false }},
		{name: "blocking finding", mutate: func(input *kernel.CompletionInput) { input.OpenBlockingFindings = 1 }},
		{name: "gate", mutate: func(input *kernel.CompletionInput) { input.OpenRequiredGates = 1 }},
		{name: "scope", mutate: func(input *kernel.CompletionInput) { input.ScopePolicyPassed = false }},
		{name: "final validation", mutate: func(input *kernel.CompletionInput) { input.FinalValidationSetCurrent = false }},
		{name: "report", mutate: func(input *kernel.CompletionInput) { input.FinalReportGenerated = false }},
		{name: "human acceptance", mutate: func(input *kernel.CompletionInput) {
			input.HumanAcceptanceRequired = true
			input.HumanAcceptanceSatisfied = false
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := baseline
			input.RequiredWorkStates = append([]domain.WorkState(nil), baseline.RequiredWorkStates...)
			input.Criteria = append([]kernel.CriterionStatus(nil), baseline.Criteria...)
			tt.mutate(&input)
			if result := kernel.EvaluateCompletion(input); result.Complete || len(result.Reasons) == 0 {
				t.Fatalf("EvaluateCompletion() = %+v", result)
			}
		})
	}
}
