package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/acceptance"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/redact"
	basestore "github.com/monshunter/xgoal/internal/store"
)

// Both versions are supplied by the user. Continuation never refreshes CAS or
// records a second decision when an already approved Gate cannot yet resume.
type GateContinuation struct {
	GateID       string
	GateVersion  int64
	OwnerVersion int64
}

// ResumeAcceptanceGate only resumes the final owner. The answer is consumed by
// BeginAcceptance together with the next Effect, after the final scene is rebuilt.
func (s *Store) ResumeAcceptanceGate(ctx context.Context, goalID string, c GateContinuation, configHash string, identity gitrepo.CheckoutIdentity, tree string) (domain.Goal, error) {
	var result domain.Goal
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		gate, err := s.continuationGate(ctx, tx, c)
		if err != nil {
			return err
		}
		prior, err := latestAcceptance(ctx, tx, goalID)
		if err != nil {
			return err
		}
		r, err := acceptanceRequest(prior)
		if err != nil {
			return err
		}
		if prior.State != domain.EffectFailed && prior.State != domain.EffectSucceeded {
			return ErrCheckoutBusy
		}
		var observation acceptance.Observation
		if json.Unmarshal(prior.ObservationJSON, &observation) != nil || !observation.ExecutionStopped {
			return basestore.ErrConflict
		}
		if r.Packet.ConfigHash != configHash {
			return ErrConfigurationChanged
		}
		r.GoalVersion = c.OwnerVersion
		if err := acceptanceBindingState(ctx, tx, r, true, domain.GoalWaiting); err != nil {
			return err
		}
		checkout, err := readCheckout(ctx, tx)
		if err != nil {
			return err
		}
		if checkout.Identity != identity || tree != checkout.AcceptedTree {
			return ErrCheckoutConflict
		}
		if err := executionIdle(ctx, tx); err != nil {
			return err
		}
		r.PreviousInvocationID = r.Packet.ID
		r.Packet.Decisions = []protocol.PacketDecision{{GateID: gate.ID, GateVersion: c.GateVersion, Answer: redact.String(gate.DecisionReason)}}
		if _, err := s.acceptanceDecision(ctx, tx, r, prior); err != nil {
			return err
		}
		blocking, err := countBlockingRequiredGates(ctx, tx, goalID, "", s.source.Now())
		if err != nil {
			return err
		}
		if blocking != 0 {
			return basestore.ErrAuthorizationDenied
		}
		if _, err := tx.ExecContext(ctx, `UPDATE goals SET state='RUNNING',version=version+1,updated_at=? WHERE id=? AND version=?`, s.source.Now().UTC().Format(time.RFC3339Nano), goalID, c.OwnerVersion); err != nil {
			return err
		}
		if err := s.planningEvent(ctx, tx, "goal", goalID, "AcceptanceResumed", map[string]any{"gate_id": gate.ID, "gate_version": gate.Version, "previous_invocation_id": r.PreviousInvocationID}); err != nil {
			return err
		}
		result, err = readGoal(ctx, tx, goalID)
		return err
	})
	return result, err
}

func (s *Store) continuationGate(ctx context.Context, tx *sql.Tx, c GateContinuation) (domain.Gate, error) {
	gate, err := readGate(ctx, tx, c.GateID)
	if err != nil {
		return gate, err
	}
	if c.GateVersion <= 0 || c.OwnerVersion <= 0 || gate.Version != c.GateVersion {
		return gate, basestore.ErrConflict
	}
	if gate.State != domain.GateApproved || gate.Decision != domain.GateAllow || gate.Used != 0 || gate.MaxUses != 1 || !gate.ExpiresAt.After(s.source.Now()) || gate.Action != domain.ActionExecCommand || strings.TrimSpace(gate.DecisionReason) == "" {
		return gate, basestore.ErrAuthorizationDenied
	}
	return gate, nil
}

func planningContinuationReason(reason string) bool {
	switch reason {
	case "planner_failed", "planner_timeout", "planner_interrupted", "planner_invalid_proposal":
		return true
	default:
		return false
	}
}
