package domain

import "fmt"

type Role string

const (
	RolePlanner     Role = "planner"
	RoleImplementer Role = "implementer"
	RoleReviewer    Role = "reviewer"
)

func (r Role) Valid() bool {
	return r == RolePlanner || r == RoleImplementer || r == RoleReviewer
}

type Authority string

const (
	AuthorityDecision      Authority = "DECISION"
	AuthorityDeterministic Authority = "DETERMINISTIC"
	AuthorityFact          Authority = "FACT"
	AuthorityInference     Authority = "INFERENCE"
	AuthorityClaim         Authority = "CLAIM"
)

func (a Authority) Valid() bool {
	return a == AuthorityDecision || a == AuthorityDeterministic || a == AuthorityFact || a == AuthorityInference || a == AuthorityClaim
}

type GoalState string

const (
	GoalDraft     GoalState = "DRAFT"
	GoalReady     GoalState = "READY"
	GoalRunning   GoalState = "RUNNING"
	GoalWaiting   GoalState = "WAITING"
	GoalVerifying GoalState = "VERIFYING"
	GoalCompleted GoalState = "COMPLETED"
	GoalCancelled GoalState = "CANCELLED"
)

var goalTransitions = transitions[GoalState]{
	GoalDraft:     set(GoalReady, GoalCancelled),
	GoalReady:     set(GoalRunning, GoalCancelled),
	GoalRunning:   set(GoalWaiting, GoalVerifying, GoalCancelled),
	GoalWaiting:   set(GoalRunning, GoalCancelled),
	GoalVerifying: set(GoalRunning, GoalWaiting, GoalCompleted, GoalCancelled),
	GoalCompleted: {},
	GoalCancelled: {},
}

func (s GoalState) CanTransition(to GoalState) bool { return goalTransitions.allows(s, to) }

func (s GoalState) Valid() bool {
	_, exists := goalTransitions[s]
	return exists
}

func ValidateGoalTransition(from, to GoalState) error {
	return goalTransitions.validate("goal", from, to)
}

type PlanRevisionState string

const (
	PlanDraft      PlanRevisionState = "DRAFT"
	PlanActive     PlanRevisionState = "ACTIVE"
	PlanSuperseded PlanRevisionState = "SUPERSEDED"
)

var planTransitions = transitions[PlanRevisionState]{
	PlanDraft:      set(PlanActive),
	PlanActive:     set(PlanSuperseded),
	PlanSuperseded: {},
}

func (s PlanRevisionState) CanTransition(to PlanRevisionState) bool {
	return planTransitions.allows(s, to)
}

func (s PlanRevisionState) Valid() bool {
	_, exists := planTransitions[s]
	return exists
}

func ValidatePlanRevisionTransition(from, to PlanRevisionState) error {
	return planTransitions.validate("plan revision", from, to)
}

type DependencyType string

const DependencyHard DependencyType = "HARD"

func (d DependencyType) Valid() bool { return d == DependencyHard }

type WorkState string

const (
	WorkPending     WorkState = "PENDING"
	WorkReady       WorkState = "READY"
	WorkClaimed     WorkState = "CLAIMED"
	WorkRunning     WorkState = "RUNNING"
	WorkVerifying   WorkState = "VERIFYING"
	WorkReconciling WorkState = "RECONCILING"
	WorkWaiting     WorkState = "WAITING"
	WorkCompleted   WorkState = "COMPLETED"
	WorkCancelled   WorkState = "CANCELLED"
)

var workTransitions = transitions[WorkState]{
	WorkPending:     set(WorkReady, WorkCancelled),
	WorkReady:       set(WorkClaimed, WorkCancelled),
	WorkClaimed:     set(WorkRunning, WorkReconciling, WorkCancelled),
	WorkRunning:     set(WorkVerifying, WorkReconciling, WorkCancelled),
	WorkVerifying:   set(WorkCompleted, WorkReconciling, WorkCancelled),
	WorkReconciling: set(WorkReady, WorkWaiting, WorkCancelled),
	WorkWaiting:     set(WorkReady, WorkCancelled),
	WorkCompleted:   {},
	WorkCancelled:   {},
}

func (s WorkState) CanTransition(to WorkState) bool { return workTransitions.allows(s, to) }

func (s WorkState) Valid() bool {
	_, exists := workTransitions[s]
	return exists
}

func ValidateWorkTransition(from, to WorkState) error {
	return workTransitions.validate("work item", from, to)
}

type AttemptState string

