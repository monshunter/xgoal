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
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/planner"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/redact"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/supervisor"
)

func (s *Store) planningGateTx(ctx context.Context, tx *sql.Tx, goalID string, generation int64, code, reason string) error {
	id := fmt.Sprintf("planning_gate_%s_%d_%s", goalID, generation, code)
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM gates WHERE id=?`, id).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	facts, err := canonical.Marshal(map[string]any{"owner": "planning", "generation": generation, "reason": reason})
	if err != nil {
		return err
	}
	scope, err := canonical.Marshal([]string{"goal/" + goalID + "/planning"})
	if err != nil {
		return err
	}
	now := s.source.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO gates(id,goal_id,reason_code,state,facts_json,unknowns_json,options_json,recommendation,action,scope_json,expires_at,max_uses,used,revocable,required,version,created_at,updated_at) VALUES (?,?,?,'OPEN',?,?,?,?,'EXEC_COMMAND',?,?,1,0,1,1,1,?,?)`, id, goalID, code, facts, []byte(`[]`), []byte(`["inspect","goal plan","cancel"]`), reason, scope, now.Add(24*time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return s.planningEvent(ctx, tx, "goal", goalID, "PlanningWaiting", map[string]any{"gate_id": id, "generation": generation, "code": code, "reason": reason})
}

func (s *Store) clearPlanningGatesTx(ctx context.Context, tx *sql.Tx, goalID, reason string) error {
	var rejected int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gates WHERE goal_id=? AND required=1 AND (state IN ('DENIED','REVOKED','EXPIRED') OR (state='APPROVED' AND used=0 AND julianday(expires_at)<=julianday(?))) AND json_extract(facts_json,'$.owner')='planning'`, goalID, s.source.Now().UTC().Format(time.RFC3339Nano)).Scan(&rejected); err != nil {
		return err
	}
	if rejected != 0 {
		return basestore.ErrAuthorizationDenied
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM gates WHERE goal_id=? AND required=1 AND (state='OPEN' OR (state='APPROVED' AND decision='ALLOW')) AND json_extract(facts_json,'$.owner')='planning'`, goalID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE gates SET state=CASE WHEN state='OPEN' THEN 'REVOKED' ELSE state END,required=0,version=version+1,updated_at=? WHERE id=?`, s.source.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
			return err
		}
		if err := s.planningEvent(ctx, tx, "gate", id, "PlanningGateResolved", map[string]any{"reason": reason}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) FailPlanning(ctx context.Context, failure PlanningFailure) (PlanningRecord, error) {
	var result PlanningRecord
	if strings.TrimSpace(failure.Code) == "" || strings.TrimSpace(failure.Reason) == "" || failure.ExpectedEffectVersion <= 0 {
		return result, errors.New("planning failure identity and reason are required")
	}
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		p, err := readPlanning(ctx, tx, failure.GoalID)
		if err != nil {
			return err
		}
		if err := planningFence(p, failure.EffectID, failure.Generation, 0, failure.ExpectedEffectVersion); err != nil {
			return err
		}
		if failure.Code == "planner_interrupted" && failure.ExecutionStopped && p.Goal.State == domain.GoalDraft {
			paused, err := planningGenerationPaused(ctx, tx, p)
			if err != nil {
				return err
			}
			if paused {
				failure.Code = "planner_paused"
			}
			observation := planner.Observation{}
			if p.Observation != nil {
				observation = *p.Observation
			}
			observation.FailureCode, observation.FailureReason, observation.ExecutionStopped = failure.Code, failure.Reason, true
			if p.Effect.State != domain.EffectRecovering {
				if err := s.planningEffectTx(ctx, tx, &p, domain.EffectRecovering, observation, "PlanningInterrupted"); err != nil {
					return err
				}
			}
			result, err = readPlanning(ctx, tx, failure.GoalID)
			return err
		}
		if err := s.failPlanningTx(ctx, tx, &p, failure.Code, failure.Reason, failure.ExecutionStopped); err != nil {
			return err
		}
		result, err = readPlanning(ctx, tx, failure.GoalID)
		return err
	})
	return result, err
}

func planningGenerationPaused(ctx context.Context, tx *sql.Tx, p PlanningRecord) (bool, error) {
	var pauses int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE aggregate_type='goal' AND aggregate_id=? AND event_type='PlanningControlChanged' AND json_extract(payload_json,'$.generation')=? AND json_extract(payload_json,'$.paused')=1`, p.Goal.ID, p.Generation).Scan(&pauses)
	return pauses != 0, err
}

