package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

// LeaseDraft describes bounded ownership requested during an atomic Work claim.
type LeaseDraft struct {
	ID     string
	Holder string
	TTL    time.Duration
}

// GateDraft describes the complete, bounded authorization question shown to a human.
type GateDraft struct {
	ID             string
	GoalID         string
	WorkItemID     string
	AttemptID      string
	ReasonCode     string
	Facts          any
	Unknowns       any
	Options        any
	Recommendation string
	Action         domain.PolicyAction
	Scope          []string
	ExpiresAt      time.Time
	MaxUses        int64
	Revocable      bool
	Required       bool
}

// ClaimWork atomically CASes READY Work, creates its Attempt and acquires one Lease.
func (s *Store) ClaimWork(
	ctx context.Context,
	workID string,
	expectedWorkVersion int64,
	leaseDraft LeaseDraft,
	attempt domain.Attempt,
	event EventInput,
) (domain.Lease, error) {
	if !validIdempotencyLabel(workID) || expectedWorkVersion <= 0 ||
		!validIdempotencyLabel(leaseDraft.ID) || !validIdempotencyLabel(leaseDraft.Holder) || leaseDraft.TTL <= 0 {
		return domain.Lease{}, errors.New("invalid work claim or lease")
	}
	if !validIdempotencyLabel(attempt.ID) || attempt.WorkItemID != workID || !validIdempotencyLabel(attempt.AgentProfileID) ||
		attempt.State != domain.AttemptCreated || attempt.Version != 1 || strings.TrimSpace(attempt.BaseTree) == "" || strings.TrimSpace(attempt.PacketHash) == "" {
		return domain.Lease{}, errors.New("invalid initial attempt")
	}
	preparedWorkEvent, err := prepareEvent(event)
	if err != nil {
		return domain.Lease{}, err
	}
	preparedAttemptEvent, err := prepareEvent(EventInput{
		Type:          "AttemptCreated",
		ActorType:     event.ActorType,
		ActorID:       event.ActorID,
		CorrelationID: event.CorrelationID,
		Payload:       map[string]any{"work_item_id": workID},
	})
	if err != nil {
		return domain.Lease{}, err
	}
	preparedLeaseEvent, err := prepareEvent(EventInput{
		Type:          "LeaseAcquired",
		ActorType:     event.ActorType,
		ActorID:       event.ActorID,
		CorrelationID: event.CorrelationID,
		Payload:       map[string]any{"attempt_id": attempt.ID},
	})
	if err != nil {
		return domain.Lease{}, err
	}

	var lease domain.Lease
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		work, err := readWorkItem(ctx, tx, workID)
		if err != nil {
			return err
		}
		if work.Version != expectedWorkVersion {
			return fmt.Errorf("work item %q: %w", workID, basestore.ErrConflict)
		}
		if err := domain.ValidateWorkTransition(work.State, domain.WorkClaimed); err != nil {
			return err
		}
		if err := ensureWorkCanBecomeReady(ctx, tx, work); err != nil {
			return err
		}
		var activeLeases int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM leases
WHERE state = ?`, domain.LeaseActive).Scan(&activeLeases); err != nil {
			return fmt.Errorf("check project execution slot for work %q: %w", workID, err)
		}
		if activeLeases != 0 {
			return fmt.Errorf("project execution slot for work item %q: %w", workID, basestore.ErrActiveLease)
		}
		if _, err := readAttempt(ctx, tx, attempt.ID); err == nil {
			return fmt.Errorf("attempt %q: %w", attempt.ID, basestore.ErrAlreadyExists)
		} else if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		if _, err := readLease(ctx, tx, leaseDraft.ID); err == nil {
			return fmt.Errorf("lease %q: %w", leaseDraft.ID, basestore.ErrAlreadyExists)
		} else if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		var generation int64
		if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(generation), 0) + 1
FROM leases
WHERE work_item_id = ?`, workID).Scan(&generation); err != nil {
			return fmt.Errorf("allocate lease generation for work %q: %w", workID, err)
		}
		now := s.source.Now().UTC()
		if err := insertAttempt(ctx, tx, attempt, now); err != nil {
			return err
		}
		lease = domain.Lease{
			ID:          leaseDraft.ID,
			WorkItemID:  workID,
			AttemptID:   attempt.ID,
			Holder:      leaseDraft.Holder,
			Generation:  generation,
			State:       domain.LeaseActive,
			AcquiredAt:  now,
			HeartbeatAt: now,
			ExpiresAt:   now.Add(leaseDraft.TTL),
			Version:     1,
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO leases(
    id, work_item_id, attempt_id, holder, generation, state,
    acquired_at, heartbeat_at, expires_at, version
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			lease.ID,
			lease.WorkItemID,
			lease.AttemptID,
			lease.Holder,
			lease.Generation,
			lease.State,
			lease.AcquiredAt.Format(time.RFC3339Nano),
			lease.HeartbeatAt.Format(time.RFC3339Nano),
			lease.ExpiresAt.Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert lease %q: %w", lease.ID, err)
		}
		updated, err := tx.ExecContext(ctx, `
UPDATE work_items
SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ? AND state = ?`,
			domain.WorkClaimed,
			now.Format(time.RFC3339Nano),
			workID,
			expectedWorkVersion,
			domain.WorkReady,
		)
		if err != nil {
			return fmt.Errorf("claim work item %q: %w", workID, err)
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return fmt.Errorf("read work claim result: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("work item %q: %w", workID, basestore.ErrConflict)
		}
		if err := s.appendEvent(ctx, tx, "work", workID, preparedWorkEvent); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "attempt", attempt.ID, preparedAttemptEvent); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "lease", lease.ID, preparedLeaseEvent)
	})
	if err != nil {
		return domain.Lease{}, err
	}
	return lease, nil
}

