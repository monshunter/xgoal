package budget

import (
	"errors"
	"fmt"
	"sort"
)

type Dimension string

const (
	GoalAttempts        Dimension = "GOAL_ATTEMPTS"
	WorkAttempts        Dimension = "WORK_ATTEMPTS"
	AgentSessions       Dimension = "AGENT_SESSIONS"
	WallTimeMillis      Dimension = "WALL_TIME_MILLIS"
	Concurrency         Dimension = "CONCURRENCY"
	Tokens              Dimension = "TOKENS"
	CostMicros          Dimension = "COST_MICROS"
	ValidatorTimeMillis Dimension = "VALIDATOR_TIME_MILLIS"
	WorkspaceBytes      Dimension = "WORKSPACE_BYTES"
)

var dimensions = map[Dimension]struct{}{
	GoalAttempts: {}, WorkAttempts: {}, AgentSessions: {}, WallTimeMillis: {}, Concurrency: {},
	Tokens: {}, CostMicros: {}, ValidatorTimeMillis: {}, WorkspaceBytes: {},
}

func (dimension Dimension) Valid() bool {
	_, ok := dimensions[dimension]
	return ok
}

type Limit struct {
	Dimension Dimension `json:"dimension"`
	Soft      int64     `json:"soft"`
	Hard      int64     `json:"hard"`
}

type Usage struct {
	Dimension Dimension `json:"dimension"`
	Known     bool      `json:"known"`
	Consumed  int64     `json:"consumed,omitempty"`
}

type Request struct {
	Dimension Dimension `json:"dimension"`
	Known     bool      `json:"known"`
	Amount    int64     `json:"amount,omitempty"`
}

type Decision string

const (
	Allowed       Decision = "ALLOW"
	SoftAttention Decision = "SOFT_ATTENTION"
	HardBlock     Decision = "HARD_BLOCK"
	RequireUsage  Decision = "REQUIRE_USAGE"
)

type Result struct {
	Dimension Dimension `json:"dimension"`
	Decision  Decision  `json:"decision"`
	Before    Usage     `json:"before"`
	After     Usage     `json:"after"`
}

func ValidateLimit(limit Limit) error {
	if !limit.Dimension.Valid() || limit.Soft < 0 || limit.Hard <= 0 || limit.Soft > limit.Hard {
		return errors.New("invalid budget limit")
	}
	return nil
}

// Preflight checks one resource request without mutating usage.
func Preflight(limit Limit, usage Usage, request Request) (Result, error) {
	if err := ValidateLimit(limit); err != nil {
		return Result{}, err
	}
	if usage.Dimension != limit.Dimension || request.Dimension != limit.Dimension || usage.Consumed < 0 || request.Amount < 0 {
		return Result{}, errors.New("budget dimensions and non-negative values are required")
	}
	result := Result{Dimension: limit.Dimension, Before: usage, After: usage}
	if !usage.Known || !request.Known {
		result.After = Usage{Dimension: limit.Dimension, Known: false}
		result.Decision = RequireUsage
		return result, nil
	}
	if request.Amount > limit.Hard-usage.Consumed {
		result.Decision = HardBlock
		return result, nil
	}
	result.After.Consumed += request.Amount
	if result.After.Consumed >= limit.Soft {
		result.Decision = SoftAttention
	} else {
		result.Decision = Allowed
	}
	return result, nil
}

// Observe applies actual post-run usage. Unknown stays unknown and is never converted to zero.
func Observe(previous Usage, observed Usage) (Usage, error) {
	if !previous.Dimension.Valid() || previous.Dimension != observed.Dimension || previous.Consumed < 0 || observed.Consumed < 0 {
		return Usage{}, errors.New("invalid usage observation")
	}
	if !previous.Known || !observed.Known {
		return Usage{Dimension: previous.Dimension, Known: false}, nil
	}
	if observed.Consumed > int64(^uint64(0)>>1)-previous.Consumed {
		return Usage{}, fmt.Errorf("budget usage overflow")
	}
	previous.Consumed += observed.Consumed
	return previous, nil
}

func SortedDimensions() []Dimension {
	result := make([]Dimension, 0, len(dimensions))
	for dimension := range dimensions {
		result = append(result, dimension)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
