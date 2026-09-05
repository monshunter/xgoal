package sqlite

import (
	"errors"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/planner"
)

var (
	ErrPlanningBlocked      = errors.New("PLANNING_WAITING: planning requires an explicit operator decision")
	ErrConfigurationChanged = errors.New("CONFIGURATION_CHANGED: restart the daemon with the current xgoal.yaml, then explicitly retry planning")
)

type PlanningRecord struct {
	Goal        domain.Goal
	Effect      domain.Effect
	Request     planner.Request
	Observation *planner.Observation
	Generation  int64
	Paused      bool
	State       string
	Blocker     string
}

type PlanningClaim struct {
	GoalID, EffectID                                       string
	Generation, ExpectedGoalVersion, ExpectedEffectVersion int64
	CurrentConfigHash, InvocationID, InputTree             string
	CheckoutIdentity                                       gitrepo.CheckoutIdentity
}
type PlanningResult struct {
	GoalID, EffectID                  string
	Generation, ExpectedEffectVersion int64
	InvocationID, SessionID           string
	Proposal                          planner.Proposal
}
type PlanningPublish struct {
	GoalID, EffectID                                       string
	Generation, ExpectedGoalVersion, ExpectedEffectVersion int64
	ObservationHash, CurrentConfigHash                     string
}
type PlanningFailure struct {
	GoalID, EffectID                  string
	Generation, ExpectedEffectVersion int64
	Code, Reason                      string
	ExecutionStopped                  bool
}
type PlanningRecoveryOptions struct {
	CurrentConfigHash string
	NoProgressLimit   int
}
