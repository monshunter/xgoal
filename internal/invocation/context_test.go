package invocation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/config"
)

func TestMetadataCannotContradictRegisteredConfigurationOrTree(t *testing.T) {
	input := Input{ID: "i", PacketHash: "packet", DelegationHash: "delegation", Role: "implementer", ProfileID: "p", Provider: "codex-cli", OwnerID: "attempt", InputTree: "tree", GoalRevisionHash: "revision", PlanRevisionHash: "plan", ExecutionConfig: config.ExecutionConfig{ProfileID: "p", Provider: "codex-cli", Role: "implementer", Model: "requested", Sandbox: "read-only"}}
	fresh := func() map[string]any {
		return map[string]any{"invocation_id": "i", "packet_hash": "packet", "delegation_hash": "delegation", "execution_config": input.ExecutionConfig, "profile_id": "p", "role": "implementer", "attempt_id": "attempt", "base_tree": "tree", "goal_revision_hash": "revision", "plan_revision_hash": "plan"}
	}
	data, _ := json.Marshal(fresh())
	if _, err := publicMetadata(data, input); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"profile_id", "attempt_id", "base_tree", "goal_revision_hash", "delegation_hash", "execution_config"} {
		t.Run(key, func(t *testing.T) {
			m := fresh()
			m[key] = "tampered"
			data, _ := json.Marshal(m)
			if _, err := publicMetadata(data, input); err == nil {
				t.Fatal("contradictory metadata accepted")
			}
		})
	}
	_, err := publicResult([]byte(`{"protocol_version":"xgoal.agent-result/v1alpha1","status":"token=error-sentinel","summary":"invalid"}`), "implementer")
	if err == nil || !strings.Contains(err.Error(), "error-sentinel") {
		t.Fatal("fixture must exercise error redaction at ReadContext boundary")
	}
}

func TestPlannerMetadataPreservesIntegerGeneration(t *testing.T) {
	input := Input{ID: "i", PacketHash: "packet", DelegationHash: "delegation", Role: "planner", ProfileID: "p", RequestHash: "request", InputTree: "tree", Generation: 9007199254740993}
	metadata := map[string]any{"invocation_id": input.ID, "packet_hash": input.PacketHash, "delegation_hash": input.DelegationHash, "execution_config": input.ExecutionConfig, "profile_id": input.ProfileID, "request_hash": input.RequestHash, "input_tree": input.InputTree, "generation": input.Generation}
	data, _ := json.Marshal(metadata)
	got, err := publicMetadata(data, input)
	if err != nil || !strings.Contains(string(got), `"generation":9007199254740993`) {
		t.Fatalf("generation was lost: %s error=%v", got, err)
	}
	metadata["generation"] = input.Generation - 1
	data, _ = json.Marshal(metadata)
	if _, err := publicMetadata(data, input); err == nil {
		t.Fatal("different generation accepted")
	}
}
