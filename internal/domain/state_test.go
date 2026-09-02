package domain_test

import (
	"testing"

	"github.com/monshunter/xgoal/internal/domain"
)

func TestGoalStateTransitions(t *testing.T) {
	tests := []struct {
		from domain.GoalState
		to   domain.GoalState
		ok   bool
	}{
		{domain.GoalDraft, domain.GoalReady, true},
		{domain.GoalRunning, domain.GoalWaiting, true},
		{domain.GoalVerifying, domain.GoalCompleted, true},
		{domain.GoalCompleted, domain.GoalRunning, false},
		{domain.GoalReady, domain.GoalCompleted, false},
	}
	for _, tt := range tests {
		if got := tt.from.CanTransition(tt.to); got != tt.ok {
			t.Errorf("%s.CanTransition(%s) = %v, want %v", tt.from, tt.to, got, tt.ok)
		}
	}
}

func TestWorkStateTransitions(t *testing.T) {
	if !domain.WorkRunning.CanTransition(domain.WorkVerifying) {
		t.Fatal("RUNNING should transition to VERIFYING")
	}
	if !domain.WorkReconciling.CanTransition(domain.WorkReady) {
		t.Fatal("RECONCILING should transition to READY")
	}
	if domain.WorkCompleted.CanTransition(domain.WorkReady) {
		t.Fatal("COMPLETED must be terminal")
	}
}

func TestAttemptAndEffectTerminalRules(t *testing.T) {
	if !domain.AttemptRunning.CanTransition(domain.AttemptTimedOut) {
		t.Fatal("non-terminal attempt should transition to TIMED_OUT")
	}
	if domain.AttemptSucceeded.CanTransition(domain.AttemptRunning) {
		t.Fatal("SUCCEEDED attempt must be terminal")
	}
	if !domain.EffectExecuting.CanTransition(domain.EffectRecovering) {
		t.Fatal("EXECUTING effect should transition to RECOVERING")
	}
	if domain.EffectSucceeded.CanTransition(domain.EffectObserving) {
		t.Fatal("SUCCEEDED effect must be terminal")
	}
}

func TestTransitionRejectsUnknownState(t *testing.T) {
	if err := domain.ValidateGoalTransition(domain.GoalState("UNKNOWN"), domain.GoalReady); err == nil {
		t.Fatal("ValidateGoalTransition() accepted unknown source state")
	}
}

func TestStateTransitionPredicatesMatchValidators(t *testing.T) {
	tests := []struct {
		name     string
		states   []string
		allows   func(string, string) bool
		validate func(string, string) error
	}{
		{
			name: "goal",
			states: []string{
				string(domain.GoalDraft), string(domain.GoalReady), string(domain.GoalRunning),
				string(domain.GoalWaiting), string(domain.GoalVerifying), string(domain.GoalCompleted), string(domain.GoalCancelled),
			},
			allows: func(from, to string) bool {
				return domain.GoalState(from).CanTransition(domain.GoalState(to))
			},
			validate: func(from, to string) error {
				return domain.ValidateGoalTransition(domain.GoalState(from), domain.GoalState(to))
			},
		},
		{
			name: "work",
			states: []string{
				string(domain.WorkPending), string(domain.WorkReady), string(domain.WorkClaimed), string(domain.WorkRunning),
				string(domain.WorkVerifying), string(domain.WorkReconciling), string(domain.WorkWaiting), string(domain.WorkCompleted), string(domain.WorkCancelled),
			},
			allows: func(from, to string) bool {
				return domain.WorkState(from).CanTransition(domain.WorkState(to))
			},
			validate: func(from, to string) error {
				return domain.ValidateWorkTransition(domain.WorkState(from), domain.WorkState(to))
			},
		},
		{
			name: "attempt",
			states: []string{
				string(domain.AttemptCreated), string(domain.AttemptPreparing), string(domain.AttemptStarting), string(domain.AttemptRunning),
				string(domain.AttemptCollecting), string(domain.AttemptValidating), string(domain.AttemptReviewing), string(domain.AttemptPromoting),
				string(domain.AttemptSucceeded), string(domain.AttemptFailed), string(domain.AttemptTimedOut), string(domain.AttemptInterrupted),
				string(domain.AttemptInvalidOutput), string(domain.AttemptQuarantined),
			},
			allows: func(from, to string) bool {
				return domain.AttemptState(from).CanTransition(domain.AttemptState(to))
			},
			validate: func(from, to string) error {
				return domain.ValidateAttemptTransition(domain.AttemptState(from), domain.AttemptState(to))
			},
		},
		{
			name: "effect",
			states: []string{
				string(domain.EffectRequested), string(domain.EffectExecuting), string(domain.EffectObserving),
				string(domain.EffectRecovering), string(domain.EffectSucceeded), string(domain.EffectFailed),
			},
			allows: func(from, to string) bool {
				return domain.EffectState(from).CanTransition(domain.EffectState(to))
			},
			validate: func(from, to string) error {
				return domain.ValidateEffectTransition(domain.EffectState(from), domain.EffectState(to))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, from := range test.states {
				for _, to := range test.states {
					allowed := test.allows(from, to)
					valid := test.validate(from, to) == nil
					if allowed != valid {
						t.Fatalf("%s -> %s: predicate = %v, validator = %v", from, to, allowed, valid)
					}
				}
			}
		})
	}
}
