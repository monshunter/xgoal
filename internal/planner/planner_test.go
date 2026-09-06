package planner

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDecodeIsStrictAndPreservesPlannerAmbiguities(t *testing.T) {
	valid := `{"protocol_version":"xgoal.planner-proposal/v1alpha1","contract":{},"plan":{},"ambiguities":["which public API may change"]}`
	proposal, err := Decode(strings.NewReader(valid), int64(len(valid)))
	if err != nil || len(proposal.Ambiguities) != 1 {
		t.Fatalf("Decode() = %+v, %v", proposal, err)
	}
	for name, input := range map[string]string{
		"unknown":  strings.TrimSuffix(valid, "}") + `,"extra":true}`,
		"trailing": valid + `{}`,
		"blank":    strings.Replace(valid, "which public API may change", " ", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(input), int64(len(input))); err == nil {
				t.Fatal("invalid proposal was accepted")
			}
		})
	}
}

func TestPrepareAndValidateInvocationBindCanonicalImmutablePacket(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	projectRoot := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	packet := Packet{
		ProtocolVersion: PacketVersion, GoalID: "goal_1", RawGoal: "implement bounded behavior", Mode: "standard",
		ConfigHash: strings.Repeat("a", 64), TrustedValidators: []string{"go-test"}, ProjectRoot: projectRoot,
		ProjectNetwork: "deny", ProjectSecrets: "deny",
	}
	path, hash, err := Prepare(runtimeRoot, packet)
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := Schema()
	invocation := Invocation{InvocationID: "invoke_1", ProfileID: "planner_1", WorkDir: projectRoot, PacketPath: path, PacketHash: hash, Prompt: "plan", OutputSchema: schema, Timeout: time.Second, MaxOutputBytes: 1 << 20}
	loaded, err := ValidateInvocation(invocation)
	if err != nil || loaded.GoalID != packet.GoalID {
		t.Fatalf("ValidateInvocation() = %+v, %v", loaded, err)
	}
	content, _ := os.ReadFile(path)
	pretty := bytes.ReplaceAll(content, []byte(`,"`), []byte(",\n  \""))
	if bytes.Equal(content, pretty) {
		t.Fatal("test fixture did not create a non-canonical form")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pretty, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateInvocation(invocation); err == nil {
		t.Fatal("non-canonical Planner packet was accepted")
	}
}

func TestPrepareRejectsUnsafeGoalIdentityAndPolicy(t *testing.T) {
	packet := Packet{ProtocolVersion: PacketVersion, GoalID: "../escape", RawGoal: "goal", Mode: "standard", ConfigHash: strings.Repeat("a", 64), TrustedValidators: []string{"test"}, ProjectRoot: t.TempDir(), ProjectNetwork: "deny", ProjectSecrets: "deny"}
	if _, _, err := Prepare(filepath.Join(t.TempDir(), "runtime"), packet); err == nil {
		t.Fatal("unsafe Goal id was accepted")
	}
}

func TestPlannerDoesNotRequirePlaceholderValidators(t *testing.T) {
	for _, policy := range []string{"allow", "human-gate", "deny"} {
		t.Run(policy, func(t *testing.T) {
			packet := Packet{ProtocolVersion: PacketVersion, GoalID: "goal_new", RawGoal: "implement game", Mode: "standard", ConfigHash: strings.Repeat("a", 64), ProjectRoot: t.TempDir(), ProjectNetwork: "deny", ProjectSecrets: "deny", GeneratedValidationPolicy: policy}
			_, _, err := Prepare(filepath.Join(t.TempDir(), "runtime"), packet)
			if policy == "deny" {
				if err == nil || !strings.Contains(err.Error(), "denies generation") {
					t.Fatalf("unclear policy failure: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