const (
	AttemptCreated       AttemptState = "CREATED"
	AttemptPreparing     AttemptState = "PREPARING"
	AttemptStarting      AttemptState = "STARTING"
	AttemptRunning       AttemptState = "RUNNING"
	AttemptCollecting    AttemptState = "COLLECTING"
	AttemptValidating    AttemptState = "VALIDATING"
	AttemptReviewing     AttemptState = "REVIEWING"
	AttemptPromoting     AttemptState = "PROMOTING"
	AttemptSucceeded     AttemptState = "SUCCEEDED"
	AttemptFailed        AttemptState = "FAILED"
	AttemptTimedOut      AttemptState = "TIMED_OUT"
	AttemptInterrupted   AttemptState = "INTERRUPTED"
	AttemptInvalidOutput AttemptState = "INVALID_OUTPUT"
	AttemptQuarantined   AttemptState = "QUARANTINED"
)

var attemptFailures = []AttemptState{AttemptFailed, AttemptTimedOut, AttemptInterrupted, AttemptInvalidOutput, AttemptQuarantined}

var attemptTransitions = func() transitions[AttemptState] {
	result := transitions[AttemptState]{
		AttemptCreated:       set(AttemptPreparing),
		AttemptPreparing:     set(AttemptStarting),
		AttemptStarting:      set(AttemptRunning),
		AttemptRunning:       set(AttemptCollecting),
		AttemptCollecting:    set(AttemptValidating),
		AttemptValidating:    set(AttemptReviewing, AttemptPromoting),
		AttemptReviewing:     set(AttemptPromoting),
		AttemptPromoting:     set(AttemptSucceeded),
		AttemptSucceeded:     {},
		AttemptFailed:        {},
		AttemptTimedOut:      {},
		AttemptInterrupted:   {},
		AttemptInvalidOutput: {},
		AttemptQuarantined:   {},
	}
	for state, targets := range result {
		if len(targets) == 0 {
			continue
		}
		for _, failure := range attemptFailures {
			targets[failure] = struct{}{}
		}
		result[state] = targets
	}
	return result
}()

func (s AttemptState) CanTransition(to AttemptState) bool { return attemptTransitions.allows(s, to) }

func (s AttemptState) Valid() bool {
	_, exists := attemptTransitions[s]
	return exists
}

func ValidateAttemptTransition(from, to AttemptState) error {
	return attemptTransitions.validate("attempt", from, to)
}

type LeaseState string

const (
	LeaseActive   LeaseState = "ACTIVE"
	LeaseReleased LeaseState = "RELEASED"
	LeaseExpired  LeaseState = "EXPIRED"
	LeaseRevoked  LeaseState = "REVOKED"
)

var leaseTransitions = transitions[LeaseState]{
	LeaseActive:   set(LeaseReleased, LeaseExpired, LeaseRevoked),
	LeaseReleased: {},
	LeaseExpired:  {},
	LeaseRevoked:  {},
}

func (s LeaseState) CanTransition(to LeaseState) bool { return leaseTransitions.allows(s, to) }

func (s LeaseState) Valid() bool {
	_, exists := leaseTransitions[s]
	return exists
}

func ValidateLeaseTransition(from, to LeaseState) error {
	return leaseTransitions.validate("lease", from, to)
}

type GateState string

const (
	GateOpen     GateState = "OPEN"
	GateApproved GateState = "APPROVED"
	GateDenied   GateState = "DENIED"
	GateExpired  GateState = "EXPIRED"
	GateRevoked  GateState = "REVOKED"
)

var gateTransitions = transitions[GateState]{
	GateOpen:     set(GateApproved, GateDenied, GateExpired),
	GateApproved: set(GateExpired, GateRevoked),
	GateDenied:   {},
	GateExpired:  {},
	GateRevoked:  {},
}

func (s GateState) CanTransition(to GateState) bool { return gateTransitions.allows(s, to) }

func (s GateState) Valid() bool {
	_, exists := gateTransitions[s]
	return exists
}

func ValidateGateTransition(from, to GateState) error {
	return gateTransitions.validate("gate", from, to)
}

type GateDecision string

const (
	GateAllow GateDecision = "ALLOW"
	GateDeny  GateDecision = "DENY"
)

func (d GateDecision) Valid() bool { return d == GateAllow || d == GateDeny }

type PolicyAction string

const (
	ActionReadFile              PolicyAction = "READ_FILE"
	ActionWriteFile             PolicyAction = "WRITE_FILE"
	ActionExecCommand           PolicyAction = "EXEC_COMMAND"
	ActionConnectProvider       PolicyAction = "CONNECT_PROVIDER"
	ActionAccessProjectNetwork  PolicyAction = "ACCESS_PROJECT_NETWORK"
	ActionReadEnv               PolicyAction = "READ_ENV"
	ActionUseProviderCredential PolicyAction = "USE_PROVIDER_CREDENTIAL"
	ActionUseProjectSecret      PolicyAction = "USE_PROJECT_SECRET"
	ActionModifyValidator       PolicyAction = "MODIFY_VALIDATOR"
	ActionModifyGitHistory      PolicyAction = "MODIFY_GIT_HISTORY"
	ActionPushRemote            PolicyAction = "PUSH_REMOTE"
	ActionPublishArtifact       PolicyAction = "PUBLISH_ARTIFACT"
	ActionDeployProduction      PolicyAction = "DEPLOY_PRODUCTION"
	ActionDeleteExternalData    PolicyAction = "DELETE_EXTERNAL_DATA"
	ActionExpandScope           PolicyAction = "EXPAND_SCOPE"
)

