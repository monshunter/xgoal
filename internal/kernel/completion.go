package kernel

import (
	"fmt"

	"github.com/monshunter/xgoal/internal/domain"
)

type CriterionStatus struct {
	ID        string
	Satisfied bool
	Current   bool
	TreeHash  string
}

type CompletionInput struct {
	GoalState                 domain.GoalState
	RequiredWorkStates        []domain.WorkState
	IntegrationTree           string
	ExpectedTree              string
	Criteria                  []CriterionStatus
	OpenBlockingFindings      int
	OpenRequiredGates         int
	ScopePolicyPassed         bool
	FinalValidationSetCurrent bool
	FinalReportGenerated      bool
	HumanAcceptanceRequired   bool
	HumanAcceptanceSatisfied  bool
}

type CompletionResult struct {
	Complete bool
	Reasons  []string
}

func EvaluateCompletion(input CompletionInput) CompletionResult {
	var reasons []string
	if input.GoalState != domain.GoalVerifying {
		reasons = append(reasons, "goal is not VERIFYING")
	}
	if len(input.RequiredWorkStates) == 0 {
		reasons = append(reasons, "required work set is empty")
	}
	for index, state := range input.RequiredWorkStates {
		if state != domain.WorkCompleted {
			reasons = append(reasons, fmt.Sprintf("required work %d is %s", index, state))
		}
	}
	if input.IntegrationTree == "" || input.IntegrationTree != input.ExpectedTree {
		reasons = append(reasons, "integration tree does not match expected final tree")
	}
	if len(input.Criteria) == 0 {
		reasons = append(reasons, "acceptance criteria set is empty")
	}
	for _, criterion := range input.Criteria {
		if criterion.ID == "" || !criterion.Satisfied || !criterion.Current || criterion.TreeHash != input.IntegrationTree {
			reasons = append(reasons, fmt.Sprintf("criterion %q is not current and satisfied on the final tree", criterion.ID))
		}
	}
	if input.OpenBlockingFindings != 0 {
		reasons = append(reasons, "blocking findings remain open")
	}
	if input.OpenRequiredGates != 0 {
		reasons = append(reasons, "required gates remain open")
	}
	if !input.ScopePolicyPassed {
		reasons = append(reasons, "scope policy has not passed")
	}
	if !input.FinalValidationSetCurrent {
		reasons = append(reasons, "final validation set is not current")
	}
	if !input.FinalReportGenerated {
		reasons = append(reasons, "final report has not been generated")
	}
	if input.HumanAcceptanceRequired && !input.HumanAcceptanceSatisfied {
		reasons = append(reasons, "required human acceptance is missing")
	}
	return CompletionResult{Complete: len(reasons) == 0, Reasons: reasons}
}
