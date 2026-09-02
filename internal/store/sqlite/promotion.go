package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/promotion"
	basestore "github.com/monshunter/xgoal/internal/store"
)

const promotionEffectType = "PROMOTION"

func (s *Store) Ensure(ctx context.Context, request promotion.Request) (promotion.Record, bool, error) {
	if err := request.Validate(); err != nil {
		return promotion.Record{}, false, err
	}
	_, _, err := s.RequestEffect(ctx, EffectRequest{
		ID: request.EffectID, Key: request.EffectKey, Type: promotionEffectType, Request: request,
	}, EventInput{Type: "PromotionEffectRequested", ActorType: "kernel", CorrelationID: request.ID, Payload: map[string]any{"promotion_id": request.ID}})
	if err != nil {
		return promotion.Record{}, false, err
	}
	created := false
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		existing, err := readPromotion(ctx, tx, request.ID)
		if err == nil {
			if !promotion.EqualRequest(existing.Request, request) {
				return fmt.Errorf("promotion %q: %w", request.ID, basestore.ErrIdempotencyConflict)
			}
			return nil
		}
		if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		var attemptPromotionCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM promotions WHERE attempt_id = ?`, request.AttemptID).Scan(&attemptPromotionCount); err != nil {
			return err
		}
		if attemptPromotionCount != 0 {
			return fmt.Errorf("attempt %q already has a promotion: %w", request.AttemptID, basestore.ErrAlreadyExists)
		}
		now := s.source.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO promotions(
    id, goal_id, goal_revision, work_item_id, attempt_id, lease_id, lease_generation,
    bundle_hash, evidence_set_id, old_commit, old_tree, candidate_tree,
    integration_ref, integration_commit, state, effect_id, version, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, 1, ?, ?)`,
			request.ID, request.GoalID, request.GoalRevision, request.WorkItemID, request.AttemptID,
			request.LeaseID, request.LeaseGeneration, request.BundleHash, request.EvidenceSetID,
			request.OldCommit, request.OldTree, request.CandidateTree, request.IntegrationRef,
			promotion.Requested, request.EffectID, now, now,
		); err != nil {
			return fmt.Errorf("insert promotion %q: %w", request.ID, err)
		}
		prepared, err := prepareEvent(EventInput{Type: "PromotionRequested", ActorType: "kernel", CorrelationID: request.EffectID, Payload: map[string]any{"attempt_id": request.AttemptID}})
		if err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "promotion", request.ID, prepared); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return promotion.Record{}, false, err
	}
	record, err := readPromotion(ctx, s.db, request.ID)
	return record.Record, created, err
}

