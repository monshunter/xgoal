package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/monshunter/xgoal/internal/acceptance"
	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/redact"
	finalreport "github.com/monshunter/xgoal/internal/report"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/supervisor"
)

var ErrAcceptanceWaiting = errors.New("ACCEPTANCE_WAITING: inspect the final acceptance Gate, answer it and resume the Goal")

func (s *Store) LatestAcceptance(ctx context.Context, goalID string) (domain.Effect, error) {
	return latestAcceptance(ctx, s.db, goalID)
}
func latestAcceptance(ctx context.Context, q rowQueryer, goalID string) (domain.Effect, error) {
	return scanEffect(q.QueryRowContext(ctx, `SELECT id,effect_key,effect_type,state,request_json,request_hash,observation_json,observation_hash,version FROM effects WHERE effect_type='acceptance' AND json_extract(request_json,'$.packet.goal_id')=? ORDER BY rowid DESC LIMIT 1`, goalID), "goal", goalID)
}
func acceptanceRequest(e domain.Effect) (acceptance.Request, error) {
	var r acceptance.Request
	if e.Type != "acceptance" {
		return r, errors.New("not an acceptance effect")
	}
	if err := decodeCanonical(e.RequestJSON, &r); err != nil {
		return r, err
	}
	_, hash, err := canonicalValue("effect-request", effectSchema, r)
	if err != nil || hash != e.RequestHash || e.ID != acceptance.EffectID(r.Packet.ID) {
		return r, errors.New("acceptance request hash or identity mismatch")
	}
	return r, nil
}