func (s *Store) failPlanningTx(ctx context.Context, tx *sql.Tx, p *PlanningRecord, code, reason string, stopped bool) error {
	if p.Effect.State == domain.EffectSucceeded || p.Effect.State == domain.EffectFailed {
		return basestore.ErrConflict
	}
	observation := planner.Observation{}
	if p.Observation != nil {
		observation = *p.Observation
	}
	observation.FailureCode, observation.FailureReason, observation.ExecutionStopped = code, reason, stopped
	if p.Effect.State != domain.EffectRecovering {
		if err := s.planningEffectTx(ctx, tx, p, domain.EffectRecovering, observation, "PlanningRecovering"); err != nil {
			return err
		}
	} else {
		// RECOVERING can acquire a new observation only through OBSERVING.
		if !stopped {
			return nil
		}
	}
	if stopped {
		if err := s.planningEffectTx(ctx, tx, p, domain.EffectObserving, observation, "PlanningFailureObserved"); err != nil {
			return err
		}
		if err := s.planningEffectTx(ctx, tx, p, domain.EffectFailed, observation, "PlanningFailed"); err != nil {
			return err
		}
	}
	if p.Goal.State == domain.GoalCancelled {
		return nil
	}
	if p.Paused && code == "planner_interrupted" {
		return nil
	}
	return s.planningGateTx(ctx, tx, p.Goal.ID, p.Generation, code, reason)
}

func (s *Store) SetPlanningPaused(ctx context.Context, goalID string, expectedGoalVersion int64, paused bool, reason string) (PlanningRecord, error) {
	var result PlanningRecord
	if expectedGoalVersion <= 0 || strings.TrimSpace(reason) == "" {
		return result, errors.New("planning control requires a version and reason")
	}
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		p, err := readPlanning(ctx, tx, goalID)
		if err != nil {
			return err
		}
		if p.Goal.Version != expectedGoalVersion {
			return basestore.ErrConflict
		}
		if p.Goal.State != domain.GoalDraft || p.Goal.ActiveRevisionID != "" {
			return ErrPlanningBlocked
		}
		if _, err := tx.ExecContext(ctx, `UPDATE goals SET planning_paused=?,version=version+1,updated_at=? WHERE id=? AND version=?`, boolInteger(paused), s.source.Now().UTC().Format(time.RFC3339Nano), goalID, expectedGoalVersion); err != nil {
			return err
		}
		if err := s.planningEvent(ctx, tx, "goal", goalID, "PlanningControlChanged", map[string]any{"paused": paused, "reason": reason, "generation": p.Generation}); err != nil {
			return err
		}
		result, err = readPlanning(ctx, tx, goalID)
		return err
	})
	return result, err
}

