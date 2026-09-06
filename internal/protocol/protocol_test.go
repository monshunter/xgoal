package protocol_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
)

func TestWorkPacketValidateAndHash(t *testing.T) {
	packet := validPacket()
	if err := packet.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	first, err := packet.Hash()
	if err != nil {
		t.Fatal(err)
	}
	second, err := packet.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("Hash() = %q / %q", first, second)
	}

	packet.WorkItem.WriteScope = []string{"../escape"}
	if err := packet.Validate(); err == nil || !strings.Contains(err.Error(), "write_scope") {
		t.Fatalf("Validate() error = %v, want write_scope error", err)
	}
}

func TestWorkPacketValidateRejectsSchemaAndPolicyViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*protocol.WorkPacket)
	}{
		{name: "project network", mutate: func(packet *protocol.WorkPacket) { packet.Constraints.ProjectNetwork = "unrestricted" }},
		{name: "project secrets", mutate: func(packet *protocol.WorkPacket) { packet.Constraints.ProjectSecrets = "allow" }},
		{name: "git push", mutate: func(packet *protocol.WorkPacket) { packet.Constraints.GitPush = true }},
		{name: "production", mutate: func(packet *protocol.WorkPacket) { packet.Constraints.Production = true }},
		{name: "embedded globstar", mutate: func(packet *protocol.WorkPacket) { packet.WorkItem.WriteScope = []string{"/internal/foo**bar"} }},
		{name: "git metadata", mutate: func(packet *protocol.WorkPacket) { packet.WorkItem.WriteScope = []string{"/.git/**"} }},
		{name: "prior attempt", mutate: func(packet *protocol.WorkPacket) {
			packet.PriorAttempt = &protocol.PacketPriorAttempt{FailureClass: "", FailureFingerprint: "fingerprint"}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			packet := validPacket()
			test.mutate(&packet)
			if err := packet.Validate(); err == nil {
				t.Fatal("Validate() accepted invalid packet")
			}
		})
	}
}

func TestAgentEventValidateRejectsInvalidNestedClaims(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	tests := []protocol.AgentEvent{
		{ProtocolVersion: protocol.AgentEventVersion, Type: "command", At: now, Command: &protocol.CommandClaim{}},
		{ProtocolVersion: protocol.AgentEventVersion, Type: "file", At: now, FileChange: &protocol.FileChangeClaim{}},
	}
	for index, event := range tests {
		if err := event.Validate(); err == nil {
			t.Fatalf("case %d: Validate() accepted invalid event", index)
		}
	}
}

func TestAgentResultClaimsDoNotBecomeEvidence(t *testing.T) {
	result := protocol.AgentResult{
		ProtocolVersion: protocol.AgentResultVersion,
		Status:          protocol.ResultCompleted,
		Summary:         "claimed complete",
		ChecksClaimed:   []protocol.CheckClaim{{Name: "go test ./...", Status: "passed"}},
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if result.Authority() != domain.AuthorityClaim {
		t.Fatalf("Authority() = %q, want CLAIM", result.Authority())
	}

	result.Status = "trusted-because-exit-zero"
	if err := result.Validate(); err == nil {
		t.Fatal("Validate() accepted unknown result status")
	}
}

func TestDecodeAgentResultRejectsUnknownFields(t *testing.T) {
	input := []byte(`{"protocol_version":"xgoal.agent-result/v1alpha1","status":"completed","summary":"done","trusted":true}`)
	if _, err := protocol.DecodeAgentResult(bytes.NewReader(input), 1024); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("DecodeAgentResult() error = %v, want unknown field error", err)
	}
}

func TestEvidenceValidateRejectsUnknownEnums(t *testing.T) {
	evidence := validEvidence()
	evidence.Authority = domain.Authority("TRUST_ME")
	if err := evidence.Validate(); err == nil {
		t.Fatal("Validate() accepted unknown authority")
	}

	evidence = validEvidence()
	evidence.State = domain.EvidenceState("FRESH")
	if err := evidence.Validate(); err == nil {
		t.Fatal("Validate() accepted unknown state")
	}
}

