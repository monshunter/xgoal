package protocol_test

import (
	"encoding/json"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/protocol"
	"testing"
)

func TestWorkPacketBindsFrozenAcceptanceAndSchemaFields(t *testing.T) {
	packet := validPacket()
	packet.Goal.RawGoal = "implement the requested game"
	packet.Goal.Contract = json.RawMessage(`{"contract":{"summary":"frozen acceptance"}}`)
	hash, err := canonical.Hash("goal-revision", "xgoal.goal-revision/v1", packet.Goal.Contract)
	if err != nil {
		t.Fatal(err)
	}
	packet.Goal.ContractHash = hash
	if err := packet.Validate(); err != nil {
		t.Fatal(err)
	}
	// Check the schema's closed goal properties against the actual serialized Packet.
	data, err := protocol.Schema(protocol.SchemaWorkPacket)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)["goal"].(map[string]any)["properties"].(map[string]any)
	encoded, err := json.Marshal(packet.Goal)
	if err != nil {
		t.Fatal(err)
	}
	var goal map[string]any
	if err := json.Unmarshal(encoded, &goal); err != nil {
		t.Fatal(err)
	}
	for key := range goal {
		if _, exists := properties[key]; !exists {
			t.Errorf("serialized Goal field %s is rejected by the public schema", key)
		}
	}
	if properties["contract"].(map[string]any)["type"] != "object" {
		t.Fatal("schema must declare the frozen object")
	}
	packet.Goal.Contract = json.RawMessage(`{"contract":{"summary":"weakened acceptance"}}`)
	if packet.Validate() == nil {
		t.Fatal("accepted modified frozen acceptance")
	}
}

func TestReviewPacketDeclaresFrozenAcceptanceInputs(t *testing.T) {
	data, err := protocol.Schema(protocol.SchemaReviewPacket)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	for key, kind := range map[string]string{"raw_goal": "string", "goal_contract": "object"} {
		field, ok := properties[key].(map[string]any)
		if !ok || field["type"] != kind {
			t.Fatalf("missing %s schema type %s", key, kind)
		}
	}
}
