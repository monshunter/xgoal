package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/acceptance"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/supervisor"
)

func acceptanceFixture(t *testing.T) (*Store, acceptance.Request) {
	t.Helper()
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := clock.NewFake(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	s, err := Open(ctx, filepath.Join(root, "state"), source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	goal, _, _ := seedFinalizableReport(t, s, s.Info().ProjectDir, source, map[string]any{"config_hash": strings.Repeat("b", 64)})
	rev, err := s.GoalRevision(ctx, goal.ActiveRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.GoalStatus(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := acceptance.Packet{ProtocolVersion: acceptance.PacketVersion, ID: "accept_1", GoalID: goal.ID, GoalRevisionHash: rev.Hash, ConfigHash: strings.Repeat("b", 64), TreeHash: strings.Repeat("a", 64), ProfileID: "acceptor", OwnerAttemptID: "attempt_final", OwnerGeneration: status.Leases[0].Generation, Workspace: root, EnvironmentID: "env_final", ScenarioDir: filepath.Join(root, "scenario"), ProjectNetwork: "deny", Scenarios: []config.Scenario{{ID: "inspect", Description: "Inspect final result", Steps: []string{"Read output"}, Validators: []string{"go-test"}}}}
	// The legacy fixture completed an arbitrary result label; make the final
	// physical owner's result equal the accepted checkout, as a real promotion.
	if _, err := s.db.Exec(`UPDATE attempts SET result_tree=? WHERE id=?`, p.TreeHash, p.OwnerAttemptID); err != nil {
		t.Fatal(err)
	}
	e, err := (config.Agent{ID: p.ProfileID, Adapter: "codex-cli", Roles: []string{"acceptance"}}).Effective("acceptance", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	req := acceptance.Request{Packet: p, GoalVersion: goal.Version, ExecutionConfig: e, RecoveryLimit: 2}
	return s, prepareAcceptanceRequest(t, s, req)
}
func prepareAcceptanceRequest(t *testing.T, s *Store, r acceptance.Request) acceptance.Request {
	t.Helper()
	var err error
	r.PacketPath, r.PacketHash, err = acceptance.Prepare(s.Info().ProjectDir, r.Packet)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func acceptanceEvent() EventInput {
	return EventInput{Type: "AcceptanceTest", ActorType: "kernel", Payload: map[string]any{}}
}
func observeAcceptance(t *testing.T, s *Store, e domain.Effect, r acceptance.Request, status protocol.ResultStatus) domain.Effect {
	t.Helper()
	out, valid, err := s.ObserveAcceptance(context.Background(), e.ID, e.Version, e.RequestHash, r.Packet.ID, acceptance.Observation{Result: &protocol.AgentResult{ProtocolVersion: protocol.AgentResultVersion, Status: status, Summary: "fixture claim"}, ExecutionStopped: true}, false, acceptanceEvent())
	if err != nil || !valid {
		t.Fatalf("observe %+v valid=%v: %v", out, valid, err)
	}
	return out
}
func TestAcceptanceBeginIsExclusiveAndExactRetryIsIdempotent(t *testing.T) {
	s, r := acceptanceFixture(t)
	ctx := context.Background()
	second := r
	second.Packet.ID = "accept_2"
	second = prepareAcceptanceRequest(t, s, second)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	created := make(chan bool, 2)
	for _, r := range []acceptance.Request{r, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, fresh, err := s.BeginAcceptance(ctx, r, acceptanceEvent())
			created <- fresh
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	close(created)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent starts=%d", successes)
	}
	latest, err := s.LatestAcceptance(ctx, r.Packet.GoalID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID == acceptance.EffectID(second.Packet.ID) {
		r = second
	}
	replay, fresh, err := s.BeginAcceptance(ctx, r, acceptanceEvent())
	if err != nil || fresh || replay.ID != latest.ID {
		t.Fatalf("replay %+v fresh=%v: %v", replay, fresh, err)
	}
	if err := s.CheckExecutionAvailable(ctx); !errors.Is(err, ErrCheckoutBusy) {
		t.Fatalf("effect failed to hold slot: %v", err)
	}
	owner := supervisor.Owner{Kind: "attempt", ID: r.Packet.OwnerAttemptID, GoalID: r.Packet.GoalID, Generation: r.Packet.OwnerGeneration}
	if err := s.BeginProcess(ctx, supervisor.ProcessIntent{ID: "scenario-command", Owner: owner}); err != nil {
		t.Fatalf("same-owner scenario denied: %v", err)
	}
	if err := s.FinishProcess(ctx, "scenario-command", supervisor.ProcessExited, "done"); err != nil {
		t.Fatal(err)
	}
	_ = observeAcceptance(t, s, latest, r, protocol.ResultCompleted)
}
func TestAcceptanceRejectsStaleBindingsAndLateCancelOnlyRecordsHistory(t *testing.T) {
	for _, name := range []string{"goal_version", "generation", "tree", "config", "revision"} {
		t.Run(name, func(t *testing.T) {
			s, r := acceptanceFixture(t)
			r.Packet.ID = "changed"
			switch name {
			case "goal_version":
				r.GoalVersion++
			case "generation":
				r.Packet.OwnerGeneration++
			case "tree":
				r.Packet.TreeHash = strings.Repeat("f", 64)
			case "config":
				r.Packet.ConfigHash = strings.Repeat("f", 64)
			case "revision":
				r.Packet.GoalRevisionHash = strings.Repeat("f", 64)
			}
			r = prepareAcceptanceRequest(t, s, r)
			if _, _, err := s.BeginAcceptance(context.Background(), r, acceptanceEvent()); err == nil {
				t.Fatal("stale request accepted")
			}
		})
	}
	s, r := acceptanceFixture(t)
	ctx := context.Background()
	e, _, err := s.BeginAcceptance(ctx, r, acceptanceEvent())
	if err != nil {
		t.Fatal(err)
	}
	obs := acceptance.Observation{Result: &protocol.AgentResult{ProtocolVersion: protocol.AgentResultVersion, Status: protocol.ResultCompleted, Summary: "done"}, ExecutionStopped: true}
	for _, v := range []struct {
		version          int64
		invocation, hash string
	}{{e.Version + 1, r.Packet.ID, e.RequestHash}, {e.Version, "old", e.RequestHash}, {e.Version, r.Packet.ID, strings.Repeat("f", 64)}} {
		if _, _, err := s.ObserveAcceptance(ctx, e.ID, v.version, v.hash, v.invocation, obs, false, acceptanceEvent()); !errors.Is(err, basestore.ErrConflict) {
			t.Fatalf("stale observation: %v", err)
		}
	}
	if err := s.UpdateGoalState(ctx, r.Packet.GoalID, r.GoalVersion, domain.GoalCancelled, acceptanceEvent()); err != nil {
		t.Fatal(err)
	}
	ended, valid, err := s.ObserveAcceptance(ctx, e.ID, e.Version, e.RequestHash, r.Packet.ID, obs, false, acceptanceEvent())
	if err != nil || valid || ended.State != domain.EffectSucceeded {
		t.Fatalf("late observation %+v %v %v", ended, valid, err)
	}
	goal, _ := s.Goal(ctx, r.Packet.GoalID)
	if goal.State != domain.GoalCancelled {
		t.Fatal("late result resumed goal")
	}
	if _, _, err := s.BeginAcceptance(ctx, r, acceptanceEvent()); err != nil {
		t.Fatalf("historical exact replay: %v", err)
	}
}

func TestAcceptanceReplayDecisionIsConsumedWithNewEffectOrNotAtAll(t *testing.T) {
	ctx := context.Background()
	s, r := acceptanceFixture(t)
	e, _, err := s.BeginAcceptance(ctx, r, acceptanceEvent())
	if err != nil {
		t.Fatal(err)
	}
	_, valid, err := s.ObserveAcceptance(ctx, e.ID, e.Version, e.RequestHash, r.Packet.ID, acceptance.Observation{Result: &protocol.AgentResult{ProtocolVersion: protocol.AgentResultVersion, Status: protocol.ResultBlocked, Summary: "provide a test account", Blockers: []string{"which account?"}}, ExecutionStopped: true}, false, acceptanceEvent())
	if err != nil || valid {
		t.Fatalf("blocked result: %v %v", valid, err)
	}
	gate, err := s.Gate(ctx, "gate_"+e.ID)
	if err != nil {
		t.Fatal(err)
	}
	gate, err = s.DecideGate(ctx, gate.ID, gate.Version, domain.GateAllow, "operator", "use fixture account", acceptanceEvent())
	if err != nil {
		t.Fatal(err)
	}
	goal, _ := s.Goal(ctx, r.Packet.GoalID)
	if err := s.UpdateGoalState(ctx, goal.ID, goal.Version, domain.GoalRunning, acceptanceEvent()); err != nil {
		t.Fatal(err)
	}
	goal, _ = s.Goal(ctx, goal.ID)
	if err := s.UpdateGoalState(ctx, goal.ID, goal.Version, domain.GoalVerifying, acceptanceEvent()); err != nil {
		t.Fatal(err)
	}
	goal, _ = s.Goal(ctx, goal.ID)
	next := r
	next.GoalVersion = goal.Version
	next.PreviousInvocationID = r.Packet.ID
	next.Packet.ID = "accept_retry"
	next.Packet.Decisions = []protocol.PacketDecision{{GateID: gate.ID, GateVersion: gate.Version, Answer: gate.DecisionReason}}
	next = prepareAcceptanceRequest(t, s, next)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_acceptance BEFORE INSERT ON effects WHEN NEW.effect_type='acceptance' BEGIN SELECT RAISE(ABORT,'injected crash before commit'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginAcceptance(ctx, next, acceptanceEvent()); err == nil {
		t.Fatal("injected commit succeeded")
	}
	unchanged, _ := s.Gate(ctx, gate.ID)
	if unchanged.Used != 0 || unchanged.Version != gate.Version {
		t.Fatal("rolled-back request consumed decision")
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_acceptance`); err != nil {
		t.Fatal(err)
	}
	created, fresh, err := s.BeginAcceptance(ctx, next, acceptanceEvent())
	if err != nil || !fresh {
		t.Fatalf("authorized retry %+v: %v", created, err)
	}
	consumed, _ := s.Gate(ctx, gate.ID)
	if consumed.Used != 1 || consumed.Version != gate.Version+1 {
		t.Fatal("decision not consumed atomically")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, s.Info().ProjectDir, clock.NewFake(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, fresh, err := reopened.BeginAcceptance(ctx, next, acceptanceEvent())
	if err != nil || fresh || replay.ID != created.ID {
		t.Fatalf("restart exact retry: %+v %v %v", replay, fresh, err)
	}
	observeAcceptance(t, reopened, replay, next, protocol.ResultCompleted)
	other := next
	other.Packet.ID = "accept_third"
	other.PreviousInvocationID = next.Packet.ID
	other = prepareAcceptanceRequest(t, reopened, other)
	if _, _, err := reopened.BeginAcceptance(ctx, other, acceptanceEvent()); !errors.Is(err, basestore.ErrAuthorizationDenied) {
		t.Fatalf("old decision started another invocation: %v", err)
	}
}

func TestAcceptanceRecoveryPreservesClaimsAndUnknownProcesses(t *testing.T) {
	for _, state := range []string{"interrupted", "blocked", "failed", "unknown", "paused", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			s, r := acceptanceFixture(t)
			r.ReplaySafe = true
			// Replay safety changes the request but not packet identity; no packet rewrite.
			e, _, err := s.BeginAcceptance(ctx, r, acceptanceEvent())
			if err != nil {
				t.Fatal(err)
			}
			o := acceptance.Observation{ExecutionStopped: true, FailureCode: "acceptance_interrupted", Reason: "daemon restart"}
			if state == "blocked" || state == "failed" {
				o.Result = &protocol.AgentResult{ProtocolVersion: protocol.AgentResultVersion, Status: protocol.ResultStatus(state), Summary: "preserved provider claim"}
				o.FailureCode = ""
			}
			if state == "unknown" {
				owner := supervisor.Owner{Kind: "attempt", ID: r.Packet.OwnerAttemptID, GoalID: r.Packet.GoalID, Generation: r.Packet.OwnerGeneration}
				if err := s.BeginProcess(ctx, supervisor.ProcessIntent{ID: acceptance.ProcessID(r.Packet.ID), Owner: owner}); err != nil {
					t.Fatal(err)
				}
				if err := s.FinishProcess(ctx, acceptance.ProcessID(r.Packet.ID), supervisor.ProcessUnknown, "unverifiable"); err != nil {
					t.Fatal(err)
				}
			}
			if state == "paused" || state == "cancelled" {
				target := domain.GoalWaiting
				if state == "cancelled" {
					target = domain.GoalCancelled
				}
				if err := s.UpdateGoalState(ctx, r.Packet.GoalID, r.GoalVersion, target, acceptanceEvent()); err != nil {
					t.Fatal(err)
				}
			}
			ended, valid, err := s.ObserveAcceptance(ctx, e.ID, e.Version, e.RequestHash, r.Packet.ID, o, true, acceptanceEvent())
			if err != nil || valid {
				t.Fatalf("recovery %+v %v %v", ended, valid, err)
			}
			goal, _ := s.Goal(ctx, r.Packet.GoalID)
			if state == "unknown" {
				if ended.State != domain.EffectRecovering || s.CheckExecutionAvailable(ctx) == nil {
					t.Fatal("unknown process barrier lost")
				}
				return
			}
			if state == "blocked" || state == "failed" {
				if !strings.Contains(string(ended.ObservationJSON), `"status":"`+state+`"`) || goal.State != domain.GoalWaiting {
					t.Fatal("structured claim lost")
				}
				return
			}
			if state == "paused" || state == "cancelled" {
				if !strings.Contains(string(ended.ObservationJSON), `"historical":true`) {
					t.Fatal("late history lost")
				}
				return
			}
			next := r
			next.Packet.ID = "recovered"
			next.PreviousInvocationID = r.Packet.ID
			next = prepareAcceptanceRequest(t, s, next)
			if _, fresh, err := s.BeginAcceptance(ctx, next, acceptanceEvent()); err != nil || !fresh {
				t.Fatalf("declared safe replay: %v %v", fresh, err)
			}
		})
	}
}

func TestAcceptanceRecoveringPersistsLateBlockedClaimAndCannotReviveHistory(t *testing.T) {
	for _, mode := range []string{"late-blocked", "historical"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, r := acceptanceFixture(t)
			r.ReplaySafe = true
			e, _, err := s.BeginAcceptance(ctx, r, acceptanceEvent())
			if err != nil {
				t.Fatal(err)
			}
			owner := supervisor.Owner{Kind: "attempt", ID: r.Packet.OwnerAttemptID, GoalID: r.Packet.GoalID, Generation: r.Packet.OwnerGeneration}
			id := acceptance.ProcessID(r.Packet.ID)
			if err := s.BeginProcess(ctx, supervisor.ProcessIntent{ID: id, Owner: owner}); err != nil {
				t.Fatal(err)
			}
			if err := s.FinishProcess(ctx, id, supervisor.ProcessUnknown, "identity unknown"); err != nil {
				t.Fatal(err)
			}
			var gate domain.Gate
			if mode == "historical" {
				gate, err = s.CreateGate(ctx, GateDraft{ID: "separate_gate", GoalID: r.Packet.GoalID, ReasonCode: "scope", Facts: map[string]any{}, Unknowns: []string{}, Options: []string{"allow", "deny"}, Recommendation: "inspect", Action: domain.ActionExecCommand, Scope: []string{"test"}, ExpiresAt: s.source.Now().Add(time.Hour), MaxUses: 1, Required: true, Revocable: true}, acceptanceEvent())
				if err != nil {
					t.Fatal(err)
				}
			}
			e, _, err = s.ObserveAcceptance(ctx, e.ID, e.Version, e.RequestHash, r.Packet.ID, acceptance.Observation{ExecutionStopped: false, FailureCode: "acceptance_interrupted", Reason: "not yet confirmed"}, true, acceptanceEvent())
			if err != nil {
				t.Fatal(err)
			}
			initialVersion := e.Version
			status := protocol.ResultBlocked
			if mode == "historical" {
				status = protocol.ResultCompleted
			}
			e, _, err = s.ObserveAcceptance(ctx, e.ID, e.Version, e.RequestHash, r.Packet.ID, acceptance.Observation{ExecutionStopped: false, Result: &protocol.AgentResult{ProtocolVersion: protocol.AgentResultVersion, Status: status, Summary: "late durable claim"}}, false, acceptanceEvent())
			if err != nil {
				t.Fatal(err)
			}
			if e.Version <= initialVersion || !strings.Contains(string(e.ObservationJSON), `"status":"`+string(status)+`"`) {
				t.Fatalf("late claim was acknowledged but lost: %+v", e)
			}
			if mode == "historical" {
				if _, err := s.DecideGate(ctx, gate.ID, gate.Version, domain.GateAllow, "human", "allowed", acceptanceEvent()); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.FinishProcess(ctx, id, supervisor.ProcessTerminated, "exit now confirmed"); err != nil {
				t.Fatal(err)
			}
			ended, valid, err := s.ObserveAcceptance(ctx, e.ID, e.Version, e.RequestHash, r.Packet.ID, acceptance.Observation{ExecutionStopped: true, FailureCode: "acceptance_interrupted", Reason: "recovery"}, mode != "historical", acceptanceEvent())
			if err != nil || valid {
				t.Fatalf("historical or blocked advanced validators %+v %v %v", ended, valid, err)
			}
			if !strings.Contains(string(ended.ObservationJSON), `"status":"`+string(status)+`"`) {
				t.Fatal("recovery erased a persisted complete claim")
			}
			if mode == "historical" && !strings.Contains(string(ended.ObservationJSON), `"historical":true`) {
				t.Fatal("old history became current")
			}
			if mode == "late-blocked" {
				goal, _ := s.Goal(ctx, r.Packet.GoalID)
				if goal.State != domain.GoalWaiting {
					t.Fatal("replaySafe ignored blocked claim")
				}
			}
		})
	}
}

func TestFinalizationCannotOmitConfiguredAcceptanceSession(t *testing.T) {
	ctx := context.Background()
	source := clock.NewFake(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	goal, facts, files := seedFinalizableReport(t, s, s.Info().ProjectDir, source)
	e, _, err := s.RequestEffect(ctx, EffectRequest{ID: "acceptance_required_planner", Key: "acceptance_required_planner", Type: "planner", Request: map[string]any{"config_hash": strings.Repeat("b", 64), "validation_capabilities": map[string]any{"required_scenario_ids": []string{"inspect"}}}}, acceptanceEvent())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE goals SET planning_effect_id=? WHERE id=?`, e.ID, goal.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FinalizeGoal(ctx, goal.ID, goal.Version, facts, files, acceptanceEvent()); err == nil || !strings.Contains(err.Error(), "Acceptance invocation is missing") {
		t.Fatalf("missing Acceptance bypassed completion: %v", err)
	}
	current, _ := s.Goal(ctx, goal.ID)
	if current.State != domain.GoalVerifying {
		t.Fatal("unobserved acceptance completed Goal")
	}
}
