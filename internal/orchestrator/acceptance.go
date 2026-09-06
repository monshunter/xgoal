package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/monshunter/xgoal/internal/acceptance"
	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/harness"
	callindex "github.com/monshunter/xgoal/internal/invocation"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/redact"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/supervisor"
	"github.com/monshunter/xgoal/internal/validator"
	"github.com/monshunter/xgoal/internal/workspace"
)

// Acceptance has no Work, Patch or Promotion authority. The last successful
// Attempt is only its physical process owner, not an implementation retry.
func (engine *Engine) invokeAcceptance(ctx context.Context, goal domain.Goal, revision domain.GoalRevision, attempt domain.Attempt, generation int64, snapshot workspace.Snapshot, handle environment.Handle, registry *validator.Registry, scenarios []config.Scenario) (domain.Effect, bool, error) {
	var empty domain.Effect
	profile, _, err := engine.config.SelectProfile("acceptance", "")
	if err != nil {
		return empty, false, err
	}
	runtime := engine.adapters[profile.ID]
	acceptor, ok := runtime.(acceptance.Adapter)
	if !ok {
		return empty, false, errors.New("selected profile has no Acceptance contract")
	}
	if err := registry.VerifyControl(snapshot.Path, "acceptance/client"); err != nil {
		return empty, false, err
	}
	input, err := harness.Discover(snapshot.Path, profile.Adapter, engine.config.Project.Harness)
	if err != nil {
		return empty, false, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	capabilities, err := runtime.Probe(probeCtx, adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: profile.ID, Timeout: 10 * time.Second})
	cancel()
	if err != nil {
		return empty, false, err
	}
	effective, err := profile.Effective("acceptance", capabilities.Version)
	if err != nil {
		return empty, false, err
	}
	id, err := randomID("acceptance")
	if err != nil {
		return empty, false, err
	}
	packet := acceptance.Packet{ProtocolVersion: acceptance.PacketVersion, ID: id, GoalID: goal.ID, GoalRevisionHash: revision.Hash, ConfigHash: engine.configHash, TreeHash: snapshot.InputTree, ProfileID: profile.ID, OwnerAttemptID: attempt.ID, OwnerGeneration: generation, Workspace: snapshot.Path, EnvironmentID: handle.ID, ScenarioDir: filepath.Join(handle.Root, "scenario"), ProjectNetwork: engine.config.Runtime.ProjectNetwork, Harness: &input}
	for _, scenario := range scenarios {
		if slices.Contains(engine.config.Acceptance.ScenarioIDs, scenario.ID) {
			packet.Scenarios = append(packet.Scenarios, scenario)
			packet.RuntimeServices = append(packet.RuntimeServices, scenario.Services...)
			for _, id := range scenario.Validators {
				d, _ := registry.Definition(id)
				packet.RuntimeServices = append(packet.RuntimeServices, d.Services...)
			}
		}
	}
	packet.RuntimeServices = uniqueSorted(packet.RuntimeServices)
	previous := ""
	prior, err := engine.store.LatestAcceptance(ctx, goal.ID)
	if err == nil {
		var old acceptance.Request
		if err := json.Unmarshal(prior.RequestJSON, &old); err != nil {
			return empty, false, err
		}
		if old.Packet.GoalRevisionHash == revision.Hash && old.Packet.ConfigHash == engine.configHash && old.Packet.TreeHash == snapshot.InputTree {
			previous = old.Packet.ID
			var observation acceptance.Observation
			if err := json.Unmarshal(prior.ObservationJSON, &observation); err != nil {
				return empty, false, err
			}
			packet.Prior = &acceptance.PriorContext{InvocationID: previous, ObservationHash: prior.ObservationHash, Observation: observation}
			gate, err := engine.store.Gate(ctx, "gate_"+prior.ID)
			if err == nil && gate.State == domain.GateApproved && gate.Decision == domain.GateAllow && gate.Used == 0 {
				packet.Decisions = []protocol.PacketDecision{{GateID: gate.ID, GateVersion: gate.Version, Answer: redact.String(gate.DecisionReason)}}
			} else if err != nil && !errors.Is(err, basestore.ErrNotFound) {
				return empty, false, err
			}
		}
	} else if !errors.Is(err, basestore.ErrNotFound) {
		return empty, false, err
	}
	path, hash, err := acceptance.Prepare(engine.runtimeRoot, packet)
	if err != nil {
		return empty, false, err
	}
	limit := engine.config.Orchestration.NoProgressLimit
	if limit <= 0 {
		limit = 3
	}
	if limit > 100 {
		limit = 100
	}
	request := acceptance.Request{Packet: packet, PacketPath: path, PacketHash: hash, ExecutionConfig: effective, GoalVersion: goal.Version, PreviousInvocationID: previous, ReplaySafe: engine.config.Acceptance.ReplaySafe, RecoveryLimit: limit}
	effect, fresh, err := engine.store.BeginAcceptance(ctx, request, event("AcceptanceStarted", "kernel", map[string]any{"goal_id": goal.ID, "invocation_id": id, "role": "acceptance"}))
	if err != nil {
		return empty, false, err
	}
	if !fresh {
		return effect, false, errors.New("acceptance invocation already started")
	}
	schema, err := protocol.Schema(protocol.SchemaAgentResult)
	if err != nil {
		return effect, false, err
	}
	values := profileEnvironment(profile)
	values["XGOAL_SCENARIO_DIR"] = filepath.Join(handle.Root, "scenario")
	values["XGOAL_ENVIRONMENT_ID"] = handle.ID
	prompt := "Act as the xgoal Acceptance worker. Read the immutable Acceptance Packet at " + path + `. Execute only the listed scenario steps with the profile's explicitly allowed tools. The Kernel has prepared the environment and owns starting and stopping services. XGOAL_SCENARIO_DIR and XGOAL_ENVIRONMENT_ID are already inherited by your tools and match the Packet. Invoke the listed trusted client commands exactly, without export, inline environment assignments, wrappers or compound shell commands; those changes may not match the allowed tool rules. Read source as needed, but do not write source, scripts, configuration, Git metadata or xgoal state. Only approved test data and scenario outputs may change. Use the trusted client scripts already in the repository; do not invent shell commands or tools. The packet's decisions are scoped answers, not new authority. Report completed, blocked or failed as an AgentResult. If any account, permission or external condition is missing, return blocked with the precise question. Do not request native CLI approval or wait for interactive input. Report observations honestly; your result is a Claim and the Kernel will independently run the business assertions.`
	invocation := acceptance.Invocation{InvocationID: id, ProfileID: profile.ID, GoalRevisionHash: revision.Hash, ConfigHash: engine.configHash, TreeHash: snapshot.InputTree, PacketPath: path, PacketHash: hash, WorkDir: snapshot.Path, Prompt: prompt, OutputSchema: schema, ExecutionConfig: &effective, Environment: values, Timeout: profile.Timeout.Duration, MaxOutputBytes: maxAgentOutput}
	tracker, err := engine.beginInvocation(ctx, callindex.Input{ID: id, GoalID: goal.ID, OwnerKind: "final", OwnerID: effect.ID, Generation: generation, Role: "acceptance", ProfileID: profile.ID, Provider: profile.Adapter, GoalRevisionHash: revision.Hash, InputTree: snapshot.InputTree, PacketHash: hash, Prompt: prompt, ExecutionConfig: effective}, path, schema)
	if err != nil {
		return effect, false, err
	}
	execution, callErr := acceptor.Accept(ctx, invocation, tracker.sink())
	callErr = tracker.finish(ctx, execution.SessionID, string(execution.Result.Status), callErr)
	cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	observation := acceptance.Observation{SessionID: execution.SessionID, ExecutionStopped: !errors.Is(callErr, supervisor.ErrProcessUnconfirmed)}
	if execution.Result.Validate() == nil {
		observation.Result = &execution.Result
	} else if claim, err := acceptance.ReadClaim(engine.runtimeRoot, request); err == nil {
		observation.Result = claim
	}
	if callErr != nil {
		observation.FailureCode = "acceptance_provider_failed"
		observation.Reason = callErr.Error()
	}
	trustErr := registry.VerifyControl(snapshot.Path, "acceptance/client")
	treeErr := engine.checkWorkspaceTree(cleanupCtx, snapshot, snapshot.InputTree)
	if err := errors.Join(trustErr, treeErr); err != nil {
		observation.FailureCode = "acceptance_source_changed"
		observation.Reason = err.Error()
	}
	if observation.Result == nil && observation.FailureCode == "" {
		observation.FailureCode = "acceptance_missing_result"
		observation.Reason = "Provider returned no valid AgentResult"
	}
	ended, current, err := engine.store.ObserveAcceptance(cleanupCtx, effect.ID, effect.Version, effect.RequestHash, id, observation, false, event("AcceptanceObserved", "kernel", map[string]any{"goal_id": goal.ID, "invocation_id": id}))
	if err != nil {
		return effect, false, err
	}
	return ended, current, nil
}