func (s *Store) Preflight(ctx context.Context, request promotion.Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		var attemptState domain.AttemptState
		var workState domain.WorkState
		var goalState domain.GoalState
		var planState domain.PlanRevisionState
		var goalID, goalRevisionHash, workID, leaseAttemptID, leaseState, leaseExpiry, bundleState string
		var revision, generation int64
		err := tx.QueryRowContext(ctx, `
SELECT attempt.state, work.state, goal.state, plan.status,
       goal.id, goal_revision.revision, goal_revision.contract_hash, work.id,
       lease.attempt_id, lease.generation, lease.state, lease.expires_at,
       patch.state
FROM attempts attempt
JOIN work_items work ON work.id = attempt.work_item_id
JOIN plan_revisions plan ON plan.id = work.plan_revision_id
JOIN goal_revisions goal_revision ON goal_revision.id = plan.goal_revision_id
JOIN goals goal ON goal.id = goal_revision.goal_id
JOIN leases lease ON lease.id = ?
JOIN patch_bundles patch ON patch.attempt_id = attempt.id AND patch.bundle_hash = ?
WHERE attempt.id = ?`, request.LeaseID, request.BundleHash, request.AttemptID).Scan(
			&attemptState, &workState, &goalState, &planState,
			&goalID, &revision, &goalRevisionHash, &workID, &leaseAttemptID, &generation, &leaseState, &leaseExpiry, &bundleState,
		)
		if err != nil {
			return fmt.Errorf("read promotion readiness: %w", err)
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, leaseExpiry)
		if err != nil {
			return err
		}
		if attemptState != domain.AttemptPromoting || workState != domain.WorkVerifying || goalState != domain.GoalRunning || planState != domain.PlanActive ||
			goalID != request.GoalID || revision != request.GoalRevision || goalRevisionHash != request.GoalRevisionHash || workID != request.WorkItemID ||
			leaseAttemptID != request.AttemptID || generation != request.LeaseGeneration || leaseState != string(domain.LeaseActive) ||
			!s.source.Now().UTC().Before(expiresAt) || bundleState != "VALID" {
			return errors.New("promotion attempt, lease, goal, plan, patch, or work is not ready")
		}
		set, err := readEvidenceSet(ctx, tx, request.EvidenceSetID)
		if err != nil {
			return err
		}
		if set.TreeHash != request.CandidateTree || set.GoalRevisionHash != request.GoalRevisionHash || set.ConfigHash != request.ConfigHash {
			return errors.New("promotion evidence set does not bind the goal, config, and candidate tree")
		}
		for _, evidenceID := range set.EvidenceIDs {
			snapshot, err := readEvidence(ctx, tx, evidenceID)
			if err != nil {
				return err
			}
			if snapshot.State != domain.EvidenceCurrent || snapshot.Evidence.GoalRevisionHash != set.GoalRevisionHash ||
				snapshot.Evidence.ConfigHash != set.ConfigHash || snapshot.Evidence.TreeHash != set.TreeHash {
				return fmt.Errorf("promotion evidence %q is stale", evidenceID)
			}
		}
		var openGates int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM gates
WHERE goal_id = ? AND required = 1 AND state = 'OPEN'
  AND (work_item_id IS NULL OR work_item_id = ?)`, request.GoalID, request.WorkItemID).Scan(&openGates); err != nil {
			return err
		}
		if openGates != 0 {
			return errors.New("promotion has an open required gate")
		}
		return nil
	})
}

func (s *Store) RecordCommit(ctx context.Context, id, commit string) (promotion.Record, error) {
	return s.advancePromotion(ctx, id, promotion.CommitCreated, commit, "PromotionCommitCreated", nil)
}

func (s *Store) RecordRefUpdate(ctx context.Context, id, commit string) (promotion.Record, error) {
	return s.advancePromotion(ctx, id, promotion.RefUpdated, commit, "PromotionRefUpdated", nil)
}

func (s *Store) Observe(ctx context.Context, id string, observation promotion.Observation) (promotion.Record, error) {
	if err := observation.Validate(); err != nil {
		return promotion.Record{}, err
	}
	return s.advancePromotion(ctx, id, promotion.Observed, observation.IntegrationCommit, "PromotionObserved", observation)
}

func (s *Store) Fail(ctx context.Context, id, reason string) error {
	if id == "" || reason == "" {
		return errors.New("promotion failure requires id and reason")
	}
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		current, err := readPromotion(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.State == promotion.Observed {
			return errors.New("observed promotion cannot fail")
		}
		if current.State == promotion.Failed {
			return nil
		}
		now := s.source.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
UPDATE promotions SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`, promotion.Failed, now, id, current.Version); err != nil {
			return err
		}
		if err := s.transitionPromotionLifecycle(ctx, tx, current, false, now); err != nil {
			return err
		}
		observation := map[string]any{"promotion_id": id, "reason": reason, "integration_commit": current.IntegrationCommit}
		if err := finishPromotionEffect(ctx, tx, current.EffectID, domain.EffectFailed, observation, now); err != nil {
			return err
		}
		prepared, err := prepareEvent(EventInput{Type: "PromotionFailed", ActorType: "kernel", CorrelationID: current.EffectID, Payload: observation})
		if err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "promotion", id, prepared)
	})
}

