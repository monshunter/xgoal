package domain

import "time"

type Goal struct {
	ID               string
	State            GoalState
	ActiveRevisionID string
	Version          int64
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

type WorkItem struct {
	ID             string
	PlanRevisionID string
	State          WorkState
	Objective      string
	Required       bool
	Version        int64
}

type Attempt struct {
	ID             string
	WorkItemID     string
	AgentProfileID string
	State          AttemptState
	BaseTree       string
	ResultTree     string
	PacketHash     string
	Version        int64
}

type Lease struct {
	ID         string
	WorkItemID string
	AttemptID  string
	Holder     string
	Generation int64
	ExpiresAt  time.Time
	Active     bool
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