// BeginAcceptance journals and starts exactly one immutable invocation. The
// replay decision and request share this transaction; a failed insert cannot
// consume the authorization and an exact retry cannot execute twice.
func (s *Store) BeginAcceptance(ctx context.Context, r acceptance.Request, event EventInput) (domain.Effect, bool, error) {
	var result domain.Effect
	if err := r.Packet.Validate(); err != nil {
		return result, false, err
	}
	if r.GoalVersion <= 0 || r.RecoveryLimit < 1 || r.RecoveryLimit > 100 {
		return result, false, errors.New("invalid acceptance version or recovery bound")
	}
	if err := adapter.ValidateExecution(&r.ExecutionConfig, r.Packet.ProfileID, r.ExecutionConfig.Provider, "acceptance"); err != nil {
		return result, false, err
	}
	schema, err := protocol.Schema(protocol.SchemaAgentResult)
	if err != nil {
		return result, false, err
	}
	invocation := acceptance.Invocation{InvocationID: r.Packet.ID, ProfileID: r.Packet.ProfileID, GoalRevisionHash: r.Packet.GoalRevisionHash, ConfigHash: r.Packet.ConfigHash, TreeHash: r.Packet.TreeHash, PacketPath: r.PacketPath, PacketHash: r.PacketHash, WorkDir: r.Packet.Workspace, ExecutionConfig: &r.ExecutionConfig, OutputSchema: schema, Prompt: "validate acceptance request", Timeout: time.Second, MaxOutputBytes: 1 << 20}
	packet, err := acceptance.ValidateInvocation(invocation)
	if err != nil {
		return result, false, err
	}
	expected, _ := canonical.Marshal(r.Packet)
	actual, _ := canonical.Marshal(packet)
	if string(expected) != string(actual) {
		return result, false, errors.New("journaled acceptance packet differs from immutable file")
	}
	requestJSON, requestHash, err := canonicalValue("effect-request", effectSchema, r)
	if err != nil {
		return result, false, err
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return result, false, err
	}
	created := false
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		id := acceptance.EffectID(r.Packet.ID)
		if existing, err := readEffect(ctx, tx, id); err == nil {
			if existing.Type != "acceptance" || existing.RequestHash != requestHash {
				return basestore.ErrIdempotencyConflict
			}
			result = existing
			return nil
		} else if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		if err := acceptanceBinding(ctx, tx, r, true); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM effects WHERE effect_type='acceptance' AND state NOT IN ('SUCCEEDED','FAILED')`).Scan(&pending); err != nil {
			return err
		}
		if pending != 0 {
			return ErrCheckoutBusy
		}
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM leases WHERE state='ACTIVE') + (SELECT count(*) FROM worker_processes WHERE state IN ('RUNNING','OBSERVING','LOST'))`).Scan(&pending); err != nil {
			return err
		}
		if pending != 0 {
			return ErrCheckoutBusy
		}
		owner := supervisor.Owner{Kind: "attempt", ID: r.Packet.OwnerAttemptID, GoalID: r.Packet.GoalID, Generation: r.Packet.OwnerGeneration}
		if err := processSlotAvailable(ctx, tx, owner); err != nil {
			return err
		}
		blocked, err := countBlockingRequiredGates(ctx, tx, r.Packet.GoalID, "", s.source.Now())
		if err != nil {
			return err
		}
		if blocked != 0 {
			return ErrAcceptanceWaiting
		}
		prior, priorErr := latestAcceptance(ctx, tx, r.Packet.GoalID)
		sameChain := false
		if priorErr == nil {
			old, err := acceptanceRequest(prior)
			if err != nil {
				return err
			}
			sameChain = old.Packet.GoalRevisionHash == r.Packet.GoalRevisionHash && old.Packet.ConfigHash == r.Packet.ConfigHash && old.Packet.TreeHash == r.Packet.TreeHash
			if sameChain {
				if r.PreviousInvocationID != old.Packet.ID {
					return basestore.ErrConflict
				}
				if len(r.Packet.Decisions) == 1 {
					if err := s.consumeAcceptanceDecision(ctx, tx, r, prior, prepared); err != nil {
						return err
					}
				} else {
					allowed, err := s.acceptanceReplayAllowed(ctx, tx, prior, r)
					if err != nil {
						return err
					}
					if !allowed {
						return ErrAcceptanceWaiting
					}
				}
			}
		} else if !errors.Is(priorErr, basestore.ErrNotFound) {
			return priorErr
		}
		if !sameChain && (r.PreviousInvocationID != "" || len(r.Packet.Decisions) != 0) {
			return basestore.ErrAuthorizationDenied
		}
		now := s.source.Now().UTC().Format(time.RFC3339Nano)
		_, err = tx.ExecContext(ctx, `INSERT INTO effects(id,effect_key,effect_type,state,request_json,request_hash,version,created_at,updated_at) VALUES (?,?,'acceptance','REQUESTED',?,?,1,?,?)`, id, id, requestJSON, requestHash, now, now)
		if err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "effect", id, prepared); err != nil {
			return err
		}
		result = domain.Effect{ID: id, Key: id, Type: "acceptance", State: domain.EffectRequested, RequestJSON: requestJSON, RequestHash: requestHash, Version: 1}
		if err := s.advanceAcceptance(ctx, tx, &result, domain.EffectExecuting, nil, prepared); err != nil {
			return err
		}
		created = true
		return nil
	})
	return result, created, err
}

