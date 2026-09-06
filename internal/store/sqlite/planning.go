package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/goalcompile"
	"github.com/monshunter/xgoal/internal/planner"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/supervisor"
)

var ErrInvalidPlanningProposal = errors.New("invalid planning proposal")

func planningIDs(goalID string, generation int64) (string, string) {
	return fmt.Sprintf("planning_%s_%d", goalID, generation), fmt.Sprintf("goal/%s/planner/%d", goalID, generation)
}
func normalizePlanningRequest(request planner.Request, generation int64) (planner.Request, error) {
	if request.ProtocolVersion == "" {
		request.ProtocolVersion = planner.RequestVersion
	}
	request.Generation = generation
	if request.Prior != nil {
		if err := request.Prior.Validate(); err != nil {
			return planner.Request{}, err
		}
		if request.Prior.Generation != generation-1 {
			return planner.Request{}, basestore.ErrConflict
		}
	}
	if request.ProtocolVersion != planner.RequestVersion || !validIdempotencyLabel(request.GoalID) || strings.ContainsAny(request.GoalID, "/\\\x00") || strings.TrimSpace(request.RawGoal) == "" || !validIdempotencyLabel(request.CreatedBy) || (request.Mode != "fast" && request.Mode != "standard") || generation < 1 {
		return planner.Request{}, errors.New("invalid planning request")
	}
	if request.BlockedReason == "" && (len(request.ConfigHash) != 64 || !validIdempotencyLabel(request.ProfileID)) {
		return planner.Request{}, errors.New("planning requires a configuration and trusted profile")
	}
	request.TrustedValidatorIDs = append([]string(nil), request.TrustedValidatorIDs...)
	sort.Strings(request.TrustedValidatorIDs)
	if request.Proposal != nil && request.BlockedReason == "" {
		if _, err := compilePlanning(request, *request.Proposal); err != nil {
			return planner.Request{}, err
		}
	}
	return request, nil
}
func compilePlanning(request planner.Request, proposal planner.Proposal) (goalcompile.Compiled, error) {
	if proposal.ProtocolVersion != planner.ProposalVersion || len(proposal.Ambiguities) != 0 {
		return goalcompile.Compiled{}, fmt.Errorf("%w: unresolved semantic gaps or invalid protocol", ErrInvalidPlanningProposal)
	}
	trusted := make(map[string]bool, len(request.TrustedValidatorIDs))
	for _, id := range request.TrustedValidatorIDs {
		trusted[id] = true
	}
	compiled, err := goalcompile.Compile(request.GoalID, request.GoalID+"_revision_1", request.GoalID+"_plan_1", proposal.Contract, proposal.Plan, trusted, request.ValidationCapabilities)
	if err != nil {
		return goalcompile.Compiled{}, fmt.Errorf("%w: %v", ErrInvalidPlanningProposal, err)
	}
	return compiled, nil
}
func (s *Store) planningEvent(ctx context.Context, tx *sql.Tx, kind, id, eventType string, payload any) error {
	event, err := prepareEvent(EventInput{Type: eventType, ActorType: "kernel", Payload: payload})
	if err != nil {
		return err
	}
	return s.appendEvent(ctx, tx, kind, id, event)
}