// RecoverablePromotions returns persisted non-terminal Promotion requests.
// The Promotion Manager re-reads Git and its immutable marker before deciding
// whether to execute or only record an already-completed external effect.
func (s *Store) RecoverablePromotions(ctx context.Context) ([]promotion.Record, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id
FROM promotions
WHERE state IN (?, ?, ?)
ORDER BY created_at, id`, promotion.Requested, promotion.CommitCreated, promotion.RefUpdated)
	if err != nil {
		return nil, err
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
	result := make([]promotion.Record, 0, len(ids))
	for _, id := range ids {
		record, err := readPromotion(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		result = append(result, record.Record)
	}
	return result, nil
}

func (s *Store) advancePromotion(ctx context.Context, id string, target promotion.State, commit, eventType string, observation any) (promotion.Record, error) {
	if id == "" || commit == "" {
		return promotion.Record{}, errors.New("promotion id and commit are required")
	}
	var result promotion.Record
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		current, err := readPromotion(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.IntegrationCommit != "" && current.IntegrationCommit != commit {
			return errors.New("promotion commit changed")
		}
		if promotionStateAtLeast(current.State, target) {
			result = current.Record
			return nil
		}
		if !validPromotionAdvance(current.State, target) {
			return fmt.Errorf("invalid promotion transition %s -> %s", current.State, target)
		}
		now := s.source.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
UPDATE promotions
SET state = ?, integration_commit = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`, target, commit, now, id, current.Version); err != nil {
			return err
		}
		switch target {
		case promotion.CommitCreated:
			if err := advancePromotionEffect(ctx, tx, current.EffectID, domain.EffectExecuting, nil, now); err != nil {
				return err
			}
		case promotion.RefUpdated:
			if err := advancePromotionEffect(ctx, tx, current.EffectID, domain.EffectObserving, nil, now); err != nil {
				return err
			}
		case promotion.Observed:
			if err := s.transitionPromotionLifecycle(ctx, tx, current, true, now); err != nil {
				return err
			}
			if err := finishPromotionEffect(ctx, tx, current.EffectID, domain.EffectSucceeded, observation, now); err != nil {
				return err
			}
		}
		prepared, err := prepareEvent(EventInput{Type: eventType, ActorType: "kernel", CorrelationID: current.EffectID, Payload: map[string]any{"integration_commit": commit}})
		if err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "promotion", id, prepared); err != nil {
			return err
		}
		current.State = target
		current.IntegrationCommit = commit
		current.Version++
		result = current.Record
		return nil
	})
	return result, err
}

