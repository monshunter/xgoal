package kernel

import (
	"context"
	"fmt"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/validator"
)

type StateStore interface {
	CreateGoal(domain.Goal, string) error
	Goal(string) (domain.Goal, error)
	UpdateGoalState(string, int64, domain.GoalState, string) error
	CreateWorkItem(domain.WorkItem, string) error
	WorkItem(string) (domain.WorkItem, error)
	UpdateWorkState(string, int64, domain.WorkState, string) error
	ClaimWork(string, int64, domain.Lease, domain.Attempt) error
	Attempt(string) (domain.Attempt, error)
	UpdateAttemptState(string, int64, domain.AttemptState, string) error
	ReleaseLease(string, int64) error
	AddEvidence(domain.Evidence, string) error
}

type Kernel struct {
	store     StateStore
	adapter   adapter.Adapter
	validator validator.Runner
	clock     clock.Clock
}

func New(state StateStore, runtime adapter.Adapter, validation validator.Runner, source clock.Clock) *Kernel {
	return &Kernel{store: state, adapter: runtime, validator: validation, clock: source}
}

type SimulationSpec struct {
	Packet                    protocol.WorkPacket
	AttemptID                 string
	LeaseID                   string
	AgentProfileID            string
	ConfigHash                string
	FinalTree                 string
	ScopePolicyPassed         bool
	FinalValidationSetCurrent bool
	FinalReportGenerated      bool
}

type SimulationOutcome struct {
	Completed    bool
	GoalState    domain.GoalState
	WorkState    domain.WorkState
	AttemptState domain.AttemptState
	AgentClaim   protocol.AgentResult
	Evidence     protocol.Evidence
	Reasons      []string
}

func (k *Kernel) RunSimulation(ctx context.Context, spec SimulationSpec) (SimulationOutcome, error) {
	if k.store == nil || k.adapter == nil || k.validator == nil || k.clock == nil {
		return SimulationOutcome{}, fmt.Errorf("kernel dependencies are required")
	}
	if err := spec.Packet.Validate(); err != nil {
		return SimulationOutcome{}, err
	}
	if spec.AttemptID == "" || spec.LeaseID == "" || spec.AgentProfileID == "" || spec.ConfigHash == "" || spec.FinalTree == "" {
		return SimulationOutcome{}, fmt.Errorf("simulation identities, config hash, and final tree are required")
	}

	goalID := spec.Packet.Goal.ID
	workID := spec.Packet.WorkItem.ID
	if err := k.store.CreateGoal(domain.Goal{ID: goalID, State: domain.GoalDraft, ActiveRevisionID: spec.Packet.Goal.ContractHash, Version: 1}, "GoalDrafted"); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.transitionGoal(goalID, domain.GoalReady, "GoalRevisionFrozen"); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.transitionGoal(goalID, domain.GoalRunning, "GoalRunning"); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.store.CreateWorkItem(domain.WorkItem{ID: workID, State: domain.WorkPending, Objective: spec.Packet.WorkItem.Objective, Required: true, Version: 1}, "WorkPending"); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.transitionWork(workID, domain.WorkReady, "WorkReady"); err != nil {
		return SimulationOutcome{}, err
	}

	packetHash, err := spec.Packet.Hash()
	if err != nil {
		return SimulationOutcome{}, err
	}
	work, err := k.store.WorkItem(workID)
	if err != nil {
		return SimulationOutcome{}, err
	}
	lease := domain.Lease{
		ID: spec.LeaseID, WorkItemID: workID, AttemptID: spec.AttemptID, Holder: "simulation-kernel",
		Generation: 1, State: domain.LeaseActive, AcquiredAt: k.clock.Now(), HeartbeatAt: k.clock.Now(),
		ExpiresAt: k.clock.Now().Add(90 * time.Second), Version: 1,
	}
	attempt := domain.Attempt{
		ID: spec.AttemptID, WorkItemID: workID, AgentProfileID: spec.AgentProfileID,
		State: domain.AttemptCreated, BaseTree: spec.Packet.Project.BaseTree, ResultTree: spec.FinalTree, PacketHash: packetHash, Version: 1,
	}
	if err := k.store.ClaimWork(workID, work.Version, lease, attempt); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.transitionWork(workID, domain.WorkRunning, "AttemptStarted"); err != nil {
		return SimulationOutcome{}, err
	}
	for _, transition := range []struct {
		state domain.AttemptState
		event string
	}{
		{domain.AttemptPreparing, "AttemptPreparing"},
		{domain.AttemptStarting, "AttemptStarting"},
		{domain.AttemptRunning, "AttemptStarted"},
	} {
		if err := k.transitionAttempt(spec.AttemptID, transition.state, transition.event); err != nil {
			return SimulationOutcome{}, err
		}
	}

	schema, err := protocol.Schema(protocol.SchemaAgentResult)
	if err != nil {
		return SimulationOutcome{}, err
	}
	handle, err := k.adapter.Start(ctx, adapter.Invocation{
		InvocationID: spec.AttemptID, AttemptID: spec.AttemptID, Role: spec.Packet.Role,
		WorkDir: spec.Packet.Project.Workspace, Prompt: "simulation", OutputSchema: schema,
	}, nil)
	if err != nil {
		return k.reconcile(spec, protocol.AgentResult{}, protocol.Evidence{}, err)
	}
	claim, err := k.adapter.Wait(ctx, handle)
	if err != nil {
		return k.reconcile(spec, protocol.AgentResult{}, protocol.Evidence{}, err)
	}
	if err := claim.Validate(); err != nil {
		return k.reconcile(spec, claim, protocol.Evidence{}, err)
	}
	if claim.Status != protocol.ResultCompleted {
		return k.reconcile(spec, claim, protocol.Evidence{}, fmt.Errorf("agent result is %s", claim.Status))
	}
	if err := k.transitionAttempt(spec.AttemptID, domain.AttemptCollecting, "AgentResultCollected"); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.transitionAttempt(spec.AttemptID, domain.AttemptValidating, "ValidationStarted"); err != nil {
		return SimulationOutcome{}, err
	}

	evidence, err := k.validator.Run(ctx, validator.Request{
		ValidatorID: spec.Packet.WorkItem.ValidatorIDs[0], SubjectID: workID,
		GoalRevisionHash: spec.Packet.Goal.ContractHash, ConfigHash: spec.ConfigHash, TreeHash: spec.FinalTree,
	})
	if err != nil || !trustedEvidence(evidence, workID, spec.Packet.Goal.ContractHash, spec.ConfigHash, spec.FinalTree) {
		if err == nil {
			err = fmt.Errorf("validator evidence is not current deterministic evidence bound to the simulation")
		}
		return k.reconcile(spec, claim, evidence, err)
	}
	if err := k.store.AddEvidence(domain.Evidence{
		ID: evidence.ID, Kind: evidence.Kind, SubjectID: evidence.SubjectID, Authority: evidence.Authority,
		GoalRevisionHash: evidence.GoalRevisionHash, ConfigHash: evidence.ConfigHash, TreeHash: evidence.TreeHash,
		PayloadHash: evidence.PayloadHash, State: evidence.State,
	}, "ValidationFinished"); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.transitionAttempt(spec.AttemptID, domain.AttemptPromoting, "PromotionStarted"); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.transitionAttempt(spec.AttemptID, domain.AttemptSucceeded, "PromotionCommitted"); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.transitionWork(workID, domain.WorkVerifying, "WorkVerifying"); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.transitionWork(workID, domain.WorkCompleted, "WorkCompleted"); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.store.ReleaseLease(workID, lease.Generation); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.transitionGoal(goalID, domain.GoalVerifying, "FinalValidationStarted"); err != nil {
		return SimulationOutcome{}, err
	}

	completion := EvaluateCompletion(CompletionInput{
		GoalState: domain.GoalVerifying, RequiredWorkStates: []domain.WorkState{domain.WorkCompleted},
		IntegrationTree: spec.FinalTree, ExpectedTree: spec.FinalTree,
		Criteria:          []CriterionStatus{{ID: spec.Packet.WorkItem.AcceptanceCriteria[0], Satisfied: true, Current: true, TreeHash: evidence.TreeHash}},
		ScopePolicyPassed: spec.ScopePolicyPassed, FinalValidationSetCurrent: spec.FinalValidationSetCurrent,
		FinalReportGenerated: spec.FinalReportGenerated,
	})
	if completion.Complete {
		if err := k.transitionGoal(goalID, domain.GoalCompleted, "GoalCompleted"); err != nil {
			return SimulationOutcome{}, err
		}
	}
	return k.outcome(spec, claim, evidence, completion), nil
}