func acceptanceBinding(ctx context.Context, q rowQueryer, r acceptance.Request, checkVersion bool) error {
	p := r.Packet
	goal, err := readGoal(ctx, q, p.GoalID)
	if err != nil {
		return err
	}
	if goal.State != domain.GoalVerifying || (checkVersion && goal.Version != r.GoalVersion) {
		return basestore.ErrConflict
	}
	rev, err := readGoalRevision(ctx, q, goal.ActiveRevisionID)
	if err != nil {
		return err
	}
	var contract struct {
		ConfigHash string `json:"config_hash"`
	}
	if json.Unmarshal(rev.ContractJSON, &contract) != nil || rev.Hash != p.GoalRevisionHash || contract.ConfigHash != p.ConfigHash {
		return basestore.ErrConflict
	}
	var incomplete int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM work_items w JOIN plan_revisions p ON p.id=w.plan_revision_id WHERE p.goal_revision_id=? AND p.status='ACTIVE' AND w.required=1 AND w.state<>'COMPLETED'`, rev.ID).Scan(&incomplete); err != nil {
		return err
	}
	if incomplete != 0 {
		return basestore.ErrConflict
	}
	checkout, err := readCheckout(ctx, q)
	if err != nil {
		return err
	}
	if checkout.GoalID != p.GoalID || checkout.WorkID != "" || checkout.AcceptedTree != p.TreeHash || checkout.ObservedTree != p.TreeHash {
		return ErrCheckoutConflict
	}
	var attemptID, tree string
	var generation int64
	err = q.QueryRowContext(ctx, `SELECT a.id,a.result_tree,l.generation FROM attempts a JOIN leases l ON l.attempt_id=a.id JOIN work_items w ON w.id=a.work_item_id JOIN plan_revisions pr ON pr.id=w.plan_revision_id WHERE pr.goal_revision_id=? AND pr.status='ACTIVE' AND a.state='SUCCEEDED' AND a.result_tree<>'' ORDER BY a.created_at DESC,a.id DESC LIMIT 1`, rev.ID).Scan(&attemptID, &tree, &generation)
	if err != nil {
		return err
	}
	if attemptID != p.OwnerAttemptID || generation != p.OwnerGeneration || tree != p.TreeHash {
		return basestore.ErrConflict
	}
	return nil
}

func (s *Store) acceptanceReplayAllowed(ctx context.Context, q rowQueryer, prior domain.Effect, next acceptance.Request) (bool, error) {
	old, err := acceptanceRequest(prior)
	if err != nil {
		return false, err
	}
	var observation acceptance.Observation
	if json.Unmarshal(prior.ObservationJSON, &observation) != nil {
		return false, nil
	}
	if prior.State != domain.EffectFailed || !next.ReplaySafe || !old.ReplaySafe || observation.Result != nil || observation.FailureCode != "acceptance_interrupted" || !observation.ExecutionStopped || observation.Historical || len(next.Packet.Decisions) != 0 {
		return false, nil
	}
	var interruptions int
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM effects WHERE effect_type='acceptance' AND json_extract(request_json,'$.packet.goal_id')=? AND json_extract(request_json,'$.packet.goal_revision_hash')=? AND json_extract(request_json,'$.packet.tree_hash')=? AND json_extract(observation_json,'$.failure_code')='acceptance_interrupted'`, next.Packet.GoalID, next.Packet.GoalRevisionHash, next.Packet.TreeHash).Scan(&interruptions)
	return interruptions <= next.RecoveryLimit, err
}

