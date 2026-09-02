package domain

import "time"

type Goal struct {
	ID                 string
	State              GoalState
	ActiveRevisionID   string
	FinalTree          string
	FinalEvidenceSetID string
	FinalReportHash    string
	Version            int64
}

type GoalRevision struct {
	ID           string
	GoalID       string
	Revision     int64
	RawGoal      string
	ContractJSON []byte
	Hash         string
	FrozenAt     time.Time
}

type PlanRevision struct {
	ID             string
	GoalRevisionID string
	Revision       int64
	GraphHash      string
	Status         PlanRevisionState
	Version        int64
}

type WorkItem struct {
	ID                 string
	PlanRevisionID     string
	State              WorkState
	Title              string
	Objective          string
	ReadScope          []string
	WriteScope         []string
	AcceptanceCriteria []string
	ValidatorIDs       []string
	RecommendedRole    Role
	Required           bool
	Version            int64
}

type WorkDependency struct {
	FromID string
	ToID   string
	Type   DependencyType
}

type Attempt struct {
	ID             string
	WorkItemID     string
	AgentProfileID string
	State          AttemptState
	BaseTree       string
	ResultTree     string
	PacketHash     string
	ResultKind     string
	Version        int64
}

type Lease struct {
	ID          string
	WorkItemID  string
	AttemptID   string
	Holder      string
	Generation  int64
	State       LeaseState
	AcquiredAt  time.Time
	HeartbeatAt time.Time
	ExpiresAt   time.Time
	Version     int64
}

type Gate struct {
	ID             string
	GoalID         string
	WorkItemID     string
	AttemptID      string
	ReasonCode     string
	State          GateState
	FactsJSON      []byte
	UnknownsJSON   []byte
	OptionsJSON    []byte
	Recommendation string
	Action         PolicyAction
	Scope          []string
	ExpiresAt      time.Time
	MaxUses        int64
	Used           int64
	Revocable      bool
	Required       bool
	Decision       GateDecision
	DecidedBy      string
	DecisionReason string
	DecidedAt      time.Time
	Version        int64
}

type Effect struct {
	ID              string
	Key             string
	Type            string
	State           EffectState
	RequestJSON     []byte
	RequestHash     string
	ObservationJSON []byte
	ObservationHash string
	Version         int64
}

type IdempotencyRecord struct {
	Scope          string
	Key            string
	RequestJSON    []byte
	RequestHash    string
	State          IdempotencyState
	ResponseStatus int
	ResponseJSON   []byte
	ResponseHash   string
	CreatedAt      time.Time
	CompletedAt    time.Time
}

type Evidence struct {
	ID               string
	Kind             string
	SubjectID        string
	Authority        Authority
	GoalRevisionHash string
	ConfigHash       string
	TreeHash         string
	PayloadHash      string
	State            EvidenceState
}

type Event struct {
	ID            string
	AggregateType string
	AggregateID   string
	Sequence      int64
	EventType     string
	ActorType     string
	ActorID       string
	CorrelationID string
	Payload       []byte
	CreatedAt     time.Time
}