// AcceptPlanningGoal binds a replayable acceptance and the runnable intent in one commit.
// Replay precedes validation against today's configuration and validator set.
func (s *Store) AcceptPlanningGoal(ctx context.Context, scope, key string, rawRequest any, request planner.Request) (domain.IdempotencyRecord, bool, error) {
	if !validIdempotencyLabel(scope) || !validIdempotencyLabel(key) {
		return domain.IdempotencyRecord{}, false, errors.New("invalid idempotency identity")
	}
	requestJSON, requestHash, err := canonicalValue("idempotency-request", idempotencySchema, rawRequest)
	if err != nil {
		return domain.IdempotencyRecord{}, false, err
	}
	var result domain.IdempotencyRecord
	created := false
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		existing, err := readIdempotencyRecord(ctx, tx, scope, key)
		if err == nil {
			if existing.RequestHash != requestHash {
				return basestore.ErrIdempotencyConflict
			}
			result = existing
			return nil
		}
		if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		if _, err := readGoal(ctx, tx, request.GoalID); err == nil {
			return basestore.ErrAlreadyExists
		} else if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		request, err = normalizePlanningRequest(request, 1)
		if err != nil {
			return err
		}
		event, err := prepareEvent(EventInput{Type: "GoalCreated", ActorType: "user", ActorID: request.CreatedBy, Payload: map[string]any{"raw_goal": request.RawGoal, "mode": request.Mode, "request_hash": requestHash, "idempotency_scope": scope, "idempotency_key": key}})
		if err != nil {
			return err
		}
		if err = s.createGoalTx(ctx, tx, domain.Goal{ID: request.GoalID, State: domain.GoalDraft, Version: 1}, event); err != nil {
			return err
		}
		if err = s.insertPlanningTx(ctx, tx, request); err != nil {
			return err
		}
		state := "QUEUED"
		if request.BlockedReason != "" {
			state = "WAITING"
			if err = s.planningGateTx(ctx, tx, request.GoalID, 1, "PLANNING_CONFIGURATION_REQUIRED", request.BlockedReason); err != nil {
				return err
			}
		}
		responseJSON, responseHash, err := canonicalValue("idempotency-response", idempotencySchema, map[string]any{"goal_id": request.GoalID, "state": domain.GoalDraft, "version": 1, "planning_state": state, "planning_generation": 1})
		if err != nil {
			return err
		}
		now := s.source.Now().UTC().Format(time.RFC3339Nano)
		if _, err = tx.ExecContext(ctx, `INSERT INTO idempotency_records(scope,key,request_json,request_hash,state,response_status,response_json,response_hash,created_at,completed_at) VALUES (?,?,?,?,'COMPLETED',201,?,?,?,?)`, scope, key, requestJSON, requestHash, responseJSON, responseHash, now, now); err != nil {
			return err
		}
		result, err = readIdempotencyRecord(ctx, tx, scope, key)
		created = err == nil
		return err
	})
	if err != nil {
		return domain.IdempotencyRecord{}, false, err
	}
	return result, created, nil
}
func (s *Store) insertPlanningTx(ctx context.Context, tx *sql.Tx, request planner.Request) error {
	id, key := planningIDs(request.GoalID, request.Generation)
	body, hash, err := canonicalValue("effect-request", effectSchema, request)
	if err != nil {
		return err
	}
	now := s.source.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO effects(id,effect_key,effect_type,state,request_json,request_hash,version,created_at,updated_at) VALUES (?,?,'planner','REQUESTED',?,?,1,?,?)`, id, key, body, hash, now, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE goals SET planning_generation=?,planning_effect_id=? WHERE id=?`, request.Generation, id, request.GoalID); err != nil {
		return err
	}
	return s.planningEvent(ctx, tx, "effect", id, "PlanningRequested", map[string]any{"goal_id": request.GoalID, "generation": request.Generation, "config_hash": request.ConfigHash, "profile_id": request.ProfileID})
}
func (s *Store) Planning(ctx context.Context, goalID string) (PlanningRecord, error) {
	var result PlanningRecord
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = readPlanning(ctx, tx, goalID)
		return err
	})
	return result, err
}
func readPlanning(ctx context.Context, q rowQueryer, goalID string) (PlanningRecord, error) {
	var p PlanningRecord
	var err error
	p.Goal, err = readGoal(ctx, q, goalID)
	if err != nil {
		return p, err
	}
	var effectID sql.NullString
	var model string
	if err = q.QueryRowContext(ctx, `SELECT planning_generation,planning_effect_id,planning_paused,execution_model FROM goals WHERE id=?`, goalID).Scan(&p.Generation, &effectID, &p.Paused, &model); err != nil {
		return p, err
	}
	if !effectID.Valid {
		if p.Goal.ActiveRevisionID != "" && p.Goal.State != domain.GoalReady {
			return p, basestore.ErrNotFound
		}
		p.State = "WAITING"
		p.Blocker = "REQUEST_INTERRUPTED: no durable planning request; inspect the Goal and cancel it or submit a new Goal"
		if model != "current-directory" {
			p.Blocker = "MIGRATION_REQUIRED: historical execution requires explicit migration"
		}
		return p, nil
	}
	p.Effect, err = readEffect(ctx, q, effectID.String)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(p.Effect.RequestJSON, &p.Request); err != nil {
		return p, err
	}
	body, hash, err := canonicalValue("effect-request", effectSchema, p.Request)
	if err != nil {
		return p, err
	}
	if hash != p.Effect.RequestHash || !bytes.Equal(body, p.Effect.RequestJSON) || p.Effect.Type != "planner" || p.Request.ProtocolVersion != planner.RequestVersion || p.Request.GoalID != goalID || p.Request.Generation != p.Generation {
		return p, errors.New("invalid persisted planning request identity")
	}
	if len(p.Effect.ObservationJSON) > 0 {
		p.Observation = &planner.Observation{}
		if err = json.Unmarshal(p.Effect.ObservationJSON, p.Observation); err != nil {
			return p, err
		}
		body, hash, err = canonicalValue("effect-observation", effectSchema, p.Observation)
		if err != nil {
			return p, err
		}
		if hash != p.Effect.ObservationHash || !bytes.Equal(body, p.Effect.ObservationJSON) {
			return p, errors.New("invalid persisted planning observation hash")
		}
	}
	p.State = string(p.Effect.State)
	switch p.Effect.State {
	case domain.EffectRequested:
		p.State = "QUEUED"
	case domain.EffectSucceeded:
		p.State = "SUCCEEDED"
	case domain.EffectFailed, domain.EffectRecovering:
		p.State = "WAITING"
	}
	if p.Effect.State == domain.EffectRecovering && p.Observation != nil && p.Observation.ExecutionStopped && (p.Observation.FailureCode == "planner_interrupted" || p.Observation.FailureCode == "planner_paused") {
		p.State = "RECOVERING"
	}
	if p.Request.BlockedReason != "" {
		p.State = "WAITING"
		p.Blocker = p.Request.BlockedReason
	}
	if p.Observation != nil && p.Observation.FailureReason != "" {
		p.Blocker = p.Observation.FailureCode + ": " + p.Observation.FailureReason
	}
	if p.Paused {
		p.State = "PAUSED"
	}
	if p.Goal.State == domain.GoalCancelled {
		p.State = "CANCELLED"
	}
	if model != "current-directory" {
		p.State = "WAITING"
		p.Blocker = "MIGRATION_REQUIRED: historical execution cannot be resumed"
	}
	return p, nil
}
func (s *Store) RunnablePlanningGoalIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id FROM goals g JOIN effects e ON e.id=g.planning_effect_id WHERE g.state='DRAFT' AND g.active_revision_id='' AND g.planning_paused=0 AND g.execution_model='current-directory' AND (e.state IN ('REQUESTED','OBSERVING') OR (e.state='RECOVERING' AND json_extract(e.observation_json,'$.failure_code') IN ('planner_interrupted','planner_paused') AND json_extract(e.observation_json,'$.execution_stopped')=1)) AND COALESCE(json_extract(e.request_json,'$.blocked_reason'),'')='' ORDER BY g.created_at,g.id`)
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
func planningFence(p PlanningRecord, effectID string, generation, goalVersion, effectVersion int64) error {
	if p.Effect.ID != effectID || p.Generation != generation || (goalVersion > 0 && p.Goal.Version != goalVersion) || (effectVersion > 0 && p.Effect.Version != effectVersion) {
		return basestore.ErrConflict
	}
	return nil
}
func planningCanExecute(p PlanningRecord, configHash string) error {
	if p.Goal.State != domain.GoalDraft || p.Goal.ActiveRevisionID != "" || p.Paused || p.Request.BlockedReason != "" {
		return ErrPlanningBlocked
	}
	if configHash == "" || p.Request.ConfigHash != configHash {
		return ErrConfigurationChanged
	}
	return nil
}
func (s *Store) BeginPlanning(ctx context.Context, c PlanningClaim) (PlanningRecord, error) {
	var result PlanningRecord
	if c.ExpectedGoalVersion <= 0 || c.ExpectedEffectVersion <= 0 || c.Generation <= 0 {
		return result, basestore.ErrConflict
	}
	if c.InvocationID == "" || c.InputTree == "" {
		return result, errors.New("planning start requires invocation and input tree")
	}
	if err := c.CheckoutIdentity.Validate(); err != nil {
		return result, err
	}
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		p, err := readPlanning(ctx, tx, c.GoalID)
		if err != nil {
			return err
		}
		if err = planningFence(p, c.EffectID, c.Generation, c.ExpectedGoalVersion, c.ExpectedEffectVersion); err != nil {
			return err
		}
		if err = planningCanExecute(p, c.CurrentConfigHash); err != nil {
			return err
		}
		if p.Effect.State != domain.EffectRequested {
			return basestore.ErrConflict
		}
		if prior := p.Request.Prior; prior != nil && (prior.Observation.InputTree != c.InputTree || prior.Observation.CheckoutIdentity != c.CheckoutIdentity) {
			return ErrCheckoutConflict
		}
		blocking, err := countBlockingRequiredGates(ctx, tx, c.GoalID, "", s.source.Now())
		if err != nil {
			return err
		}
		if blocking != 0 {
			return basestore.ErrAuthorizationDenied
		}
		if err = executionIdle(ctx, tx); err != nil {
			return err
		}
		observation := planner.Observation{InvocationID: c.InvocationID, InputTree: c.InputTree, CheckoutIdentity: c.CheckoutIdentity}
		if err = s.planningEffectTx(ctx, tx, &p, domain.EffectExecuting, observation, "PlanningStarted"); err != nil {
			return err
		}
		result, err = readPlanning(ctx, tx, c.GoalID)
		return err
	})
	return result, err
}
func (s *Store) PersistPlanningProposal(ctx context.Context, r PlanningResult) (PlanningRecord, error) {
	var result PlanningRecord
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		p, err := readPlanning(ctx, tx, r.GoalID)
		if err != nil {
			return err
		}
		if err = planningFence(p, r.EffectID, r.Generation, 0, 0); err != nil {
			return err
		}
		if p.Observation == nil || p.Observation.InvocationID != r.InvocationID || r.InvocationID == "" {
			return basestore.ErrConflict
		}
		observation := *p.Observation
		observation.Proposal = &r.Proposal
		observation.SessionID = r.SessionID
		observation.ExecutionStopped = true
		observation.FailureCode = ""
		observation.FailureReason = ""
		_, hash, err := canonicalValue("effect-observation", effectSchema, observation)
		if err != nil {
			return err
		}
		if p.Observation.Proposal != nil {
			if hash != p.Effect.ObservationHash {
				return basestore.ErrConflict
			}
			result = p
			return nil
		}
		if p.Effect.Version != r.ExpectedEffectVersion || (p.Effect.State != domain.EffectExecuting && p.Effect.State != domain.EffectRecovering) {
			return basestore.ErrConflict
		}
		if err = s.planningEffectTx(ctx, tx, &p, domain.EffectObserving, observation, "PlanningProposalObserved"); err != nil {
			return err
		}
		result, err = readPlanning(ctx, tx, r.GoalID)
		return err
	})
	return result, err
}
func (s *Store) planningEffectTx(ctx context.Context, tx *sql.Tx, p *PlanningRecord, state domain.EffectState, observation planner.Observation, eventType string) error {
	if err := domain.ValidateEffectTransition(p.Effect.State, state); err != nil {
		return err
	}
	body, hash, err := canonicalValue("effect-observation", effectSchema, observation)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE effects SET state=?,observation_json=?,observation_hash=?,version=version+1,updated_at=? WHERE id=? AND version=?`, state, body, hash, s.source.Now().UTC().Format(time.RFC3339Nano), p.Effect.ID, p.Effect.Version)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return basestore.ErrConflict
	}
	if err = s.planningEvent(ctx, tx, "effect", p.Effect.ID, eventType, map[string]any{"goal_id": p.Goal.ID, "generation": p.Generation, "state": state, "observation_hash": hash}); err != nil {
		return err
	}
	if err := s.planningEvent(ctx, tx, "goal", p.Goal.ID, eventType, map[string]any{"planning_effect_id": p.Effect.ID, "planning_generation": p.Generation, "effect_state": state}); err != nil {
		return err
	}
	p.Effect.State = state
	p.Effect.Version++
	p.Effect.ObservationJSON = body
	p.Effect.ObservationHash = hash
	p.Observation = &observation
	return nil
}
func (s *Store) PublishPlanning(ctx context.Context, publish PlanningPublish) (PlanningRecord, error) {
	var result PlanningRecord
	if publish.ExpectedGoalVersion <= 0 || publish.ExpectedEffectVersion <= 0 || publish.Generation <= 0 {
		return result, basestore.ErrConflict
	}
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		p, err := readPlanning(ctx, tx, publish.GoalID)
		if err != nil {
			return err
		}
		if err = planningFence(p, publish.EffectID, publish.Generation, publish.ExpectedGoalVersion, publish.ExpectedEffectVersion); err != nil {
			return err
		}
		if err = planningCanExecute(p, publish.CurrentConfigHash); err != nil {
			return err
		}
		stopped, err := planningProcessesStopped(ctx, tx, p.Goal.ID)
		if err != nil {
			return err
		}
		if !stopped {
			return ErrCheckoutBusy
		}
		if err := processSlotAvailable(ctx, tx, supervisor.Owner{Kind: "planning", ID: p.Effect.ID, GoalID: p.Goal.ID, Generation: p.Generation}); err != nil {
			return err
		}
		if p.Effect.State != domain.EffectObserving || p.Observation == nil || p.Observation.Proposal == nil || publish.ObservationHash == "" || p.Effect.ObservationHash != publish.ObservationHash {
			return basestore.ErrConflict
		}
		compiled, err := compilePlanning(p.Request, *p.Observation.Proposal)
		if err != nil {
			return err
		}
		revisionID, planID := p.Goal.ID+"_revision_1", p.Goal.ID+"_plan_1"
		contract := map[string]any{"protocol_version": goalcompile.ContractVersion, "contract": compiled.Contract, "config_hash": p.Request.ConfigHash, "created_by": p.Request.CreatedBy, "mode": p.Request.Mode}
		contractJSON, contractHash, err := canonicalValue("goal-revision", goalRevisionSchema, contract)
		if err != nil {
			return err
		}
		event, err := prepareEvent(EventInput{Type: "GoalRevisionFrozen", ActorType: "kernel", Payload: map[string]any{"planning_effect_id": p.Effect.ID, "generation": p.Generation}})
		if err != nil {
			return err
		}
		var revision domain.GoalRevision
		if err = s.freezeGoalRevisionTx(ctx, tx, GoalRevisionDraft{ID: revisionID, GoalID: p.Goal.ID, Revision: 1, RawGoal: p.Request.RawGoal, Contract: contract}, p.Goal.Version, contractJSON, contractHash, event, &revision); err != nil {
			return err
		}
		draft := PlanRevisionDraft{ID: planID, GoalRevisionID: revisionID, Revision: 1, WorkItems: compiled.WorkItems, Dependencies: compiled.Dependencies}
		works, deps, graphHash, err := validateAndHashPlan(draft)
		if err != nil {
			return err
		}
		event, err = prepareEvent(EventInput{Type: "PlanRevisionCreated", ActorType: "kernel", Payload: map[string]any{"planning_effect_id": p.Effect.ID}})
		if err != nil {
			return err
		}
		workEvents := make(map[string]preparedEvent, len(works))
		for _, work := range works {
			we, e := prepareEvent(EventInput{Type: "WorkItemCreated", ActorType: "kernel", Payload: map[string]any{"plan_revision_id": planID}})
			if e != nil {
				return e
			}
			workEvents[work.ID] = we
		}
		var plan domain.PlanRevision
		if err = s.createPlanRevisionTx(ctx, tx, draft, works, deps, graphHash, event, workEvents, &plan); err != nil {
			return err
		}
		event, err = prepareEvent(EventInput{Type: "PlanRevisionActivated", ActorType: "kernel", Payload: map[string]any{"planning_effect_id": p.Effect.ID}})
		if err != nil {
			return err
		}
		if err = s.clearPlanningGatesTx(ctx, tx, p.Goal.ID, "planning published"); err != nil {
			return err
		}
		if err = s.activatePlanRevisionTx(ctx, tx, planID, 1, p.Goal.Version+1, event, &plan); err != nil {
			return err
		}
		event, err = prepareEvent(EventInput{Type: "WorkItemsReadied", ActorType: "kernel", Payload: map[string]any{"plan_revision_id": planID}})
		if err != nil {
			return err
		}
		var ready []string
		if err = s.refreshReadyWorkTx(ctx, tx, p.Goal.ID, event, &ready); err != nil {
			return err
		}
		if err = s.planningEffectTx(ctx, tx, &p, domain.EffectSucceeded, *p.Observation, "PlanningPublished"); err != nil {
			return err
		}
		result, err = readPlanning(ctx, tx, p.Goal.ID)
		return err
	})
	return result, err
}