func (s *Store) consumeAcceptanceDecision(ctx context.Context, tx *sql.Tx, r acceptance.Request, prior domain.Effect, event preparedEvent) error {
	d := r.Packet.Decisions[0]
	gate, err := readGate(ctx, tx, d.GateID)
	if err != nil {
		return err
	}
	var facts struct{ Owner, InvocationID, GoalRevisionHash, ConfigHash, TreeHash string }
	// Gate facts use the same packet-shaped names as the immutable request.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(gate.FactsJSON, &raw); err != nil {
		return err
	}
	_ = json.Unmarshal(raw["owner"], &facts.Owner)
	_ = json.Unmarshal(raw["invocation_id"], &facts.InvocationID)
	_ = json.Unmarshal(raw["goal_revision_hash"], &facts.GoalRevisionHash)
	_ = json.Unmarshal(raw["config_hash"], &facts.ConfigHash)
	_ = json.Unmarshal(raw["tree_hash"], &facts.TreeHash)
	if gate.Version != d.GateVersion || gate.GoalID != r.Packet.GoalID || gate.WorkItemID != "" || gate.AttemptID != "" || facts.Owner != "final" || facts.InvocationID != r.PreviousInvocationID || facts.GoalRevisionHash != r.Packet.GoalRevisionHash || facts.ConfigHash != r.Packet.ConfigHash || facts.TreeHash != r.Packet.TreeHash || gate.ReasonCode != acceptance.ReplayReason || gate.Action != domain.ActionExecCommand || !slices.Equal(gate.Scope, []string{"goal/" + r.Packet.GoalID + "/final/" + r.PreviousInvocationID}) || gate.State != domain.GateApproved || gate.Decision != domain.GateAllow || gate.Used != 0 || gate.MaxUses != 1 || !gate.ExpiresAt.After(s.source.Now()) || redact.String(gate.DecisionReason) != d.Answer || prior.ID != acceptance.EffectID(r.PreviousInvocationID) {
		return basestore.ErrAuthorizationDenied
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gates SET used=1,version=version+1,updated_at=? WHERE id=? AND version=?`, s.source.Now().UTC().Format(time.RFC3339Nano), gate.ID, gate.Version); err != nil {
		return err
	}
	return s.appendEvent(ctx, tx, "gate", gate.ID, event)
}

// ObserveAcceptance may preserve late historical claims, but only a current
// callback can enter final validators. Recovery never invents a successful call.
func (s *Store) ObserveAcceptance(ctx context.Context, id string, version int64, requestHash, invocationID string, observation acceptance.Observation, recovering bool, event EventInput) (domain.Effect, bool, error) {
	var result domain.Effect
	valid := false
	if observation.Result != nil {
		if err := observation.Result.Validate(); err != nil {
			return result, false, err
		}
	}
	if observation.Result == nil && observation.FailureCode == "" {
		return result, false, errors.New("acceptance observation requires a claim or failure")
	}
	if observation.Result != nil {
		encoded, _ := json.Marshal(observation.Result)
		var model any
		if err := json.Unmarshal(encoded, &model); err != nil {
			return result, false, err
		}
		redacted, err := json.Marshal(redact.Value(model))
		if err != nil {
			return result, false, err
		}
		var claim protocol.AgentResult
		if json.Unmarshal(redacted, &claim) != nil {
			return result, false, errors.New("cannot redact claim")
		}
		observation.Result = &claim
	}
	observation.Reason = redact.String(observation.Reason)
	prepared, err := prepareEvent(event)
	if err != nil {
		return result, false, err
	}
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		e, err := readEffect(ctx, tx, id)
		if err != nil {
			return err
		}
		r, err := acceptanceRequest(e)
		if err != nil {
			return err
		}
		if e.Version != version || e.RequestHash != requestHash || r.Packet.ID != invocationID || e.State == domain.EffectSucceeded || e.State == domain.EffectFailed {
			return basestore.ErrConflict
		}
		var old acceptance.Observation
		if len(e.ObservationJSON) != 0 {
			if err := json.Unmarshal(e.ObservationJSON, &old); err != nil {
				return err
			}
			if old.Result != nil {
				observation.Result = old.Result
				observation.FailureCode = old.FailureCode
				observation.Reason = old.Reason
			}
		}
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM process_invocations WHERE id=? AND state IN ('INTENT','REGISTERED','UNKNOWN')`, acceptance.ProcessID(invocationID)).Scan(&live); err != nil {
			return err
		}
		if live != 0 {
			observation.ExecutionStopped = false
		}
		bindingErr := acceptanceBinding(ctx, tx, r, true)
		latest, latestErr := latestAcceptance(ctx, tx, r.Packet.GoalID)
		blocked, err := countBlockingRequiredGates(ctx, tx, r.Packet.GoalID, "", s.source.Now())
		if err != nil {
			return err
		}
		valid = bindingErr == nil && latestErr == nil && latest.ID == e.ID && blocked == 0 && !old.Historical
		observation.Historical = !valid || old.Historical
		if recovering || !observation.ExecutionStopped {
			if err := s.advanceAcceptance(ctx, tx, &e, domain.EffectRecovering, &observation, prepared); err != nil {
				return err
			}
		}
		if !observation.ExecutionStopped {
			result = e
			valid = false
			return nil
		}
		if e.State != domain.EffectObserving {
			if err := s.advanceAcceptance(ctx, tx, &e, domain.EffectObserving, &observation, prepared); err != nil {
				return err
			}
		}
		target := domain.EffectFailed
		if observation.Result != nil && observation.FailureCode == "" {
			target = domain.EffectSucceeded
		}
		if err := s.advanceAcceptance(ctx, tx, &e, target, &observation, prepared); err != nil {
			return err
		}
		result = e
		// The Gate and outcome are atomic. Pause/cancel leave only historical facts.
		if valid && (recovering || observation.Result == nil || observation.Result.Status != protocol.ResultCompleted || observation.FailureCode != "") {
			replayAllowed, err := s.acceptanceReplayAllowed(ctx, tx, e, r)
			if err != nil {
				return err
			}
			if !recovering || !replayAllowed {
				if err := s.acceptanceGateTx(ctx, tx, r, e, observation); err != nil {
					return err
				}
			}
			valid = false
		}
		return nil
	})
	return result, valid, err
}