func trustedEvidence(evidence protocol.Evidence, subjectID, goalHash, configHash, treeHash string) bool {
	return evidence.Validate() == nil && evidence.Authority == domain.AuthorityDeterministic && evidence.State == domain.EvidenceCurrent &&
		evidence.SubjectID == subjectID && evidence.GoalRevisionHash == goalHash && evidence.ConfigHash == configHash && evidence.TreeHash == treeHash
}

func (k *Kernel) reconcile(spec SimulationSpec, claim protocol.AgentResult, evidence protocol.Evidence, _ error) (SimulationOutcome, error) {
	if err := k.transitionAttempt(spec.AttemptID, domain.AttemptFailed, "AttemptFailed"); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.transitionWork(spec.Packet.WorkItem.ID, domain.WorkReconciling, "AttemptReconciled"); err != nil {
		return SimulationOutcome{}, err
	}
	if err := k.store.ReleaseLease(spec.Packet.WorkItem.ID, 1); err != nil {
		return SimulationOutcome{}, err
	}
	return k.outcome(spec, claim, evidence, CompletionResult{Complete: false, Reasons: []string{"trusted evidence closure failed"}}), nil
}

func (k *Kernel) outcome(spec SimulationSpec, claim protocol.AgentResult, evidence protocol.Evidence, completion CompletionResult) SimulationOutcome {
	goal, _ := k.store.Goal(spec.Packet.Goal.ID)
	work, _ := k.store.WorkItem(spec.Packet.WorkItem.ID)
	attempt, _ := k.store.Attempt(spec.AttemptID)
	return SimulationOutcome{
		Completed: completion.Complete, GoalState: goal.State, WorkState: work.State, AttemptState: attempt.State,
		AgentClaim: claim, Evidence: evidence, Reasons: append([]string(nil), completion.Reasons...),
	}
}

func (k *Kernel) transitionGoal(id string, state domain.GoalState, event string) error {
	goal, err := k.store.Goal(id)
	if err != nil {
		return err
	}
	return k.store.UpdateGoalState(id, goal.Version, state, event)
}

func (k *Kernel) transitionWork(id string, state domain.WorkState, event string) error {
	work, err := k.store.WorkItem(id)
	if err != nil {
		return err
	}
	return k.store.UpdateWorkState(id, work.Version, state, event)
}

func (k *Kernel) transitionAttempt(id string, state domain.AttemptState, event string) error {
	attempt, err := k.store.Attempt(id)
	if err != nil {
		return err
	}
	return k.store.UpdateAttemptState(id, attempt.Version, state, event)
}
