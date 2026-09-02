package kernel

import "github.com/monshunter/xgoal/internal/completion"

type CriterionStatus = completion.CriterionStatus

type CompletionInput = completion.Input

type CompletionResult = completion.Result

func EvaluateCompletion(input CompletionInput) CompletionResult {
	return completion.Evaluate(input)
}
