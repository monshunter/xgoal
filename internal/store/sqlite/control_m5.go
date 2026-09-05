package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/policy"
	"github.com/monshunter/xgoal/internal/reconcile"
	basestore "github.com/monshunter/xgoal/internal/store"
)

type AuthorizationRequest struct {
	GoalID     string
	WorkItemID string
	AttemptID  string
	Action     domain.PolicyAction
	Scope      []string
}

// Gates returns gates for one goal in deterministic creation order.
func (s *Store) Gates(ctx context.Context, goalID string, onlyOpen bool) ([]domain.Gate, error) {
	if !validIdempotencyLabel(goalID) {
		return nil, errors.New("goal id is required")
	}
	query := `SELECT id FROM gates WHERE goal_id = ?`
	arguments := []any{goalID}
	if onlyOpen {
		query += ` AND state = ?`
		arguments = append(arguments, domain.GateOpen)
	}
	query += ` ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list gates for goal %q: %w", goalID, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan gate id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate gate ids: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close gate ids: %w", err)
	}
	result := make([]domain.Gate, 0, len(ids))
	for _, id := range ids {
		gate, err := s.Gate(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, gate)
	}
	return result, nil
}

// ConsumeAuthorization atomically consumes one approved finite authorization.
func (s *Store) ConsumeAuthorization(ctx context.Context, gateID string, request AuthorizationRequest, event EventInput) (domain.Gate, error) {
	if !validIdempotencyLabel(gateID) || !validIdempotencyLabel(request.GoalID) || !request.Action.Valid() || len(request.Scope) == 0 {
		return domain.Gate{}, errors.New("invalid authorization request")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return domain.Gate{}, err
	}
	expiredEvent, err := prepareEvent(EventInput{Type: "GateExpired", ActorType: "kernel", CorrelationID: event.CorrelationID, Payload: map[string]any{"requested_action": request.Action}})
	if err != nil {
		return domain.Gate{}, err
	}
	var result domain.Gate
	expired := false
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		gate, err := readGate(ctx, tx, gateID)
		if err != nil {
			return err
		}
		now := s.source.Now().UTC()
		if !now.Before(gate.ExpiresAt) {
			if gate.Used == 0 && (gate.State == domain.GateOpen || gate.State == domain.GateApproved) {
				updated, updateErr := tx.ExecContext(ctx, `UPDATE gates SET state = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ?`, domain.GateExpired, now.Format(time.RFC3339Nano), gate.ID, gate.Version)
				if updateErr != nil {
					return fmt.Errorf("expire gate %q: %w", gate.ID, updateErr)
				}
				affected, updateErr := updated.RowsAffected()
				if updateErr != nil || affected != 1 {
					return fmt.Errorf("gate %q expiry: %w", gate.ID, basestore.ErrConflict)
				}
				if err := s.appendEvent(ctx, tx, "gate", gate.ID, expiredEvent); err != nil {
					return err
				}
				gate.State, gate.Version = domain.GateExpired, gate.Version+1
				result = gate
			}
			expired = true
			return nil
		}
		if gate.State != domain.GateApproved || gate.Decision != domain.GateAllow || gate.Used >= gate.MaxUses ||
			gate.GoalID != request.GoalID || gate.WorkItemID != request.WorkItemID || gate.AttemptID != request.AttemptID ||
			gate.Action != request.Action || !policy.ScopeContains(gate.Scope, request.Scope) {
			return fmt.Errorf("gate %q: %w", gate.ID, basestore.ErrAuthorizationDenied)
		}
		updated, err := tx.ExecContext(ctx, `
UPDATE gates
SET used = used + 1, version = version + 1, updated_at = ?
WHERE id = ? AND version = ? AND state = ? AND used < max_uses`, now.Format(time.RFC3339Nano), gate.ID, gate.Version, domain.GateApproved)
		if err != nil {
			return fmt.Errorf("consume gate %q: %w", gate.ID, err)
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return fmt.Errorf("read gate consumption: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("gate %q: %w", gate.ID, basestore.ErrConflict)
		}
		if err := s.appendEvent(ctx, tx, "gate", gate.ID, prepared); err != nil {
			return err
		}
		gate.Used++
		gate.Version++
		result = gate
		return nil
	})
	if err != nil {
		return domain.Gate{}, err
	}
	if expired {
		return result, fmt.Errorf("gate %q: %w", gateID, basestore.ErrExpired)
	}
	return result, nil
}

// RevokeGate revokes a revocable approved authorization with CAS.
func (s *Store) RevokeGate(ctx context.Context, id string, expectedVersion int64, actor, reason string, event EventInput) (domain.Gate, error) {
	if !validIdempotencyLabel(id) || expectedVersion <= 0 || !validIdempotencyLabel(actor) || strings.TrimSpace(reason) == "" {
		return domain.Gate{}, errors.New("invalid gate revocation")
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
		if !gate.Revocable || gate.State != domain.GateApproved {
			return fmt.Errorf("gate %q: %w", id, basestore.ErrAuthorizationDenied)
		}
		now := s.source.Now().UTC()
		updated, err := tx.ExecContext(ctx, `UPDATE gates SET state = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND state = ?`, domain.GateRevoked, now.Format(time.RFC3339Nano), id, expectedVersion, domain.GateApproved)
		if err != nil {
			return fmt.Errorf("revoke gate %q: %w", id, err)
		}
		affected, _ := updated.RowsAffected()
		if affected != 1 {
			return fmt.Errorf("gate %q: %w", id, basestore.ErrConflict)
		}
		if err := s.appendEvent(ctx, tx, "gate", id, prepared); err != nil {
			return err
		}
		gate.State = domain.GateRevoked
		gate.Version++
		result = gate
		return nil
	})
	return result, err
}

type FailureDraft struct {
	ID         string
	GoalID     string
	WorkItemID string
	AttemptID  string
	Failure    reconcile.Failure
	Strategy   string
	Previous   reconcile.Snapshot
	Current    reconcile.Snapshot
}

type FailureRecord struct {
	FailureDraft
	Fingerprint      string
	SnapshotHash     string
	MaterialProgress bool
	RepeatCount      int64
	CreatedAt        time.Time
}

// RecordFailure stores the normalized fingerprint and same-strategy repetition count.
func (s *Store) RecordFailure(ctx context.Context, draft FailureDraft, event EventInput) (FailureRecord, error) {
	if !validIdempotencyLabel(draft.ID) || !validIdempotencyLabel(draft.GoalID) || !validIdempotencyLabel(draft.Strategy) {
		return FailureRecord{}, errors.New("invalid failure identity")
	}
	fingerprint, err := reconcile.Fingerprint(draft.Failure)
	if err != nil {
		return FailureRecord{}, err
	}
	snapshotHash, err := reconcile.SnapshotHash(draft.Current)
	if err != nil {
		return FailureRecord{}, err
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return FailureRecord{}, err
	}
	record := FailureRecord{FailureDraft: draft, Fingerprint: fingerprint, SnapshotHash: snapshotHash, MaterialProgress: reconcile.MaterialProgress(draft.Previous, draft.Current)}
	record.Failure.PrimaryError = reconcile.NormalizeError(record.Failure.PrimaryError)
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		if _, err := readGoal(ctx, tx, draft.GoalID); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) + 1 FROM failure_records WHERE goal_id = ? AND fingerprint = ? AND strategy = ?`, draft.GoalID, fingerprint, draft.Strategy).Scan(&record.RepeatCount); err != nil {
			return fmt.Errorf("count repeated failure: %w", err)
		}
		record.CreatedAt = s.source.Now().UTC()
		if _, err := tx.ExecContext(ctx, `INSERT INTO failure_records(id, goal_id, work_item_id, attempt_id, failure_class, normalized_error, validator_definition_hash, base_tree, result_tree, goal_revision_hash, config_hash, fingerprint, strategy, snapshot_hash, material_progress, repeat_count, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, draft.ID, draft.GoalID, nullableString(draft.WorkItemID), nullableString(draft.AttemptID), record.Failure.Class, record.Failure.PrimaryError, record.Failure.ValidatorDefinitionHash, record.Failure.BaseTree, record.Failure.ResultTree, record.Failure.GoalRevisionHash, record.Failure.RelevantConfigHash, fingerprint, draft.Strategy, snapshotHash, boolInteger(record.MaterialProgress), record.RepeatCount, record.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("insert failure %q: %w", draft.ID, err)
		}
		return s.appendEvent(ctx, tx, "goal", draft.GoalID, prepared)
	})
	return record, err
}

type ReconcileRecord struct {
	ID           string
	FailureID    string
	Decision     reconcile.Decision
	PlanRevision int64
	CreatedAt    time.Time
}

func (s *Store) RecordReconcileDecision(ctx context.Context, record ReconcileRecord, event EventInput) (ReconcileRecord, error) {
	if !validIdempotencyLabel(record.ID) || !validIdempotencyLabel(record.FailureID) || record.Decision.Action == "" || strings.TrimSpace(record.Decision.Reason) == "" || record.PlanRevision < 0 {
		return ReconcileRecord{}, errors.New("invalid reconcile decision")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return ReconcileRecord{}, err
	}
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		var goalID string
		if err := tx.QueryRowContext(ctx, `SELECT goal_id FROM failure_records WHERE id = ?`, record.FailureID).Scan(&goalID); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failure %q: %w", record.FailureID, basestore.ErrNotFound)
		} else if err != nil {
			return err
		}
		record.CreatedAt = s.source.Now().UTC()
		if _, err := tx.ExecContext(ctx, `INSERT INTO reconcile_decisions(id, failure_id, action, reason, plan_revision, created_at) VALUES (?, ?, ?, ?, ?, ?)`, record.ID, record.FailureID, record.Decision.Action, record.Decision.Reason, record.PlanRevision, record.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("insert reconcile decision: %w", err)
		}
		return s.appendEvent(ctx, tx, "goal", goalID, prepared)
	})
	return record, err
}

// EventsAfter returns the global immutable stream after one unique event id.
func (s *Store) EventsAfter(ctx context.Context, afterID string, limit int) ([]domain.Event, error) {
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("event limit must be between 1 and 1000")
	}
	afterTime := ""
	if afterID != "" {
		if err := s.db.QueryRowContext(ctx, `SELECT created_at FROM events WHERE id = ?`, afterID).Scan(&afterTime); errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("event %q: %w", afterID, basestore.ErrNotFound)
		} else if err != nil {
			return nil, fmt.Errorf("read event cursor: %w", err)
		}
	}
	query := `SELECT id, aggregate_type, aggregate_id, sequence, event_type, actor_type, actor_id, correlation_id, payload_json, created_at FROM events`
	arguments := make([]any, 0, 3)
	if afterID != "" {
		query += ` WHERE created_at > ? OR (created_at = ? AND id > ?)`
		arguments = append(arguments, afterTime, afterTime, afterID)
	}
	query += ` ORDER BY created_at, id LIMIT ?`
	arguments = append(arguments, limit)
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("read global events: %w", err)
	}
	defer rows.Close()
	var result []domain.Event
	for rows.Next() {
		var item domain.Event
		var createdAt string
		if err := rows.Scan(&item.ID, &item.AggregateType, &item.AggregateID, &item.Sequence, &item.EventType, &item.ActorType, &item.ActorID, &item.CorrelationID, &item.Payload, &createdAt); err != nil {
			return nil, err
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// GoalEventsAfter returns one Goal's immutable aggregate stream after a stable Event ID.
func (s *Store) GoalEventsAfter(ctx context.Context, goalID, afterID string, limit int) ([]domain.Event, error) {
	if !validIdempotencyLabel(goalID) || limit <= 0 || limit > 1000 {
		return nil, errors.New("goal id and event limit between 1 and 1000 are required")
	}
	var afterSequence int64
	if afterID != "" {
		if err := s.db.QueryRowContext(ctx, `SELECT sequence FROM events WHERE id = ? AND aggregate_type = 'goal' AND aggregate_id = ?`, afterID, goalID).Scan(&afterSequence); errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("goal event %q: %w", afterID, basestore.ErrNotFound)
		} else if err != nil {
			return nil, fmt.Errorf("read goal event cursor: %w", err)
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, aggregate_type, aggregate_id, sequence, event_type, actor_type, actor_id, correlation_id, payload_json, created_at FROM events WHERE aggregate_type = 'goal' AND aggregate_id = ? AND sequence > ? ORDER BY sequence LIMIT ?`, goalID, afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("read goal events: %w", err)
	}
	defer rows.Close()
	var result []domain.Event
	for rows.Next() {
		var item domain.Event
		var createdAt string
		if err := rows.Scan(&item.ID, &item.AggregateType, &item.AggregateID, &item.Sequence, &item.EventType, &item.ActorType, &item.ActorID, &item.CorrelationID, &item.Payload, &createdAt); err != nil {
			return nil, err
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// GoalWorkItems returns Work Items from the active Goal/Plan revision.
func (s *Store) GoalWorkItems(ctx context.Context, goalID string) ([]domain.WorkItem, error) {
	if !validIdempotencyLabel(goalID) {
		return nil, errors.New("goal id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT work.id
FROM work_items work
JOIN plan_revisions plan ON plan.id = work.plan_revision_id
JOIN goal_revisions revision ON revision.id = plan.goal_revision_id
JOIN goals goal ON goal.id = revision.goal_id
WHERE goal.id = ? AND goal.active_revision_id = revision.id AND plan.status = ?
ORDER BY work.id`, goalID, domain.PlanActive)
	if err != nil {
		return nil, fmt.Errorf("list goal work items: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	items := make([]domain.WorkItem, 0, len(ids))
	for _, id := range ids {
		item, err := s.WorkItem(ctx, id)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// ActivateReplan supersedes the current active Plan and activates a prepared draft.
func (s *Store) ActivateReplan(ctx context.Context, id string, expectedPlanVersion, expectedGoalVersion int64, event EventInput) (domain.PlanRevision, error) {
	if !validIdempotencyLabel(id) || expectedPlanVersion <= 0 || expectedGoalVersion <= 0 {
		return domain.PlanRevision{}, errors.New("invalid replan activation")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return domain.PlanRevision{}, err
	}
	var result domain.PlanRevision
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		plan, err := readPlanRevision(ctx, tx, id)
		if err != nil {
			return err
		}
		if plan.Status != domain.PlanDraft || plan.Version != expectedPlanVersion {
			return fmt.Errorf("plan revision %q: %w", id, basestore.ErrConflict)
		}
		revision, err := readGoalRevision(ctx, tx, plan.GoalRevisionID)
		if err != nil {
			return err
		}
		goal, err := readGoal(ctx, tx, revision.GoalID)
		if err != nil {
			return err
		}
		if goal.Version != expectedGoalVersion || goal.ActiveRevisionID != revision.ID || (goal.State != domain.GoalRunning && goal.State != domain.GoalWaiting) {
			return fmt.Errorf("goal %q: %w", goal.ID, basestore.ErrConflict)
		}
		checkout, checkoutErr := readCheckout(ctx, tx)
		if checkoutErr == nil && checkout.GoalID == goal.ID && checkout.WorkID != "" {
			return fmt.Errorf("resolve the current checkout scene before replanning: %w", ErrCheckoutConflict)
		}
		if checkoutErr != nil && !errors.Is(checkoutErr, basestore.ErrNotFound) {
			return checkoutErr
		}
		var activeLeases int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM leases lease
JOIN work_items work ON work.id = lease.work_item_id
JOIN plan_revisions active_plan ON active_plan.id = work.plan_revision_id
WHERE active_plan.goal_revision_id = ? AND active_plan.status = ? AND lease.state = ?`,
			revision.ID, domain.PlanActive, domain.LeaseActive,
		).Scan(&activeLeases); err != nil {
			return fmt.Errorf("check active plan leases: %w", err)
		}
		if activeLeases != 0 {
			return fmt.Errorf("goal %q has active work: %w", goal.ID, basestore.ErrActiveLease)
		}
		now := s.source.Now().UTC().Format(time.RFC3339Nano)
		superseded, err := tx.ExecContext(ctx, `UPDATE plan_revisions SET status = ?, version = version + 1, updated_at = ? WHERE goal_revision_id = ? AND status = ?`, domain.PlanSuperseded, now, revision.ID, domain.PlanActive)
		if err != nil {
			return fmt.Errorf("supersede active plan: %w", err)
		}
		supersededCount, err := superseded.RowsAffected()
		if err != nil {
			return fmt.Errorf("read superseded plan result: %w", err)
		}
		if supersededCount != 1 {
			return fmt.Errorf("goal %q active plan: %w", goal.ID, basestore.ErrConflict)
		}
		updated, err := tx.ExecContext(ctx, `UPDATE plan_revisions SET status = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND status = ?`, domain.PlanActive, now, id, expectedPlanVersion, domain.PlanDraft)
		if err != nil {
			return fmt.Errorf("activate replacement plan: %w", err)
		}
		affected, _ := updated.RowsAffected()
		if affected != 1 {
			return fmt.Errorf("plan revision %q: %w", id, basestore.ErrConflict)
		}
		updatedGoal, err := tx.ExecContext(ctx, `UPDATE goals SET state = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ?`, domain.GoalRunning, now, goal.ID, expectedGoalVersion)
		if err != nil {
			return fmt.Errorf("advance replanned goal: %w", err)
		}
		updatedGoalCount, err := updatedGoal.RowsAffected()
		if err != nil {
			return fmt.Errorf("read replanned goal result: %w", err)
		}
		if updatedGoalCount != 1 {
			return fmt.Errorf("goal %q: %w", goal.ID, basestore.ErrConflict)
		}
		if err := s.appendEvent(ctx, tx, "plan", id, prepared); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "goal", goal.ID, prepared); err != nil {
			return err
		}
		plan.Status, plan.Version = domain.PlanActive, plan.Version+1
		result = plan
		return nil
	})
	return result, err
}
