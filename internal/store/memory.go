package store

import (
	"errors"
	"fmt"
	"sync"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
)

var (
	ErrAlreadyExists       = errors.New("already exists")
	ErrNotFound            = errors.New("not found")
	ErrConflict            = errors.New("version conflict")
	ErrActiveLease         = errors.New("active lease exists")
	ErrIdempotencyConflict = errors.New("idempotency key conflict")
	ErrExpired             = errors.New("expired")
	ErrStaleLease          = errors.New("stale lease generation")
	ErrAuthorizationDenied = errors.New("authorization denied")
)

type Memory struct {
	mu           sync.RWMutex
	clock        clock.Clock
	goals        map[string]domain.Goal
	workItems    map[string]domain.WorkItem
	attempts     map[string]domain.Attempt
	activeLeases map[string]domain.Lease
	evidence     map[string]domain.Evidence
	events       map[string][]domain.Event
	nextEventID  int64
}

func NewMemory(source clock.Clock) *Memory {
	return &Memory{
		clock:        source,
		goals:        make(map[string]domain.Goal),
		workItems:    make(map[string]domain.WorkItem),
		attempts:     make(map[string]domain.Attempt),
		activeLeases: make(map[string]domain.Lease),
		evidence:     make(map[string]domain.Evidence),
		events:       make(map[string][]domain.Event),
	}
}

func (m *Memory) CreateGoal(goal domain.Goal, eventType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if goal.ID == "" || goal.Version <= 0 {
		return fmt.Errorf("invalid goal")
	}
	if _, exists := m.goals[goal.ID]; exists {
		return fmt.Errorf("goal %q: %w", goal.ID, ErrAlreadyExists)
	}
	m.goals[goal.ID] = goal
	m.appendEvent("goal", goal.ID, eventType)
	return nil
}

func (m *Memory) Goal(id string) (domain.Goal, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	goal, exists := m.goals[id]
	if !exists {
		return domain.Goal{}, fmt.Errorf("goal %q: %w", id, ErrNotFound)
	}
	return goal, nil
}

func (m *Memory) UpdateGoalState(id string, expectedVersion int64, state domain.GoalState, eventType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	goal, exists := m.goals[id]
	if !exists {
		return fmt.Errorf("goal %q: %w", id, ErrNotFound)
	}
	if goal.Version != expectedVersion {
		return fmt.Errorf("goal %q: %w", id, ErrConflict)
	}
	if err := domain.ValidateGoalTransition(goal.State, state); err != nil {
		return err
	}
	goal.State = state
	goal.Version++
	m.goals[id] = goal
	m.appendEvent("goal", id, eventType)
	return nil
}

func (m *Memory) CreateWorkItem(work domain.WorkItem, eventType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if work.ID == "" || work.Version <= 0 {
		return fmt.Errorf("invalid work item")
	}
	if _, exists := m.workItems[work.ID]; exists {
		return fmt.Errorf("work item %q: %w", work.ID, ErrAlreadyExists)
	}
	m.workItems[work.ID] = work
	m.appendEvent("work", work.ID, eventType)
	return nil
}

func (m *Memory) WorkItem(id string) (domain.WorkItem, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	work, exists := m.workItems[id]
	if !exists {
		return domain.WorkItem{}, fmt.Errorf("work item %q: %w", id, ErrNotFound)
	}
	return work, nil
}

func (m *Memory) UpdateWorkState(id string, expectedVersion int64, state domain.WorkState, eventType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	work, exists := m.workItems[id]
	if !exists {
		return fmt.Errorf("work item %q: %w", id, ErrNotFound)
	}
	if work.Version != expectedVersion {
		return fmt.Errorf("work item %q: %w", id, ErrConflict)
	}
	if err := domain.ValidateWorkTransition(work.State, state); err != nil {
		return err
	}
	work.State = state
	work.Version++
	m.workItems[id] = work
	m.appendEvent("work", id, eventType)
	return nil
}

