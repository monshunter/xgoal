package budget_test

import (
	"testing"

	"github.com/monshunter/xgoal/internal/budget"
)

func TestPreflightSoftAndHardLimits(t *testing.T) {
	limit := budget.Limit{Dimension: budget.GoalAttempts, Soft: 2, Hard: 3}
	usage := budget.Usage{Dimension: budget.GoalAttempts, Known: true, Consumed: 1}
	soft, err := budget.Preflight(limit, usage, budget.Request{Dimension: budget.GoalAttempts, Known: true, Amount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if soft.Decision != budget.SoftAttention || soft.After.Consumed != 2 {
		t.Fatalf("soft = %+v", soft)
	}
	hard, err := budget.Preflight(limit, budget.Usage{Dimension: budget.GoalAttempts, Known: true, Consumed: 3}, budget.Request{Dimension: budget.GoalAttempts, Known: true, Amount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if hard.Decision != budget.HardBlock || hard.After.Consumed != 3 {
		t.Fatalf("hard = %+v", hard)
	}
}

func TestUnknownUsageIsNeverZero(t *testing.T) {
	limit := budget.Limit{Dimension: budget.Tokens, Soft: 800, Hard: 1000}
	result, err := budget.Preflight(limit, budget.Usage{Dimension: budget.Tokens, Known: false}, budget.Request{Dimension: budget.Tokens, Known: true, Amount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != budget.RequireUsage || result.After.Known || result.After.Consumed != 0 {
		t.Fatalf("unknown result = %+v", result)
	}
	observed, err := budget.Observe(budget.Usage{Dimension: budget.Tokens, Known: true, Consumed: 100}, budget.Usage{Dimension: budget.Tokens, Known: false})
	if err != nil {
		t.Fatal(err)
	}
	if observed.Known {
		t.Fatalf("unknown observation became known: %+v", observed)
	}
}