func (a PolicyAction) Valid() bool {
	switch a {
	case ActionReadFile,
		ActionWriteFile,
		ActionExecCommand,
		ActionConnectProvider,
		ActionAccessProjectNetwork,
		ActionReadEnv,
		ActionUseProviderCredential,
		ActionUseProjectSecret,
		ActionModifyValidator,
		ActionModifyGitHistory,
		ActionPushRemote,
		ActionPublishArtifact,
		ActionDeployProduction,
		ActionDeleteExternalData,
		ActionExpandScope:
		return true
	default:
		return false
	}
}

type EffectState string

const (
	EffectRequested  EffectState = "REQUESTED"
	EffectExecuting  EffectState = "EXECUTING"
	EffectObserving  EffectState = "OBSERVING"
	EffectRecovering EffectState = "RECOVERING"
	EffectSucceeded  EffectState = "SUCCEEDED"
	EffectFailed     EffectState = "FAILED"
)

var effectTransitions = transitions[EffectState]{
	EffectRequested:  set(EffectExecuting, EffectRecovering),
	EffectExecuting:  set(EffectObserving, EffectRecovering),
	EffectObserving:  set(EffectSucceeded, EffectFailed, EffectRecovering),
	EffectRecovering: set(EffectObserving),
	EffectSucceeded:  {},
	EffectFailed:     {},
}

func (s EffectState) CanTransition(to EffectState) bool { return effectTransitions.allows(s, to) }

func (s EffectState) Valid() bool {
	_, exists := effectTransitions[s]
	return exists
}

func ValidateEffectTransition(from, to EffectState) error {
	return effectTransitions.validate("effect", from, to)
}

type IdempotencyState string

const (
	IdempotencyInProgress IdempotencyState = "IN_PROGRESS"
	IdempotencyCompleted  IdempotencyState = "COMPLETED"
)

func (s IdempotencyState) Valid() bool {
	return s == IdempotencyInProgress || s == IdempotencyCompleted
}

type EvidenceState string

const (
	EvidenceCurrent    EvidenceState = "CURRENT"
	EvidenceStale      EvidenceState = "STALE"
	EvidenceSuperseded EvidenceState = "SUPERSEDED"
	EvidenceInvalid    EvidenceState = "INVALID"
)

func (s EvidenceState) Valid() bool {
	return s == EvidenceCurrent || s == EvidenceStale || s == EvidenceSuperseded || s == EvidenceInvalid
}

type FindingState string

const (
	FindingOpen                FindingState = "OPEN"
	FindingResolvedByPatch     FindingState = "RESOLVED_BY_PATCH"
	FindingDisprovedByEvidence FindingState = "DISPROVED_BY_EVIDENCE"
	FindingWaivedByHuman       FindingState = "WAIVED_BY_HUMAN"
	FindingSuperseded          FindingState = "SUPERSEDED"
)

var findingTransitions = transitions[FindingState]{
	FindingOpen:                set(FindingResolvedByPatch, FindingDisprovedByEvidence, FindingWaivedByHuman, FindingSuperseded),
	FindingResolvedByPatch:     {},
	FindingDisprovedByEvidence: {},
	FindingWaivedByHuman:       {},
	FindingSuperseded:          {},
}

func (s FindingState) Valid() bool {
	_, exists := findingTransitions[s]
	return exists
}

func ValidateFindingTransition(from, to FindingState) error {
	return findingTransitions.validate("review finding", from, to)
}

type transitions[T comparable] map[T]map[T]struct{}

func (t transitions[T]) allows(from, to T) bool {
	targets, exists := t[from]
	if !exists {
		return false
	}
	_, exists = targets[to]
	return exists
}

func (t transitions[T]) validate(kind string, from, to T) error {
	if _, exists := t[from]; !exists {
		return fmt.Errorf("unknown %s source state %v", kind, from)
	}
	if _, exists := t[to]; !exists {
		return fmt.Errorf("unknown %s target state %v", kind, to)
	}
	if !t.allows(from, to) {
		return fmt.Errorf("invalid %s transition %v -> %v", kind, from, to)
	}
	return nil
}

func set[T comparable](values ...T) map[T]struct{} {
	result := make(map[T]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