// Attempt returns one persisted agent invocation.
func (s *Store) Attempt(ctx context.Context, id string) (domain.Attempt, error) {
	if id == "" {
		return domain.Attempt{}, errors.New("attempt id is empty")
	}
	return readAttempt(ctx, s.db, id)
}

func insertAttempt(ctx context.Context, tx *sql.Tx, attempt domain.Attempt, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO attempts(
    id, work_item_id, agent_profile_id, state, base_tree, result_tree,
    packet_hash, result_kind, version, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.ID,
		attempt.WorkItemID,
		attempt.AgentProfileID,
		attempt.State,
		attempt.BaseTree,
		attempt.ResultTree,
		attempt.PacketHash,
		attempt.ResultKind,
		attempt.Version,
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("insert attempt %q: %w", attempt.ID, err)
	}
	return nil
}

func readAttempt(ctx context.Context, queryer rowQueryer, id string) (domain.Attempt, error) {
	var attempt domain.Attempt
	err := queryer.QueryRowContext(ctx, `
SELECT id, work_item_id, agent_profile_id, state, base_tree, result_tree,
       packet_hash, result_kind, version
FROM attempts
WHERE id = ?`, id).Scan(
		&attempt.ID,
		&attempt.WorkItemID,
		&attempt.AgentProfileID,
		&attempt.State,
		&attempt.BaseTree,
		&attempt.ResultTree,
		&attempt.PacketHash,
		&attempt.ResultKind,
		&attempt.Version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Attempt{}, fmt.Errorf("attempt %q: %w", id, basestore.ErrNotFound)
	}
	if err != nil {
		return domain.Attempt{}, fmt.Errorf("read attempt %q: %w", id, err)
	}
	if !attempt.State.Valid() || attempt.Version <= 0 {
		return domain.Attempt{}, fmt.Errorf("attempt %q contains invalid persisted state", id)
	}
	return attempt, nil
}

// Lease returns one persisted ownership record.
func (s *Store) Lease(ctx context.Context, id string) (domain.Lease, error) {
	if id == "" {
		return domain.Lease{}, errors.New("lease id is empty")
	}
	return readLease(ctx, s.db, id)
}