func TestEmbeddedSchemasAreValidAndVersioned(t *testing.T) {
	want := map[string]struct {
		version string
		digest  string
	}{
		protocol.SchemaWorkPacket:  {version: protocol.WorkPacketVersion, digest: "d5b4929235ec7a6188cc15e361b1439572a7fc83f3eba1e736853a0f8141690f"},
		protocol.SchemaAgentResult: {version: protocol.AgentResultVersion, digest: "a71b93ea2850db6cac218d66564b8c94bc84f5552d2dc5594893e30edeca8303"},
		protocol.SchemaAgentEvent:  {version: protocol.AgentEventVersion, digest: "bfb4de3beade4c1b1ee92633641cc4de7c05b54441a2ff0b4dd1d3ea5d193509"},
		protocol.SchemaEvidence:    {version: protocol.EvidenceVersion, digest: "91cde100ace57970ae14e060496c886db3ad7549c3b673c03ed71390dc163663"},
		protocol.SchemaPatchBundle: {version: protocol.PatchBundleVersion, digest: "837a5fd10ba619e549aebf1f5fb3b1995714113f027b575156655f50dfa1c485"},
		protocol.SchemaCommandReceipt: {
			version: protocol.CommandReceiptVersion, digest: "3e86182ea4662a7adca84224eac88fa9f0dbbfc8a9fa63aa4f161e8719b5bc07",
		},
		protocol.SchemaEnvironmentSnapshot: {
			version: protocol.EnvironmentSnapshotVersion, digest: "f549c2e9f6704dbef91d1932caca27fde117764114d21ea1c0967f42f55a5983",
		},
		protocol.SchemaReviewPacket: {
			version: protocol.ReviewPacketVersion, digest: "c008326c3e4e5eca6e8be8ea61e04083b38788f650b349cf2bc8f00202ab254e",
		},
		protocol.SchemaReviewResult: {
			version: protocol.ReviewResultVersion, digest: "1a988670afb88424ab8faf92c2ba03f04f90284d35c087599f3f465f4337b961",
		},
	}
	for name, expected := range want {
		t.Run(name, func(t *testing.T) {
			data, err := protocol.Schema(name)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				ID string `json:"$id"`
			}
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatalf("invalid JSON schema: %v", err)
			}
			if !strings.Contains(schema.ID, expected.version) {
				t.Fatalf("schema $id = %q, want version %q", schema.ID, expected.version)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != expected.digest {
				t.Fatalf("schema digest = %q, want %q", got, expected.digest)
			}
		})
	}
}

func validPacket() protocol.WorkPacket {
	return protocol.WorkPacket{
		ProtocolVersion: protocol.WorkPacketVersion,
		Project: protocol.PacketProject{
			Name:      "demo",
			BaseTree:  "tree-123",
			Workspace: "/tmp/worktree",
		},
		Goal: protocol.PacketGoal{
			ID:           "goal_1",
			Revision:     1,
			Summary:      "deliver a bounded change",
			ContractHash: "hash-1",
		},
		WorkItem: protocol.PacketWorkItem{
			ID:                 "work_1",
			Title:              "bounded change",
			Objective:          "change one package",
			ReadScope:          []string{"/**"},
			WriteScope:         []string{"/internal/demo/**"},
			AcceptanceCriteria: []string{"AC-GOAL-001"},
			ValidatorIDs:       []string{"go-test-demo"},
		},
		Role: domain.RoleImplementer,
		Constraints: protocol.PacketConstraints{
			ProjectNetwork: "deny",
			ProjectSecrets: "deny",
		},
		RequiredOutputSchema: protocol.AgentResultVersion,
	}
}

func validEvidence() protocol.Evidence {
	return protocol.Evidence{
		ProtocolVersion:  protocol.EvidenceVersion,
		ID:               "ev_1",
		Kind:             "command",
		SubjectID:        "work_1",
		Producer:         "validator",
		Authority:        domain.AuthorityDeterministic,
		GoalRevisionHash: "goal-hash",
		ConfigHash:       "config-hash",
		TreeHash:         "tree-hash",
		PayloadHash:      "payload-hash",
		State:            domain.EvidenceCurrent,
		CreatedAt:        time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
	}
}
