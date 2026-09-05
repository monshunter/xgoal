package acceptance_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/acceptance"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/protocol"
)

func fixture(t *testing.T) (acceptance.Packet, acceptance.Invocation) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	packet := acceptance.Packet{ProtocolVersion: acceptance.PacketVersion, ID: "acceptance_1", GoalID: "goal", GoalRevisionHash: strings.Repeat("a", 64), ConfigHash: strings.Repeat("b", 64), TreeHash: strings.Repeat("c", 40), ProfileID: "acceptance", OwnerAttemptID: "last_success", OwnerGeneration: 1, Workspace: root, EnvironmentID: "final_env", ScenarioDir: filepath.Join(root, "scenario"), ProjectNetwork: "deny", Scenarios: []config.Scenario{{ID: "inspect", Description: "Inspect the final source", Steps: []string{"Read the implementation"}, Validators: []string{"check"}}}}
	path, hash, err := acceptance.Prepare(root, packet)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := (config.Agent{ID: "acceptance", Adapter: "codex-cli", Roles: []string{"acceptance"}, Model: "fixture", ReasoningEffort: "low"}).Effective("acceptance", "fixture-version")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := protocol.Schema(protocol.SchemaAgentResult)
	if err != nil {
		t.Fatal(err)
	}
	return packet, acceptance.Invocation{InvocationID: packet.ID, ProfileID: packet.ProfileID, GoalRevisionHash: packet.GoalRevisionHash, ConfigHash: packet.ConfigHash, TreeHash: packet.TreeHash, PacketPath: path, PacketHash: hash, WorkDir: root, Prompt: "Inspect the immutable packet", OutputSchema: schema, ExecutionConfig: &effective, Timeout: time.Second, MaxOutputBytes: 1 << 20}
}

func TestAcceptanceHasIndependentPacketAndEnforcesItsFrozenIdentity(t *testing.T) {
	packet, invocation := fixture(t)
	actual, err := acceptance.ValidateInvocation(invocation)
	if err != nil || actual.ID != packet.ID || actual.OwnerAttemptID != "last_success" {
		t.Fatalf("packet: %+v %v", actual, err)
	}
	for _, field := range []string{"revision", "tree", "profile", "permission", "schema"} {
		t.Run(field, func(t *testing.T) {
			changed := invocation
			changed.ExecutionConfig = invocation.ExecutionConfig.Clone()
			switch field {
			case "revision":
				changed.GoalRevisionHash = strings.Repeat("f", 64)
			case "tree":
				changed.TreeHash = strings.Repeat("f", 40)
			case "profile":
				changed.ProfileID = "other"
			case "permission":
				changed.ExecutionConfig.Sandbox = "workspace-write"
			case "schema":
				changed.OutputSchema = []byte(`{"type":"object"}`)
			}
			if _, err := acceptance.ValidateInvocation(changed); err == nil {
				t.Fatal("mismatched invocation accepted")
			}
		})
	}
	if err := os.Chmod(invocation.PacketPath, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := acceptance.ValidateInvocation(invocation); err == nil {
		t.Fatal("mutable packet accepted")
	}
}

func TestAcceptancePacketCannotBeOverwritten(t *testing.T) {
	packet, invocation := fixture(t)
	if _, _, err := acceptance.Prepare(invocation.WorkDir, packet); err == nil {
		t.Fatal("existing acceptance packet overwritten")
	}
}