func readLease(ctx context.Context, queryer rowQueryer, id string) (domain.Lease, error) {
	lease, err := scanLease(queryer.QueryRowContext(ctx, `
SELECT id, work_item_id, attempt_id, holder, generation, state,
       acquired_at, heartbeat_at, expires_at, version
FROM leases
WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Lease{}, fmt.Errorf("lease %q: %w", id, basestore.ErrNotFound)
	}
	if err != nil {
		return domain.Lease{}, fmt.Errorf("read lease %q: %w", id, err)
	}
	return lease, nil
}

// CreateGate persists a bounded authorization request and its opening Event atomically.
func (s *Store) CreateGate(ctx context.Context, draft GateDraft, event EventInput) (domain.Gate, error) {
	if !validIdempotencyLabel(draft.ID) || !validIdempotencyLabel(draft.GoalID) || !validIdempotencyLabel(draft.ReasonCode) ||
		strings.TrimSpace(draft.Recommendation) == "" || !draft.Action.Valid() || draft.MaxUses <= 0 || len(draft.Scope) == 0 {
		return domain.Gate{}, errors.New("invalid gate draft")
	}
	now := s.source.Now().UTC()
	if !draft.ExpiresAt.After(now) {
		return domain.Gate{}, fmt.Errorf("gate expiry: %w", basestore.ErrExpired)
	}
	if err := validateUniqueStrings(draft.Scope, "gate scope"); err != nil {
		return domain.Gate{}, err
	}
	factsJSON, err := canonical.Marshal(draft.Facts)
	if err != nil {
		return domain.Gate{}, fmt.Errorf("canonicalize gate facts: %w", err)
	}
	unknownsJSON, err := canonical.Marshal(draft.Unknowns)
	if err != nil {
		return domain.Gate{}, fmt.Errorf("canonicalize gate unknowns: %w", err)
	}
	optionsJSON, err := canonical.Marshal(draft.Options)
	if err != nil {
		return domain.Gate{}, fmt.Errorf("canonicalize gate options: %w", err)
	}
	scope := sortedCopy(draft.Scope)
	scopeJSON, err := canonical.Marshal(scope)
	if err != nil {
		return domain.Gate{}, fmt.Errorf("canonicalize gate scope: %w", err)
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return domain.Gate{}, err
	}
	var gate domain.Gate
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		if _, err := readGate(ctx, tx, draft.ID); err == nil {
			return fmt.Errorf("gate %q: %w", draft.ID, basestore.ErrAlreadyExists)
		} else if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		if _, err := readGoal(ctx, tx, draft.GoalID); err != nil {
			return err
		}
		if draft.WorkItemID != "" {
			work, err := readWorkItem(ctx, tx, draft.WorkItemID)
			if err != nil {
				return err
			}
			var workGoalID string
			if err := tx.QueryRowContext(ctx, `
SELECT goal_revision.goal_id
FROM plan_revisions plan
JOIN goal_revisions goal_revision ON goal_revision.id = plan.goal_revision_id
WHERE plan.id = ?`, work.PlanRevisionID).Scan(&workGoalID); err != nil {
				return fmt.Errorf("resolve work item %q goal: %w", work.ID, err)
			}
			if workGoalID != draft.GoalID {
				return errors.New("gate work item belongs to another goal")
			}
		}
		if draft.AttemptID != "" {
			if draft.WorkItemID == "" {
				return errors.New("gate attempt requires a work item")
			}
			attempt, err := readAttempt(ctx, tx, draft.AttemptID)
			if err != nil {
				return err
			}
			if attempt.WorkItemID != draft.WorkItemID {
				return errors.New("gate attempt belongs to another work item")
			}
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO gates(
    id, goal_id, work_item_id, attempt_id, reason_code, state,
    facts_json, unknowns_json, options_json, recommendation,
    action, scope_json, expires_at, max_uses, used, revocable, required,
    version, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, 1, ?, ?)`,
			draft.ID,
			draft.GoalID,
			nullableString(draft.WorkItemID),
			nullableString(draft.AttemptID),
			draft.ReasonCode,
			domain.GateOpen,
			factsJSON,
			unknownsJSON,
			optionsJSON,
			draft.Recommendation,
			draft.Action,
			scopeJSON,
			draft.ExpiresAt.UTC().Format(time.RFC3339Nano),
			draft.MaxUses,
			boolInteger(draft.Revocable),
			boolInteger(draft.Required),
			now.Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert gate %q: %w", draft.ID, err)
		}
		if err := s.appendEvent(ctx, tx, "gate", draft.ID, prepared); err != nil {
			return err
		}
		gate = domain.Gate{
			ID:             draft.ID,
			GoalID:         draft.GoalID,
			WorkItemID:     draft.WorkItemID,
			AttemptID:      draft.AttemptID,
			ReasonCode:     draft.ReasonCode,
			State:          domain.GateOpen,
			FactsJSON:      append([]byte(nil), factsJSON...),
			UnknownsJSON:   append([]byte(nil), unknownsJSON...),
			OptionsJSON:    append([]byte(nil), optionsJSON...),
			Recommendation: draft.Recommendation,
			Action:         draft.Action,
			Scope:          scope,
			ExpiresAt:      draft.ExpiresAt.UTC(),
			MaxUses:        draft.MaxUses,
			Revocable:      draft.Revocable,
			Required:       draft.Required,
			Version:        1,
		}
		return nil
	})
	if err != nil {
		return domain.Gate{}, err
	}
	return gate, nil
}

// Gate returns one persisted bounded authorization request.
func (s *Store) Gate(ctx context.Context, id string) (domain.Gate, error) {
	if id == "" {
		return domain.Gate{}, errors.New("gate id is empty")
	}
	return readGate(ctx, s.db, id)
}