func (engine *Engine) acceptanceReady(ctx context.Context, goal domain.Goal, revision domain.GoalRevision, tree string) (bool, error) {
	if engine.config.Acceptance == nil {
		return true, nil
	}
	prior, err := engine.store.LatestAcceptance(ctx, goal.ID)
	if errors.Is(err, basestore.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var request acceptance.Request
	if err := json.Unmarshal(prior.RequestJSON, &request); err != nil {
		return false, err
	}
	if request.Packet.GoalRevisionHash != revision.Hash || request.Packet.ConfigHash != engine.configHash || request.Packet.TreeHash != tree {
		return true, nil
	}
	if err := engine.store.RequireAcceptanceReplay(ctx, prior.ID); err != nil {
		return false, err
	}
	current, err := engine.store.Goal(ctx, goal.ID)
	return current.State == domain.GoalVerifying, err
}

func (engine *Engine) recoverAcceptance(ctx context.Context) error {
	effects, err := engine.store.RecoverableEffects(ctx)
	if err != nil {
		return err
	}
	for _, effect := range effects {
		if effect.Type != "acceptance" {
			continue
		}
		var request acceptance.Request
		if err := json.Unmarshal(effect.RequestJSON, &request); err != nil {
			return err
		}
		observation := acceptance.Observation{ExecutionStopped: true, FailureCode: "acceptance_interrupted", Reason: "daemon recovered an unfinished Acceptance invocation"}
		// A complete blocked/failed result survives the result-file/Gate crash window.
		claim, claimErr := acceptance.ReadClaim(engine.runtimeRoot, request)
		if claim != nil {
			observation.Result = claim
			observation.FailureCode = ""
			observation.Reason = "complete claim recovered; process outcome was interrupted"
		}
		if claimErr != nil && !errors.Is(claimErr, os.ErrNotExist) && !errors.Is(claimErr, acceptance.ErrNoClaim) {
			observation.FailureCode = "acceptance_artifact_invalid"
			observation.Reason = claimErr.Error()
		}
		_, _, err := engine.store.ObserveAcceptance(ctx, effect.ID, effect.Version, effect.RequestHash, request.Packet.ID, observation, true, event("AcceptanceRecovered", "kernel", map[string]any{"goal_id": request.Packet.GoalID, "invocation_id": request.Packet.ID}))
		if err != nil {
			return fmt.Errorf("recover acceptance %s: %w", effect.ID, err)
		}
	}
	return nil
}
