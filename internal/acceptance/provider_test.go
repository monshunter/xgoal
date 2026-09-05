package acceptance_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/acceptance"
	"github.com/monshunter/xgoal/internal/adapter/claude"
	"github.com/monshunter/xgoal/internal/adapter/codex"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
)

func TestNativeAcceptanceInvokesRestrictedProviderAndPersistsOnlyClaim(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			packet, in := fixture(t)
			in.Timeout = 10 * time.Second
			base := in.WorkDir
			claim := protocol.AgentResult{ProtocolVersion: protocol.AgentResultVersion, Status: protocol.ResultBlocked, Summary: "needs a test account", Blockers: []string{"supply test account"}}
			result, _ := json.Marshal(claim)
			var stream []byte
			if provider == "codex" {
				item, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": string(result)}})
				stream = append([]byte("{\"type\":\"thread.started\",\"thread_id\":\"accept-session\"}\n"), append(item, '\n')...)
			} else {
				item, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": "accept-session", "structured_output": claim})
				stream = append([]byte("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"accept-session\"}\n"), append(item, '\n')...)
			}
			binary := filepath.Join(base, "provider")
			script := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" > \"$ARG_FILE\"\ncat > \"$INPUT_FILE\"\ncat \"$EVENT_FILE\"\n"
			for name, data := range map[string][]byte{"provider": []byte(script), "events.jsonl": stream} {
				if err := os.WriteFile(filepath.Join(base, name), data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			in.Environment = map[string]string{"PATH": os.Getenv("PATH"), "ARG_FILE": filepath.Join(base, "args"), "INPUT_FILE": filepath.Join(base, "input"), "EVENT_FILE": filepath.Join(base, "events.jsonl")}
			profile := config.Agent{ID: packet.ProfileID, Adapter: provider + "-cli", Roles: []string{"acceptance"}, Model: "fixture-model", ReasoningEffort: "low"}
			if provider == "claude" {
				profile.AllowedTools = []string{"Read", "Bash(./client.sh *)"}
			}
			effective, err := profile.Effective("acceptance", "fixture-version")
			if err != nil {
				t.Fatal(err)
			}
			in.ExecutionConfig = &effective
			var runtime acceptance.Adapter
			if provider == "codex" {
				runtime, err = codex.New(codex.Config{Binary: binary, RuntimeRoot: base, ProjectRoot: base})
			} else {
				runtime, err = claude.New(claude.Config{Binary: binary, RuntimeRoot: base, ProjectRoot: base})
			}
			if err != nil {
				t.Fatal(err)
			}
			out, err := runtime.Accept(context.Background(), in, nil)
			if err != nil || out.SessionID != "accept-session" || out.Result.Status != protocol.ResultBlocked || out.Result.Authority() != domain.AuthorityClaim {
				t.Fatalf("acceptance result %+v, %v", out, err)
			}
			args, err := os.ReadFile(filepath.Join(base, "args"))
			if err != nil {
				t.Fatal(err)
			}
			wants := []string{"fixture-model"}
			if provider == "codex" {
				wants = append(wants, "--sandbox read-only", "model_reasoning_effort=\"low\"", "--ask-for-approval never")
			} else {
				wants = append(wants, "--permission-mode dontAsk", "--tools Bash,Read", "--allowedTools Bash(./client.sh *),Read", "--effort low")
			}
			for _, want := range wants {
				if !strings.Contains(string(args), want) {
					t.Fatalf("missing %q in %s", want, args)
				}
			}
			dir := filepath.Join(base, "adapters", provider, "acceptances", in.InvocationID)
			for _, file := range []string{"invocation.json", "result.json", "stderr.log"} {
				if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
					t.Fatal(err)
				}
			}
			request := acceptance.Request{Packet: packet, PacketPath: in.PacketPath, PacketHash: in.PacketHash, ExecutionConfig: effective, GoalVersion: 1, RecoveryLimit: 2}
			recovered, err := acceptance.ReadClaim(base, request)
			if err != nil || recovered.Status != protocol.ResultBlocked {
				t.Fatalf("result recovery %+v %v", recovered, err)
			}
			if err := os.Rename(filepath.Join(dir, "result.json"), filepath.Join(dir, "result.saved")); err != nil {
				t.Fatal(err)
			}
			recovered, err = acceptance.ReadClaim(base, request)
			if err != nil || recovered.Status != protocol.ResultBlocked {
				t.Fatalf("event-only recovery %+v %v", recovered, err)
			}
			metadata, err := os.ReadFile(filepath.Join(dir, "invocation.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(metadata), `"role":"acceptance"`) || strings.Contains(string(metadata), "EVENT_FILE") || strings.Contains(string(metadata), "work_item_id") {
				t.Fatalf("invalid provenance %s", metadata)
			}
		})
	}
}