func planningProcessesStopped(ctx context.Context, q rowQueryer, goalID string) (bool, error) {
	var count int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM process_invocations WHERE goal_id=? AND owner_kind='planning' AND state IN ('INTENT','REGISTERED','UNKNOWN')`, goalID).Scan(&count)
	return count == 0, err
}

func (s *Store) RetryPlanning(ctx context.Context, goalID string, expectedGoalVersion int64, request planner.Request, reason string) (PlanningRecord, error) {
	return s.retryPlanning(ctx, goalID, expectedGoalVersion, request, reason, nil, gitrepo.CheckoutIdentity{}, "")
}

func (s *Store) ResumePlanningGate(ctx context.Context, goalID string, continuation GateContinuation, configHash string, identity gitrepo.CheckoutIdentity, tree string) (PlanningRecord, error) {
	return s.retryPlanning(ctx, goalID, continuation.OwnerVersion, planner.Request{ConfigHash: configHash}, "resume approved planning Gate", &continuation, identity, tree)
}

func (s *Store) retryPlanning(ctx context.Context, goalID string, expectedGoalVersion int64, request planner.Request, reason string, continuation *GateContinuation, identity gitrepo.CheckoutIdentity, tree string) (PlanningRecord, error) {
	var result PlanningRecord
	if expectedGoalVersion <= 0 || strings.TrimSpace(reason) == "" {
		return result, errors.New("planning retry requires a version and reason")
	}
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		p, err := readPlanning(ctx, tx, goalID)
		if err != nil {
			return err
		}
		if p.Goal.Version != expectedGoalVersion {
			return basestore.ErrConflict
		}
		if p.Goal.State != domain.GoalDraft || p.Goal.ActiveRevisionID != "" || p.Effect.ID == "" {
			return ErrPlanningBlocked
		}
		if continuation != nil {
			blocking, err := countBlockingRequiredGates(ctx, tx, goalID, "", s.source.Now())
			if err != nil {
				return err
			}
			if blocking != 0 {
				return basestore.ErrAuthorizationDenied
			}
			gate, err := s.continuationGate(ctx, tx, *continuation)
			if err != nil {
				return err
			}
			var facts struct {
				Owner      string `json:"owner"`
				Generation int64  `json:"generation"`
			}
			if err := json.Unmarshal(gate.FactsJSON, &facts); err != nil {
				return err
			}
			if gate.GoalID != goalID || gate.WorkItemID != "" || gate.AttemptID != "" || facts.Owner != "planning" || facts.Generation != p.Generation || len(gate.Scope) != 1 || gate.Scope[0] != "goal/"+goalID+"/planning" || !planningContinuationReason(gate.ReasonCode) || p.Effect.State != domain.EffectFailed || p.Observation == nil || !p.Observation.ExecutionStopped || p.Observation.InvocationID == "" {
				return basestore.ErrAuthorizationDenied
			}
			if request.ConfigHash != p.Request.ConfigHash {
				return ErrConfigurationChanged
			}
			if p.Observation.FailureCode != gate.ReasonCode {
				return basestore.ErrAuthorizationDenied
			}
			if identity != p.Observation.CheckoutIdentity || tree != p.Observation.InputTree {
				return ErrCheckoutConflict
			}
			observation := *p.Observation
			observation.FailureReason = redact.String(observation.FailureReason)
			request = p.Request
			request.Proposal = nil
			request.Prior = &planner.PriorContext{EffectID: p.Effect.ID, RequestHash: p.Effect.RequestHash, Generation: p.Generation, Observation: observation, Decision: protocol.PacketDecision{GateID: gate.ID, GateVersion: gate.Version, Answer: redact.String(gate.DecisionReason)}}
			if _, err := tx.ExecContext(ctx, `UPDATE gates SET used=1,required=0,version=version+1,updated_at=? WHERE id=? AND version=?`, s.source.Now().UTC().Format(time.RFC3339Nano), gate.ID, gate.Version); err != nil {
				return err
			}
			if err := s.planningEvent(ctx, tx, "gate", gate.ID, "GateContinuationConsumed", map[string]any{"generation": p.Generation + 1}); err != nil {
				return err
			}
		}
		if request.GoalID != goalID || request.RawGoal != p.Request.RawGoal || request.Mode != p.Request.Mode || request.CreatedBy != p.Request.CreatedBy {
			return basestore.ErrConflict
		}
		request, err = normalizePlanningRequest(request, p.Generation+1)
		if err != nil {
			return err
		}
		if request.BlockedReason != "" {
			return ErrPlanningBlocked
		}
		if p.Effect.State == domain.EffectExecuting {
			return ErrCheckoutBusy
		}
		stopped, err := planningProcessesStopped(ctx, tx, goalID)
		if err != nil {
			return err
		}
		if !stopped {
			return ErrCheckoutBusy
		}
		if err := processSlotAvailable(ctx, tx, supervisor.Owner{Kind: "planning", ID: p.Effect.ID, GoalID: goalID, Generation: p.Generation}); err != nil {
			return err
		}
		return s.retryPlanningTx(ctx, tx, p, request, reason, &result)
	})
	return result, err
}

func (s *Store) retryPlanningTx(ctx context.Context, tx *sql.Tx, p PlanningRecord, request planner.Request, reason string, result *PlanningRecord) error {
	if p.Effect.State != domain.EffectFailed {
		if err := s.failPlanningTx(ctx, tx, &p, "planning_superseded", reason, true); err != nil {
			return err
		}
	}
	if err := s.insertPlanningTx(ctx, tx, request); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE goals SET planning_paused=0,version=version+1,updated_at=? WHERE id=?`, s.source.Now().UTC().Format(time.RFC3339Nano), p.Goal.ID); err != nil {
		return err
	}
	if err := s.clearPlanningGatesTx(ctx, tx, p.Goal.ID, reason); err != nil {
		return err
	}
	if err := s.planningEvent(ctx, tx, "goal", p.Goal.ID, "PlanningRetried", map[string]any{"generation": request.Generation, "previous_effect_id": p.Effect.ID, "old_config_hash": p.Request.ConfigHash, "config_hash": request.ConfigHash, "reason": reason}); err != nil {
		return err
	}
	var err error
	*result, err = readPlanning(ctx, tx, p.Goal.ID)
	return err
}