// DecideGate records one scoped human decision with CAS and an Event.
func (s *Store) DecideGate(
	ctx context.Context,
	id string,
	expectedVersion int64,
	decision domain.GateDecision,
	decidedBy, reason string,
	event EventInput,
) (domain.Gate, error) {
	if id == "" || expectedVersion <= 0 || !decision.Valid() || !validIdempotencyLabel(decidedBy) || strings.TrimSpace(reason) == "" {
		return domain.Gate{}, errors.New("invalid gate decision")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return domain.Gate{}, err
	}
	var result domain.Gate
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		gate, err := readGate(ctx, tx, id)
		if err != nil {
			return err
		}
		if gate.Version != expectedVersion {
			return fmt.Errorf("gate %q: %w", id, basestore.ErrConflict)
		}
		if !s.source.Now().UTC().Before(gate.ExpiresAt) {
			return fmt.Errorf("gate %q: %w", id, basestore.ErrExpired)
		}
		targetState := domain.GateDenied
		if decision == domain.GateAllow {
			targetState = domain.GateApproved
		}
		if err := domain.ValidateGateTransition(gate.State, targetState); err != nil {
			return err
		}
		decidedAt := s.source.Now().UTC()
		updated, err := tx.ExecContext(ctx, `
UPDATE gates
SET state = ?, decision = ?, decided_by = ?, decision_reason = ?,
    decided_at = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ? AND state = ?`,
			targetState,
			decision,
			decidedBy,
			reason,
			decidedAt.Format(time.RFC3339Nano),
			decidedAt.Format(time.RFC3339Nano),
			id,
			expectedVersion,
			domain.GateOpen,
		)
		if err != nil {
			return fmt.Errorf("decide gate %q: %w", id, err)
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return fmt.Errorf("read gate decision result: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("gate %q: %w", id, basestore.ErrConflict)
		}
		if err := s.appendEvent(ctx, tx, "gate", id, prepared); err != nil {
			return err
		}
		gate.State = targetState
		gate.Decision = decision
		gate.DecidedBy = decidedBy
		gate.DecisionReason = reason
		gate.DecidedAt = decidedAt
		gate.Version++
		result = gate
		return nil
	})
	if err != nil {
		return domain.Gate{}, err
	}
	return result, nil
}

func readGate(ctx context.Context, queryer rowQueryer, id string) (domain.Gate, error) {
	var gate domain.Gate
	var workItemID, attemptID, decision, decidedBy, decisionReason, decidedAt sql.NullString
	var scopeJSON []byte
	var expiresAt string
	var revocable, required int
	err := queryer.QueryRowContext(ctx, `
SELECT id, goal_id, work_item_id, attempt_id, reason_code, state,
       facts_json, unknowns_json, options_json, recommendation,
       action, scope_json, expires_at, max_uses, used, revocable, required,
       decision, decided_by, decision_reason, decided_at, version
FROM gates
WHERE id = ?`, id).Scan(
		&gate.ID,
		&gate.GoalID,
		&workItemID,
		&attemptID,
		&gate.ReasonCode,
		&gate.State,
		&gate.FactsJSON,
		&gate.UnknownsJSON,
		&gate.OptionsJSON,
		&gate.Recommendation,
		&gate.Action,
		&scopeJSON,
		&expiresAt,
		&gate.MaxUses,
		&gate.Used,
		&revocable,
		&required,
		&decision,
		&decidedBy,
		&decisionReason,
		&decidedAt,
		&gate.Version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Gate{}, fmt.Errorf("gate %q: %w", id, basestore.ErrNotFound)
	}
	if err != nil {
		return domain.Gate{}, fmt.Errorf("read gate %q: %w", id, err)
	}
	gate.WorkItemID = workItemID.String
	gate.AttemptID = attemptID.String
	gate.Decision = domain.GateDecision(decision.String)
	gate.DecidedBy = decidedBy.String
	gate.DecisionReason = decisionReason.String
	gate.Revocable = revocable == 1
	gate.Required = required == 1
	if err := json.Unmarshal(scopeJSON, &gate.Scope); err != nil {
		return domain.Gate{}, fmt.Errorf("decode gate %q scope: %w", id, err)
	}
	gate.ExpiresAt, err = parseStoredTime("gate expires_at", expiresAt)
	if err != nil {
		return domain.Gate{}, err
	}
	if decidedAt.Valid {
		gate.DecidedAt, err = parseStoredTime("gate decided_at", decidedAt.String)
		if err != nil {
			return domain.Gate{}, err
		}
	}
	if !gate.State.Valid() || !gate.Action.Valid() || gate.Version <= 0 || gate.MaxUses <= 0 || gate.Used < 0 || gate.Used > gate.MaxUses ||
		(revocable != 0 && revocable != 1) || (required != 0 && required != 1) {
		return domain.Gate{}, fmt.Errorf("gate %q contains invalid persisted state", id)
	}
	return gate, nil
}

func parseStoredTime(label, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s: %w", label, err)
	}
	return parsed, nil
}
