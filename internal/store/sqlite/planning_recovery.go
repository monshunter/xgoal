package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/goalcompile"
	basestore "github.com/monshunter/xgoal/internal/store"
)

// RecoverPlanning runs only under daemon/project ownership, after process
// recovery. A saved proposal is left for deterministic publication by Engine.
func (s *Store) RecoverPlanning(ctx context.Context, options PlanningRecoveryOptions) error {
	if options.NoProgressLimit <= 0 {
		options.NoProgressLimit = 2
	}
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		ids, err := planningRecoveryIDs(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range ids {
			p, err := readPlanning(ctx, tx, id)
			if err != nil {
				return err
			}
			if p.Effect.ID == "" {
				if p.Goal.State == domain.GoalDraft {
					if err := s.planningGateTx(ctx, tx, id, 0, "REQUEST_INTERRUPTED", "No complete durable planning request is available. Inspect the historical request, cancel this draft and submit a new Goal."); err != nil {
						return err
					}
				} else if p.Goal.State == domain.GoalReady {
					if err := s.recoverReadyPlanningTx(ctx, tx, p.Goal, options.CurrentConfigHash); err != nil {
						return err
					}
				}
				continue
			}
			if p.Effect.State == domain.EffectFailed || p.Effect.State == domain.EffectSucceeded {
				continue
			}
			stopped, err := planningProcessesStopped(ctx, tx, id)
			if err != nil {
				return err
			}
			// Persisted process ownership is stronger than queue state. Never retire
			// a live invocation, including a cancelled Goal's still-running writer.
			if !stopped {
				continue
			}
			if p.Goal.State == domain.GoalCancelled {
				if err := s.failPlanningTx(ctx, tx, &p, "planner_cancelled", "Goal was cancelled", true); err != nil {
					return err
				}
				continue
			}
			if p.Paused {
				continue
			}
			if p.Request.BlockedReason != "" {
				continue
			}
			if options.CurrentConfigHash == "" || p.Request.ConfigHash != options.CurrentConfigHash {
				if err := s.failPlanningTx(ctx, tx, &p, "configuration_changed", ErrConfigurationChanged.Error(), true); err != nil {
					return err
				}
				continue
			}
			if p.Effect.State == domain.EffectRequested || p.Effect.State == domain.EffectObserving {
				continue
			}
			// Recovery without an observed result creates a fresh invocation. The
			// original effect and packet remain immutable audit history.
			if p.Effect.State == domain.EffectExecuting || p.Effect.State == domain.EffectRecovering {
				code := "planner_interrupted"
				paused, err := planningGenerationPaused(ctx, tx, p)
				if err != nil {
					return err
				}
				if paused || (p.Observation != nil && p.Observation.FailureCode == "planner_paused") {
					code = "planner_paused"
				}
				if err := s.failPlanningTx(ctx, tx, &p, code, "Previous planning invocation stopped without a publishable result", true); err != nil {
					return err
				}
				var interruptions int
				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM effects WHERE effect_type='planner' AND json_extract(request_json,'$.goal_id')=? AND json_extract(request_json,'$.config_hash')=? AND json_extract(observation_json,'$.failure_code')='planner_interrupted'`, p.Goal.ID, p.Request.ConfigHash).Scan(&interruptions); err != nil {
					return err
				}
				if code == "planner_interrupted" && interruptions >= options.NoProgressLimit {
					continue
				}
				// An answered Gate authorized one new generation. A crash cannot
				// reuse that finite decision for another Provider invocation.
				if p.Request.Prior != nil {
					continue
				}
				request, err := normalizePlanningRequest(p.Request, p.Generation+1)
				if err != nil {
					return err
				}
				var next PlanningRecord
				if err := s.retryPlanningTx(ctx, tx, p, request, "daemon recovered an interrupted planning invocation", &next); err != nil {
					return err
				}
			}
		}
		return s.recoverInterruptedRequestsTx(ctx, tx)
	})
}

func planningRecoveryIDs(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM goals WHERE (state IN ('DRAFT','READY') OR planning_effect_id IS NOT NULL) AND execution_model='current-directory' ORDER BY id`)
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
	return ids, rows.Err()
}

func (s *Store) recoverInterruptedRequestsTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT scope,key FROM idempotency_records WHERE state='IN_PROGRESS' ORDER BY scope,key`)
	if err != nil {
		return err
	}
	var keys [][2]string
	for rows.Next() {
		var key [2]string
		if err := rows.Scan(&key[0], &key[1]); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, key := range keys {
		record, err := readIdempotencyRecord(ctx, tx, key[0], key[1])
		if err != nil {
			return err
		}
		status := 409
		var response any = map[string]any{"error": map[string]any{"code": "REQUEST_INTERRUPTED", "message": "The previous daemon stopped before recording a response. Inspect current state before issuing a new request; this request will not be replayed."}}
		var request struct {
			GoalID string `json:"goal_id"`
		}
		_ = json.Unmarshal(record.RequestJSON, &request)
		if key[0] == "POST /v1/goals" && request.GoalID != "" {
			p, readErr := readPlanning(ctx, tx, request.GoalID)
			if readErr == nil {
				var proofs int
				err = tx.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE aggregate_type='goal' AND aggregate_id=? AND event_type='GoalCreated' AND json_extract(payload_json,'$.request_hash')=? AND json_extract(payload_json,'$.idempotency_scope')=? AND json_extract(payload_json,'$.idempotency_key')=?`, request.GoalID, record.RequestHash, key[0], key[1]).Scan(&proofs)
				if err != nil {
					return err
				}
				if proofs == 1 && p.Effect.ID != "" {
					state := "QUEUED"
					if p.Request.BlockedReason != "" {
						state = "WAITING"
					}
					status = 201
					response = map[string]any{"goal_id": p.Goal.ID, "state": domain.GoalDraft, "version": 1, "planning_state": state, "planning_generation": 1}
				} else if p.Goal.State == domain.GoalDraft || p.Goal.State == domain.GoalReady {
					if err := s.planningGateTx(ctx, tx, p.Goal.ID, 0, "REQUEST_INTERRUPTED", "Historical request ownership is not proven. Inspect the retained request and current Goal before cancellation or a new submission."); err != nil {
						return err
					}
				}
			} else if !errors.Is(readErr, basestore.ErrNotFound) {
				return readErr
			}
		}
		body, hash, err := canonicalValue("idempotency-response", idempotencySchema, response)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE idempotency_records SET state='COMPLETED',response_status=?,response_json=?,response_hash=?,completed_at=? WHERE scope=? AND key=? AND state='IN_PROGRESS'`, status, body, hash, s.source.Now().UTC().Format(time.RFC3339Nano), key[0], key[1]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) recoverReadyPlanningTx(ctx context.Context, tx *sql.Tx, goal domain.Goal, currentConfigHash string) error {
	block := func(reason string) error {
		return s.planningGateTx(ctx, tx, goal.ID, 0, "PLANNING_GRAPH_INCOMPLETE", reason+"; inspect, cancel and submit a new Goal if the complete frozen graph cannot be proven")
	}
	revision, err := readGoalRevision(ctx, tx, goal.ActiveRevisionID)
	if err != nil {
		return block("Frozen Goal Revision is missing")
	}
	var contract map[string]json.RawMessage
	if err := json.Unmarshal(revision.ContractJSON, &contract); err != nil {
		return block("Frozen contract is not valid JSON")
	}
	_, hash, err := canonicalValue("goal-revision", goalRevisionSchema, contract)
	if err != nil || hash != revision.Hash {
		return block("Frozen contract hash is inconsistent")
	}
	var envelope struct {
		ProtocolVersion string               `json:"protocol_version"`
		Contract        goalcompile.Contract `json:"contract"`
	}
	if err := json.Unmarshal(revision.ContractJSON, &envelope); err != nil || envelope.ProtocolVersion != goalcompile.ContractVersion || envelope.Contract.Validate() != nil {
		return block("Frozen contract semantics cannot be proven")
	}
	var configHash string
	_ = json.Unmarshal(contract["config_hash"], &configHash)
	if currentConfigHash == "" || configHash != currentConfigHash {
		return block("Frozen contract configuration does not match the daemon")
	}
	var count int
	var planID string
	if err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(id),'') FROM plan_revisions WHERE goal_revision_id=? AND status='DRAFT'`, revision.ID).Scan(&count, &planID); err != nil {
		return err
	}
	if count != 1 {
		return block("Exactly one complete draft Plan is required")
	}
	plan, err := readPlanRevision(ctx, tx, planID)
	if err != nil {
		return err
	}
	// Only the old initial compiler's transaction is an automatic recovery
	// source. A failed human replan can also leave a DRAFT with valid hashes.
	contractHash, err := canonical.Hash("goal-contract", goalcompile.ContractVersion, envelope.Contract)
	if err != nil {
		return err
	}
	var provenance int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM events p JOIN events r ON r.aggregate_type='goal' AND r.aggregate_id=? AND r.event_type='GoalRevisionFrozen' AND r.actor_type='planner' AND r.actor_id=p.actor_id AND json_extract(r.payload_json,'$.proposal_hash')=? WHERE p.aggregate_type='plan' AND p.aggregate_id=? AND p.event_type='PlanRevisionCreated' AND p.actor_type='planner' AND length(json_extract(p.payload_json,'$.proposal_hash'))=64`, goal.ID, contractHash, plan.ID).Scan(&provenance); err != nil {
		return err
	}
	if revision.ID != goal.ID+"_revision_1" || revision.Revision != 1 || plan.ID != goal.ID+"_plan_1" || plan.Revision != 1 || provenance != 1 {
		return block("Initial compiled Plan provenance cannot be proven")
	}
	draft := PlanRevisionDraft{ID: plan.ID, GoalRevisionID: revision.ID, Revision: plan.Revision}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM work_items WHERE plan_revision_id=? ORDER BY id`, plan.ID)
	if err != nil {
		return err
	}
	var workIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		workIDs = append(workIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range workIDs {
		work, err := readWorkItem(ctx, tx, id)
		if err != nil {
			return err
		}
		if work.State != domain.WorkPending {
			return block("Draft Work has already changed state")
		}
		draft.WorkItems = append(draft.WorkItems, work)
	}
	rows, err = tx.QueryContext(ctx, `SELECT d.from_id,d.to_id,d.dependency_type FROM work_dependencies d JOIN work_items w ON w.id=d.to_id WHERE w.plan_revision_id=? ORDER BY d.from_id,d.to_id`, plan.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var dependency domain.WorkDependency
		if err := rows.Scan(&dependency.FromID, &dependency.ToID, &dependency.Type); err != nil {
			rows.Close()
			return err
		}
		draft.Dependencies = append(draft.Dependencies, dependency)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	_, _, graphHash, err := validateAndHashPlan(draft)
	if err != nil || graphHash != plan.GraphHash {
		return block("Stored Work Graph does not match its frozen hash")
	}
	criteria := make(map[string]bool, len(envelope.Contract.AcceptanceCriteria))
	for _, criterion := range envelope.Contract.AcceptanceCriteria {
		criteria[criterion.ID] = false
	}
	for _, work := range draft.WorkItems {
		for _, criterion := range work.AcceptanceCriteria {
			if _, exists := criteria[criterion]; !exists {
				return block("Work references an unknown Contract criterion")
			}
			if work.Required {
				criteria[criterion] = true
			}
		}
	}
	for _, covered := range criteria {
		if !covered {
			return block("Contract criterion has no Required Work")
		}
	}
	event, err := prepareEvent(EventInput{Type: "PlanningGraphRecovered", ActorType: "daemon", Payload: map[string]any{"plan_id": plan.ID, "graph_hash": graphHash}})
	if err != nil {
		return err
	}
	var activated domain.PlanRevision
	if _, err := tx.ExecContext(ctx, `UPDATE gates SET state='REVOKED',required=0,version=version+1,updated_at=? WHERE goal_id=? AND state='OPEN' AND reason_code='PLANNING_GRAPH_INCOMPLETE' AND json_extract(facts_json,'$.owner')='planning'`, s.source.Now().UTC().Format(time.RFC3339Nano), goal.ID); err != nil {
		return err
	}
	if err := s.activatePlanRevisionTx(ctx, tx, plan.ID, plan.Version, goal.Version, event, &activated); err != nil {
		return fmt.Errorf("recover complete draft graph: %w", err)
	}
	var ready []string
	return s.refreshReadyWorkTx(ctx, tx, goal.ID, event, &ready)
}
