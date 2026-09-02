package kernel_test

import (
	"context"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/adapter/fake"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/kernel"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/store"
	validatorfake "github.com/monshunter/xgoal/internal/validator/fake"
)

func TestRunSimulationCompletesOnlyWithBoundDeterministicEvidence(t *testing.T) {
	fixture := newFixture(domain.AuthorityDeterministic)
	outcome, err := fixture.kernel.RunSimulation(context.Background(), fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Completed || outcome.GoalState != domain.GoalCompleted || outcome.WorkState != domain.WorkCompleted || outcome.AttemptState != domain.AttemptSucceeded {
		t.Fatalf("RunSimulation() = %+v", outcome)
	}
	if outcome.AgentClaim.Authority() != domain.AuthorityClaim || outcome.Evidence.Authority != domain.AuthorityDeterministic {
		t.Fatalf("claim/evidence authority = %q/%q", outcome.AgentClaim.Authority(), outcome.Evidence.Authority)
	}
}

func TestRunSimulationRejectsAgentCompletionClaimWithoutTrustedEvidence(t *testing.T) {
	fixture := newFixture(domain.AuthorityClaim)
	outcome, err := fixture.kernel.RunSimulation(context.Background(), fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Completed || outcome.GoalState == domain.GoalCompleted {
		t.Fatalf("RunSimulation() accepted agent claim: %+v", outcome)
	}
	if outcome.WorkState != domain.WorkReconciling || outcome.AttemptState != domain.AttemptFailed {
		t.Fatalf("failure states = work %s, attempt %s", outcome.WorkState, outcome.AttemptState)
	}
}

func TestRunSimulationRejectsMismatchedEvidenceBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*protocol.Evidence)
	}{
		{name: "subject", mutate: func(evidence *protocol.Evidence) { evidence.SubjectID = "work_other" }},
		{name: "goal revision", mutate: func(evidence *protocol.Evidence) { evidence.GoalRevisionHash = "goal-old" }},
		{name: "config", mutate: func(evidence *protocol.Evidence) { evidence.ConfigHash = "config-old" }},
		{name: "tree", mutate: func(evidence *protocol.Evidence) { evidence.TreeHash = "tree-old" }},
		{name: "authority", mutate: func(evidence *protocol.Evidence) { evidence.Authority = domain.AuthorityFact }},
		{name: "state", mutate: func(evidence *protocol.Evidence) { evidence.State = domain.EvidenceStale }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixtureWithEvidence(domain.AuthorityDeterministic, test.mutate)
			outcome, err := fixture.kernel.RunSimulation(context.Background(), fixture.spec)
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Completed || outcome.GoalState == domain.GoalCompleted {
				t.Fatalf("RunSimulation() accepted mismatched evidence: %+v", outcome)
			}
			if outcome.WorkState != domain.WorkReconciling || outcome.AttemptState != domain.AttemptFailed {
				t.Fatalf("failure states = work %s, attempt %s", outcome.WorkState, outcome.AttemptState)
			}
		})
	}
}

type fixture struct {
	kernel *kernel.Kernel
	spec   kernel.SimulationSpec
}

func newFixture(authority domain.Authority) fixture {
	return newFixtureWithEvidence(authority, nil)
}

func newFixtureWithEvidence(authority domain.Authority, mutate func(*protocol.Evidence)) fixture {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	packet := protocol.WorkPacket{
		ProtocolVersion: protocol.WorkPacketVersion,
		Project:         protocol.PacketProject{Name: "demo", BaseTree: "tree-base", Workspace: "/tmp/xgoal-sim"},
		Goal:            protocol.PacketGoal{ID: "goal_1", Revision: 1, Summary: "simulate closure", ContractHash: "goal-hash"},
		WorkItem: protocol.PacketWorkItem{
			ID: "work_1", Title: "simulate", Objective: "prove closure", ReadScope: []string{"/**"}, WriteScope: []string{"/internal/demo/**"},
			AcceptanceCriteria: []string{"AC-1"}, ValidatorIDs: []string{"fake-validator"},
		},
		Role:                 domain.RoleImplementer,
		Constraints:          protocol.PacketConstraints{ProjectNetwork: "deny", ProjectSecrets: "deny"},
		RequiredOutputSchema: protocol.AgentResultVersion,
	}
	evidence := protocol.Evidence{
		ProtocolVersion: protocol.EvidenceVersion,
		ID:              "ev_1", Kind: "command", SubjectID: "work_1", Producer: "fake-validator", Authority: authority,
		GoalRevisionHash: "goal-hash", ConfigHash: "config-hash", TreeHash: "tree-final", PayloadHash: "payload-hash",
		State: domain.EvidenceCurrent, CreatedAt: now,
	}
	if mutate != nil {
		mutate(&evidence)
	}
	agent := fake.New("fake-runtime", fake.Script{Result: protocol.AgentResult{
		ProtocolVersion: protocol.AgentResultVersion,
		Status:          protocol.ResultCompleted,
		Summary:         "agent claims completion",
	}})
	memory := store.NewMemory(clock.NewFake(now))
	return fixture{
		kernel: kernel.New(memory, agent, validatorfake.New(evidence, nil), clock.NewFake(now)),
		spec: kernel.SimulationSpec{
			Packet: packet, AttemptID: "att_1", LeaseID: "lease_1", AgentProfileID: "fake-runtime",
			ConfigHash: "config-hash", FinalTree: "tree-final", ScopePolicyPassed: true,
			FinalValidationSetCurrent: true, FinalReportGenerated: true,
		},
	}
}
