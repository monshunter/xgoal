package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/harness"
	"github.com/monshunter/xgoal/internal/planner"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/supervisor"
)

const maxPlannerOutput = int64(8 << 20)

func (engine *Engine) currentPlanningConfiguration(expected string) (string, error) {
	current, err := config.LoadFile(filepath.Join(engine.projectRoot, "xgoal.yaml"))
	if err != nil {
		return "", errors.Join(sqlite.ErrConfigurationChanged, err)
	}
	hash, err := current.Hash()
	if err != nil {
		return "", err
	}
	if hash != engine.configHash || hash != expected {
		return hash, sqlite.ErrConfigurationChanged
	}
	return hash, nil
}

// runPlanning runs only while RunGoal owns the project execution slot. Every
// transition is fenced in Store; the queue and cancellation maps carry no state.
func (engine *Engine) runPlanning(ctx context.Context, goalID string) error {
	record, err := engine.store.Planning(ctx, goalID)
	if errors.Is(err, basestore.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.Goal.State == domain.GoalCancelled || record.Goal.ActiveRevisionID != "" || record.Paused || record.State == "WAITING" {
		return nil
	}
	configHash, err := engine.currentPlanningConfiguration(record.Request.ConfigHash)
	if err != nil {
		return engine.failPlanning(ctx, record, "configuration_changed", err, true)
	}
	if record.Effect.State == domain.EffectExecuting || record.Effect.State == domain.EffectRecovering {
		if err := engine.store.RecoverPlanning(ctx, sqlite.PlanningRecoveryOptions{CurrentConfigHash: configHash, NoProgressLimit: engine.config.Orchestration.NoProgressLimit}); err != nil {
			return err
		}
		record, err = engine.store.Planning(ctx, goalID)
		if err != nil {
			return err
		}
		if record.Paused || record.State == "WAITING" || record.Goal.State == domain.GoalCancelled {
			return nil
		}
	}
	if record.Effect.State == domain.EffectRequested {
		identity, err := engine.repository.ReadCheckoutIdentity(ctx)
		if err != nil {
			return engine.failPlanning(ctx, record, "planning_input_unavailable", err, true)
		}
		baseTree := identity.HeadTree
		if checkout, readErr := engine.store.Checkout(ctx); readErr == nil {
			baseTree = checkout.AcceptedTree
		} else if !errors.Is(readErr, basestore.ErrNotFound) {
			return readErr
		}
		snapshot, err := engine.repository.SnapshotTree(ctx, gitrepo.SnapshotSpec{BaseTree: baseTree, ExcludePaths: []string{engine.runtimeRoot}, MaxFileBytes: maxPatchFile})
		if err != nil {
			return engine.failPlanning(ctx, record, "planning_input_unavailable", err, true)
		}
		invocationID, err := randomID("planner")
		if err != nil {
			return err
		}
		record, err = engine.store.BeginPlanning(ctx, sqlite.PlanningClaim{GoalID: goalID, EffectID: record.Effect.ID, Generation: record.Generation, ExpectedGoalVersion: record.Goal.Version, ExpectedEffectVersion: record.Effect.Version, CurrentConfigHash: configHash, InvocationID: invocationID, InputTree: snapshot.Tree, CheckoutIdentity: snapshot.Identity})
		if err != nil {
			if errors.Is(err, sqlite.ErrCheckoutBusy) {
				current, readErr := engine.store.Planning(ctx, goalID)
				if readErr != nil {
					return readErr
				}
				return engine.failPlanning(ctx, current, "project_execution_blocked", err, true)
			}
			return engine.planningControlRace(ctx, goalID, err)
		}
		invocationContext := supervisor.WithOwner(ctx, engine.store, supervisor.Owner{Kind: "planning", ID: record.Effect.ID, GoalID: goalID, Generation: record.Generation})
		var execution planner.Execution
		if record.Request.Proposal != nil {
			execution.Proposal = *record.Request.Proposal
		} else {
			execution, err = engine.invokePlanner(invocationContext, record, invocationID)
		}
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if errors.Is(err, supervisor.ErrProcessUnconfirmed) {
			return engine.failPlanning(cleanupContext, record, "planning_process_unconfirmed", err, false)
		}
		if configErr := engine.planningConfigGuard(record); configErr != nil {
			return engine.failPlanning(cleanupContext, record, "configuration_changed", configErr, true)
		}
		if snapshotErr := engine.checkPlanningInput(cleanupContext, record); snapshotErr != nil {
			return engine.failPlanning(cleanupContext, record, "planning_input_changed", snapshotErr, true)
		}
		if err != nil {
			code := "planner_failed"
			if errors.Is(err, harness.ErrRequired) {
				code = "project_harness_required"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				code = "planner_timeout"
			}
			if errors.Is(err, context.Canceled) {
				code = "planner_interrupted"
			}
			return engine.failPlanning(cleanupContext, record, code, err, true)
		}
		record, err = engine.store.PersistPlanningProposal(cleanupContext, sqlite.PlanningResult{GoalID: goalID, EffectID: record.Effect.ID, Generation: record.Generation, ExpectedEffectVersion: record.Effect.Version, InvocationID: invocationID, Proposal: execution.Proposal, SessionID: execution.SessionID})
		if err != nil {
			return engine.planningControlRace(cleanupContext, goalID, err)
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	// Resume from a reliable observation repeats only deterministic checks and
	// the atomic publish, never provider execution or mutable packet creation.
	record, err = engine.store.Planning(ctx, goalID)
	if err != nil {
		return err
	}
	if record.Paused || record.Goal.State == domain.GoalCancelled || record.Goal.ActiveRevisionID != "" || record.State == "WAITING" {
		return nil
	}
	if record.Effect.State != domain.EffectObserving || record.Observation == nil || record.Observation.Proposal == nil {
		return nil
	}
	if err := engine.planningConfigGuard(record); err != nil {
		return engine.failPlanning(ctx, record, "configuration_changed", err, true)
	}
	if err := engine.checkPlanningInput(ctx, record); err != nil {
		return engine.failPlanning(ctx, record, "planning_input_changed", err, true)
	}
	_, err = engine.store.PublishPlanning(ctx, sqlite.PlanningPublish{GoalID: goalID, EffectID: record.Effect.ID, Generation: record.Generation, ExpectedGoalVersion: record.Goal.Version, ExpectedEffectVersion: record.Effect.Version, ObservationHash: record.Effect.ObservationHash, CurrentConfigHash: configHash})
	if err != nil {
		if errors.Is(err, basestore.ErrConflict) || errors.Is(err, sqlite.ErrPlanningBlocked) {
			return engine.planningControlRace(ctx, goalID, err)
		}
		return engine.failPlanning(ctx, record, "planner_invalid_proposal", err, true)
	}
	return nil
}

func (engine *Engine) invokePlanner(ctx context.Context, record sqlite.PlanningRecord, invocationID string) (planner.Execution, error) {
	profile, exists := engine.profiles[record.Request.ProfileID]
	if !exists {
		return planner.Execution{}, errors.New("frozen Planner profile is unavailable; restart and explicitly use goal plan")
	}
	runtimeAdapter := engine.adapters[profile.ID]
	plannerAdapter, ok := runtimeAdapter.(planner.Adapter)
	if !ok {
		return planner.Execution{}, errors.New("selected adapter has no Planner contract")
	}
	harnessInput, err := harness.Discover(engine.projectRoot, profile.Adapter, engine.config.Project.Harness)
	if err != nil {
		return planner.Execution{}, err
	}
	probeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	capabilities, err := runtimeAdapter.Probe(probeContext, adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: profile.ID, Timeout: 10 * time.Second})
	cancel()
	if err != nil {
		return planner.Execution{}, err
	}
	effective, err := profile.Effective("planner", capabilities.Version)
	if err != nil {
		return planner.Execution{}, err
	}
	packetPath, packetHash, err := planner.PrepareInvocation(engine.runtimeRoot, invocationID, record.Generation, planner.Packet{Harness: &harnessInput, ProtocolVersion: planner.PacketVersion, GoalID: record.Goal.ID, RawGoal: record.Request.RawGoal, Mode: record.Request.Mode, ConfigHash: record.Request.ConfigHash, TrustedValidators: record.Request.TrustedValidatorIDs, ProjectRoot: engine.projectRoot, ProjectNetwork: engine.config.Runtime.ProjectNetwork, ProjectSecrets: engine.config.Runtime.ProjectSecrets})
	if err != nil {
		return planner.Execution{}, err
	}
	schema, err := planner.Schema()
	if err != nil {
		return planner.Execution{}, err
	}
	prompt := "Act as the read-only xgoal Planner. Read the immutable Planner Packet at " + packetPath + `.
Inspect only the current repository. Do not modify files or Git metadata. Record unresolved product or authorization questions in ambiguities.
Return a bounded Goal Contract and acyclic Work Graph using the packet's trusted validator IDs. When there are no ambiguities:
- Provide a non-empty summary and rationale and at least one specific, unique entry in every Contract list, including quality_attributes and human_gates.
- Human gates describe conditional approval boundaries from the project policy, such as scope expansion; they do not require unnecessary approval for already authorized work.
- Set all three completion_policy requirements to true.
- Every acceptance criterion must name trusted Validators that actually prove its statement, or require explicit human_acceptance for a human decision in the goal. Do not invent Validator IDs or claim checks they do not perform. Scope, policy and independent-review requirements belong in constraints; do not turn them into extra criteria with no Validator or human acceptance.
- Each criterion must be covered by a required Work Item. Each Work Item needs non-empty acceptance_criteria and validators, role implementer, and explicit read_scope and write_scope.
- Scopes are repository-root-anchored patterns beginning with /, such as /output.txt or /src/**, not filesystem absolute paths or unprefixed relative paths. Never include Git metadata or path traversal.
- Use unique client_key values and criterion IDs, reference only existing dependencies and criteria, and order Work Items with overlapping write scopes.
- Work Item acceptance_criteria contains criterion IDs, never statement text. For example, contract.acceptance_criteria [{"id":"AC-1","statement":"output.txt has the required bytes","validators":["output-check"],"human_acceptance":false}] is referenced by work_items acceptance_criteria ["AC-1"]. Use the actual trusted validator IDs from the packet, not the example ID.
If a verifiable plan cannot be formed within the supplied goal and trusted validators, explain the gap in ambiguities instead of inventing coverage.`
	invocation := planner.Invocation{RequestHash: record.Effect.RequestHash, InputTree: record.Observation.InputTree, Generation: record.Generation, ExecutionConfig: &effective, InvocationID: invocationID, ProfileID: profile.ID, WorkDir: engine.projectRoot, PacketPath: packetPath, PacketHash: packetHash, Prompt: prompt, OutputSchema: schema, Environment: profileEnvironment(profile), Timeout: profile.Timeout.Duration, MaxOutputBytes: maxPlannerOutput}
	providerContext, stop := context.WithTimeout(ctx, invocation.Timeout)
	defer stop()
	execution, err := plannerAdapter.Plan(providerContext, invocation, nil)
	if err != nil {
		return execution, err
	}
	if len(execution.Proposal.Ambiguities) != 0 {
		return execution, fmt.Errorf("Planner requires clarification: %s", strings.Join(execution.Proposal.Ambiguities, "; "))
	}
	return execution, nil
}

func (engine *Engine) checkPlanningInput(ctx context.Context, record sqlite.PlanningRecord) error {
	if record.Observation == nil {
		return errors.New("planning input observation is missing")
	}
	return engine.repository.CheckSnapshot(ctx, gitrepo.SnapshotSpec{BaseTree: record.Observation.InputTree, ExcludePaths: []string{engine.runtimeRoot}, MaxFileBytes: maxPatchFile}, record.Observation.CheckoutIdentity, record.Observation.InputTree)
}
func (engine *Engine) planningConfigGuard(record sqlite.PlanningRecord) error {
	_, err := engine.currentPlanningConfiguration(record.Request.ConfigHash)
	return err
}

func (engine *Engine) failPlanning(ctx context.Context, record sqlite.PlanningRecord, code string, cause error, stopped bool) error {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, err := engine.store.FailPlanning(cleanupContext, sqlite.PlanningFailure{GoalID: record.Goal.ID, EffectID: record.Effect.ID, Generation: record.Generation, ExpectedEffectVersion: record.Effect.Version, Code: code, Reason: cause.Error(), ExecutionStopped: stopped})
	if err != nil {
		return engine.planningControlRace(cleanupContext, record.Goal.ID, err)
	}
	return nil
}

func (engine *Engine) planningControlRace(ctx context.Context, goalID string, cause error) error {
	current, err := engine.store.Planning(ctx, goalID)
	if err == nil && (current.Paused || current.Goal.State == domain.GoalCancelled || current.State == "WAITING") {
		return nil
	}
	return cause
}