func (s *Store) advanceAcceptance(ctx context.Context, tx *sql.Tx, e *domain.Effect, target domain.EffectState, observation *acceptance.Observation, event preparedEvent) error {
	if e.State != domain.EffectRecovering || target != domain.EffectRecovering {
		if err := domain.ValidateEffectTransition(e.State, target); err != nil {
			return err
		}
	}
	data, hash := e.ObservationJSON, e.ObservationHash
	if observation != nil {
		var err error
		data, hash, err = canonicalValue("effect-observation", effectSchema, observation)
		if err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE effects SET state=?,observation_json=?,observation_hash=?,version=version+1,updated_at=? WHERE id=? AND version=?`, target, nullableBytes(data), nullableString(hash), s.source.Now().UTC().Format(time.RFC3339Nano), e.ID, e.Version)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return basestore.ErrConflict
	}
	if err := s.appendEvent(ctx, tx, "effect", e.ID, event); err != nil {
		return err
	}
	e.State = target
	e.Version++
	e.ObservationJSON = data
	e.ObservationHash = hash
	return nil
}

func (s *Store) acceptanceGateTx(ctx context.Context, tx *sql.Tx, r acceptance.Request, e domain.Effect, o acceptance.Observation) error {
	goal, err := readGoal(ctx, tx, r.Packet.GoalID)
	if err != nil {
		return err
	}
	if goal.State != domain.GoalRunning && goal.State != domain.GoalVerifying {
		return nil
	}
	id := "gate_" + e.ID
	facts, err := canonical.Marshal(map[string]any{"owner": "final", "invocation_id": r.Packet.ID, "goal_revision_hash": r.Packet.GoalRevisionHash, "config_hash": r.Packet.ConfigHash, "tree_hash": r.Packet.TreeHash, "claim": o.Result, "failure_code": o.FailureCode, "reason": o.Reason})
	if err != nil {
		return err
	}
	scope, _ := canonical.Marshal([]string{"goal/" + goal.ID + "/final/" + r.Packet.ID})
	now := s.source.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO gates(id,goal_id,reason_code,state,facts_json,unknowns_json,options_json,recommendation,action,scope_json,expires_at,max_uses,used,revocable,required,version,created_at,updated_at) VALUES (?, ?, ?, 'OPEN', ?, ?, ?, ?, 'EXEC_COMMAND', ?, ?, 1,0,1,1,1,?,?) ON CONFLICT(id) DO NOTHING`, id, goal.ID, acceptance.ReplayReason, facts, []byte(`["whether the test data and external conditions are ready for a fresh acceptance session"]`), []byte(`["allow","deny"]`), "Inspect the claim and scenario diagnostics. Supply the requested answer and authorize one fresh invocation, then resume the Goal.", scope, now.Add(24*time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE goals SET state='WAITING',version=version+1,updated_at=? WHERE id=? AND version=?`, now.Format(time.RFC3339Nano), goal.ID, goal.Version); err != nil {
		return err
	}
	return s.planningEvent(ctx, tx, "goal", goal.ID, "AcceptanceWaiting", map[string]any{"gate_id": id, "invocation_id": r.Packet.ID, "owner": "final"})
}

// RequireAcceptanceReplay repairs the crash window after a terminal observation
// but before final validators/report publication. Terminal history is unchanged.
func (s *Store) RequireAcceptanceReplay(ctx context.Context, effectID string) error {
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		e, err := readEffect(ctx, tx, effectID)
		if err != nil {
			return err
		}
		r, err := acceptanceRequest(e)
		if err != nil {
			return err
		}
		if e.State != domain.EffectSucceeded && e.State != domain.EffectFailed {
			return ErrCheckoutBusy
		}
		latest, err := latestAcceptance(ctx, tx, r.Packet.GoalID)
		if err != nil {
			return err
		}
		if latest.ID != e.ID {
			return basestore.ErrConflict
		}
		if err := acceptanceBinding(ctx, tx, r, false); err != nil {
			return err
		}
		var o acceptance.Observation
		if err := json.Unmarshal(e.ObservationJSON, &o); err != nil {
			return err
		}
		allowed, err := s.acceptanceReplayAllowed(ctx, tx, e, r)
		if err != nil || allowed {
			return err
		}
		// An approved unused exact decision is consumed only by BeginAcceptance.
		gate, err := readGate(ctx, tx, "gate_"+e.ID)
		if err == nil && gate.State == domain.GateApproved && gate.Decision == domain.GateAllow && gate.Used == 0 && gate.ExpiresAt.After(s.source.Now()) {
			return nil
		}
		if err != nil && !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		return s.acceptanceGateTx(ctx, tx, r, e, o)
	})
}

// verifyFinalAcceptance prevents a manual finalization path from skipping a
// configured acceptance session. The claim is a required workflow observation,
// never a substitute for the separately verified deterministic evidence set.
func verifyFinalAcceptance(ctx context.Context, q rowQueryer, goal domain.Goal, value finalreport.Report) error {
	var planningJSON []byte
	err := q.QueryRowContext(ctx, `SELECT e.request_json FROM goals g JOIN effects e ON e.id=g.planning_effect_id WHERE g.id=?`, goal.ID).Scan(&planningJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var planning struct {
		ConfigHash   string                         `json:"config_hash"`
		Capabilities *config.ValidationCapabilities `json:"validation_capabilities"`
	}
	if len(planningJSON) != 0 {
		if err := json.Unmarshal(planningJSON, &planning); err != nil {
			return err
		}
	}
	required := planning.ConfigHash == value.Goal.ConfigHash && planning.Capabilities != nil && len(planning.Capabilities.RequiredScenarioIDs) > 0
	effect, err := latestAcceptance(ctx, q, goal.ID)
	if errors.Is(err, basestore.ErrNotFound) {
		if required {
			return errors.New("required Acceptance invocation is missing")
		}
		return nil
	}
	if err != nil {
		return err
	}
	request, err := acceptanceRequest(effect)
	if err != nil {
		return err
	}
	if request.Packet.GoalRevisionHash != value.Goal.RevisionHash {
		if required {
			return errors.New("required Acceptance belongs to an older revision")
		}
		return nil
	}
	if err := acceptanceBinding(ctx, q, request, true); err != nil {
		return err
	}
	var observation acceptance.Observation
	if err := json.Unmarshal(effect.ObservationJSON, &observation); err != nil {
		return err
	}
	_, hash, err := canonicalValue("effect-observation", effectSchema, observation)
	if err != nil {
		return err
	}
	if hash != effect.ObservationHash || effect.State != domain.EffectSucceeded || !observation.ExecutionStopped || observation.Historical || observation.Result == nil || observation.Result.Status != protocol.ResultCompleted || observation.FailureCode != "" {
		return errors.New("Acceptance has no current completed claim observation")
	}
	for _, scene := range request.Packet.Scenarios {
		found := false
		for _, m := range value.Scenarios {
			if m.Scenario.ID == scene.ID && m.EnvironmentID == request.Packet.EnvironmentID {
				found = true
				break
			}
		}
		if !found {
			return errors.New("Acceptance and final scenario assertions used different environments")
		}
	}
	return nil
}