func (s *Store) transitionPromotionLifecycle(ctx context.Context, tx *sql.Tx, current promotionRecord, succeeded bool, now string) error {
	attempt, err := readAttempt(ctx, tx, current.AttemptID)
	if err != nil {
		return err
	}
	work, err := readWorkItem(ctx, tx, current.WorkItemID)
	if err != nil {
		return err
	}
	lease, err := readLease(ctx, tx, current.LeaseID)
	if err != nil {
		return err
	}
	if attempt.WorkItemID != work.ID || lease.AttemptID != attempt.ID || lease.WorkItemID != work.ID ||
		lease.Generation != current.LeaseGeneration || lease.State != domain.LeaseActive {
		return errors.New("promotion lifecycle no longer matches its active lease")
	}
	attemptTarget := domain.AttemptFailed
	workTarget := domain.WorkReconciling
	resultTree := attempt.ResultTree
	resultKind := attempt.ResultKind
	if succeeded {
		attemptTarget = domain.AttemptSucceeded
		workTarget = domain.WorkCompleted
		resultTree = current.CandidateTree
		resultKind = "PATCH_BUNDLE"
	}
	if err := domain.ValidateAttemptTransition(attempt.State, attemptTarget); err != nil {
		return err
	}
	if err := domain.ValidateWorkTransition(work.State, workTarget); err != nil {
		return err
	}
	if err := domain.ValidateLeaseTransition(lease.State, domain.LeaseReleased); err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, `
UPDATE attempts
SET state = ?, result_tree = ?, result_kind = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ? AND state = ?`,
		attemptTarget, resultTree, resultKind, now, attempt.ID, attempt.Version, attempt.State)
	if err != nil {
		return fmt.Errorf("finish promoted attempt %q: %w", attempt.ID, err)
	}
	if err := requireOnePromotionRow(updated, "attempt", attempt.ID); err != nil {
		return err
	}
	updated, err = tx.ExecContext(ctx, `
UPDATE work_items
SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ? AND state = ?`,
		workTarget, now, work.ID, work.Version, work.State)
	if err != nil {
		return fmt.Errorf("finish promoted work item %q: %w", work.ID, err)
	}
	if err := requireOnePromotionRow(updated, "work item", work.ID); err != nil {
		return err
	}
	updated, err = tx.ExecContext(ctx, `
UPDATE leases
SET state = ?, version = version + 1
WHERE id = ? AND generation = ? AND version = ? AND state = ?`,
		domain.LeaseReleased, lease.ID, lease.Generation, lease.Version, domain.LeaseActive)
	if err != nil {
		return fmt.Errorf("release promoted lease %q: %w", lease.ID, err)
	}
	if err := requireOnePromotionRow(updated, "lease", lease.ID); err != nil {
		return err
	}
	outcome := "failed"
	attemptEvent := "AttemptPromotionFailed"
	workEvent := "WorkPromotionFailed"
	if succeeded {
		outcome = "succeeded"
		attemptEvent = "AttemptPromotionSucceeded"
		workEvent = "WorkPromotionSucceeded"
	}
	events := []struct {
		aggregateType string
		aggregateID   string
		eventType     string
	}{
		{aggregateType: "attempt", aggregateID: attempt.ID, eventType: attemptEvent},
		{aggregateType: "work", aggregateID: work.ID, eventType: workEvent},
		{aggregateType: "lease", aggregateID: lease.ID, eventType: "LeaseReleasedAfterPromotion"},
	}
	for _, item := range events {
		prepared, err := prepareEvent(EventInput{
			Type: item.eventType, ActorType: "kernel", CorrelationID: current.EffectID,
			Payload: map[string]any{"promotion_id": current.ID, "outcome": outcome},
		})
		if err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, item.aggregateType, item.aggregateID, prepared); err != nil {
			return err
		}
	}
	return nil
}

func requireOnePromotionRow(result sql.Result, aggregate, id string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read promoted %s update result: %w", aggregate, err)
	}
	if affected != 1 {
		return fmt.Errorf("promoted %s %q: %w", aggregate, id, basestore.ErrConflict)
	}
	return nil
}

func advancePromotionEffect(ctx context.Context, tx *sql.Tx, id string, target domain.EffectState, observation any, now string) error {
	current, err := readEffect(ctx, tx, id)
	if err != nil {
		return err
	}
	if current.State == target || current.State == domain.EffectSucceeded {
		return nil
	}
	if current.State == domain.EffectRecovering && target == domain.EffectExecuting {
		target = domain.EffectObserving
	}
	if err := domain.ValidateEffectTransition(current.State, target); err != nil {
		return err
	}
	var observationJSON []byte
	var observationHash string
	if observation != nil {
		observationJSON, observationHash, err = canonicalValue("effect-observation", effectSchema, observation)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
UPDATE effects
SET state = ?, observation_json = COALESCE(?, observation_json), observation_hash = COALESCE(?, observation_hash),
    version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`, target, nullableBytes(observationJSON), nullableString(observationHash), now, id, current.Version)
	return err
}

func finishPromotionEffect(ctx context.Context, tx *sql.Tx, id string, target domain.EffectState, observation any, now string) error {
	current, err := readEffect(ctx, tx, id)
	if err != nil {
		return err
	}
	for current.State != domain.EffectObserving {
		var next domain.EffectState
		switch current.State {
		case domain.EffectRequested:
			next = domain.EffectExecuting
		case domain.EffectExecuting, domain.EffectRecovering:
			next = domain.EffectObserving
		default:
			return fmt.Errorf("effect %q cannot reach observation from %s", id, current.State)
		}
		if err := advancePromotionEffect(ctx, tx, id, next, nil, now); err != nil {
			return err
		}
		current, err = readEffect(ctx, tx, id)
		if err != nil {
			return err
		}
	}
	if target != domain.EffectSucceeded && target != domain.EffectFailed {
		return errors.New("promotion effect terminal state is invalid")
	}
	observationJSON, observationHash, err := canonicalValue("effect-observation", effectSchema, observation)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE effects
SET state = ?, observation_json = ?, observation_hash = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`, target, observationJSON, observationHash, now, id, current.Version)
	return err
}