func (m *Memory) ClaimWork(workID string, expectedVersion int64, lease domain.Lease, attempt domain.Attempt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	work, exists := m.workItems[workID]
	if !exists {
		return fmt.Errorf("work item %q: %w", workID, ErrNotFound)
	}
	if work.Version != expectedVersion {
		return fmt.Errorf("work item %q: %w", workID, ErrConflict)
	}
	if existing, exists := m.activeLeases[workID]; exists && existing.State == domain.LeaseActive {
		return fmt.Errorf("work item %q: %w", workID, ErrActiveLease)
	}
	if err := domain.ValidateWorkTransition(work.State, domain.WorkClaimed); err != nil {
		return err
	}
	if lease.ID == "" || lease.WorkItemID != workID || lease.AttemptID != attempt.ID || lease.State != domain.LeaseActive || lease.Generation <= 0 {
		return fmt.Errorf("invalid lease")
	}
	if attempt.ID == "" || attempt.WorkItemID != workID || attempt.State != domain.AttemptCreated || attempt.Version <= 0 {
		return fmt.Errorf("invalid attempt")
	}
	if _, exists := m.attempts[attempt.ID]; exists {
		return fmt.Errorf("attempt %q: %w", attempt.ID, ErrAlreadyExists)
	}
	work.State = domain.WorkClaimed
	work.Version++
	m.workItems[workID] = work
	m.activeLeases[workID] = lease
	m.attempts[attempt.ID] = attempt
	m.appendEvent("work", workID, "LeaseAcquired")
	m.appendEvent("attempt", attempt.ID, "AttemptCreated")
	return nil
}

func (m *Memory) Attempt(id string) (domain.Attempt, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	attempt, exists := m.attempts[id]
	if !exists {
		return domain.Attempt{}, fmt.Errorf("attempt %q: %w", id, ErrNotFound)
	}
	return attempt, nil
}

func (m *Memory) UpdateAttemptState(id string, expectedVersion int64, state domain.AttemptState, eventType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	attempt, exists := m.attempts[id]
	if !exists {
		return fmt.Errorf("attempt %q: %w", id, ErrNotFound)
	}
	if attempt.Version != expectedVersion {
		return fmt.Errorf("attempt %q: %w", id, ErrConflict)
	}
	if err := domain.ValidateAttemptTransition(attempt.State, state); err != nil {
		return err
	}
	attempt.State = state
	attempt.Version++
	m.attempts[id] = attempt
	m.appendEvent("attempt", id, eventType)
	return nil
}

func (m *Memory) ReleaseLease(workID string, generation int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	lease, exists := m.activeLeases[workID]
	if !exists || lease.State != domain.LeaseActive {
		return fmt.Errorf("work item %q lease: %w", workID, ErrNotFound)
	}
	if lease.Generation != generation {
		return fmt.Errorf("work item %q lease: %w", workID, ErrConflict)
	}
	lease.State = domain.LeaseReleased
	delete(m.activeLeases, workID)
	m.appendEvent("work", workID, "LeaseReleased")
	return nil
}

func (m *Memory) AddEvidence(evidence domain.Evidence, eventType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if evidence.ID == "" {
		return fmt.Errorf("invalid evidence")
	}
	if _, exists := m.evidence[evidence.ID]; exists {
		return fmt.Errorf("evidence %q: %w", evidence.ID, ErrAlreadyExists)
	}
	m.evidence[evidence.ID] = evidence
	m.appendEvent("evidence", evidence.ID, eventType)
	return nil
}

func (m *Memory) Evidence(id string) (domain.Evidence, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	evidence, exists := m.evidence[id]
	if !exists {
		return domain.Evidence{}, fmt.Errorf("evidence %q: %w", id, ErrNotFound)
	}
	return evidence, nil
}

func (m *Memory) Events(aggregateType, aggregateID string) []domain.Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := aggregateType + "/" + aggregateID
	return append([]domain.Event(nil), m.events[key]...)
}

func (m *Memory) appendEvent(aggregateType, aggregateID, eventType string) {
	m.nextEventID++
	key := aggregateType + "/" + aggregateID
	m.events[key] = append(m.events[key], domain.Event{
		ID:            fmt.Sprintf("event_%d", m.nextEventID),
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		Sequence:      int64(len(m.events[key]) + 1),
		EventType:     eventType,
		ActorType:     "kernel",
		CreatedAt:     m.clock.Now(),
	})
}