type promotionRecord struct {
	promotion.Record
	EffectID string
}

func readPromotion(ctx context.Context, queryer rowQueryer, id string) (promotionRecord, error) {
	var result promotionRecord
	var requestJSON []byte
	var goalID, workItemID, attemptID, leaseID, bundleHash, evidenceSetID string
	var oldCommit, oldTree, candidateTree, integrationRef, effectType string
	var goalRevision, leaseGeneration int64
	err := queryer.QueryRowContext(ctx, `
SELECT p.goal_id, p.goal_revision, p.work_item_id, p.attempt_id, p.lease_id, p.lease_generation,
       p.bundle_hash, p.evidence_set_id, p.old_commit, p.old_tree, p.candidate_tree, p.integration_ref,
       p.state, p.integration_commit, p.version, p.effect_id, e.effect_type, e.request_json
FROM promotions p JOIN effects e ON e.id = p.effect_id
WHERE p.id = ?`, id).Scan(
		&goalID, &goalRevision, &workItemID, &attemptID, &leaseID, &leaseGeneration,
		&bundleHash, &evidenceSetID, &oldCommit, &oldTree, &candidateTree, &integrationRef,
		&result.State, &result.IntegrationCommit, &result.Version, &result.EffectID, &effectType, &requestJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return promotionRecord{}, fmt.Errorf("promotion %q: %w", id, basestore.ErrNotFound)
	}
	if err != nil {
		return promotionRecord{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(requestJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result.Request); err != nil {
		return promotionRecord{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return promotionRecord{}, errors.New("promotion effect request has trailing data")
	}
	if !result.Record.Valid() || result.ID != id || result.EffectID != result.Request.EffectID || effectType != promotionEffectType ||
		goalID != result.GoalID || goalRevision != result.GoalRevision || workItemID != result.WorkItemID || attemptID != result.AttemptID ||
		leaseID != result.LeaseID || leaseGeneration != result.LeaseGeneration || bundleHash != result.BundleHash ||
		evidenceSetID != result.EvidenceSetID || oldCommit != result.OldCommit || oldTree != result.OldTree ||
		candidateTree != result.CandidateTree || integrationRef != result.IntegrationRef {
		return promotionRecord{}, errors.New("persisted promotion violates its contract")
	}
	return result, nil
}

func validPromotionAdvance(from, to promotion.State) bool {
	return (from == promotion.Requested && to == promotion.CommitCreated) ||
		(from == promotion.CommitCreated && to == promotion.RefUpdated) ||
		(from == promotion.RefUpdated && to == promotion.Observed)
}

func promotionStateAtLeast(current, target promotion.State) bool {
	rank := map[promotion.State]int{promotion.Requested: 1, promotion.CommitCreated: 2, promotion.RefUpdated: 3, promotion.Observed: 4}
	return rank[current] >= rank[target] && rank[target] > 0
}
