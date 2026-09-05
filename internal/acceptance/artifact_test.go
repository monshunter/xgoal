package acceptance_test

import (
	"errors"
	"github.com/monshunter/xgoal/internal/config"
	"os"
	"path/filepath"
	"testing"

	"github.com/monshunter/xgoal/internal/acceptance"
	"github.com/monshunter/xgoal/internal/canonical"
)

func TestPersistedTerminalProviderErrorIsNotReplayableInterruption(t *testing.T) {
	for _, event := range []string{`{"type":"turn.failed","error":{"message":"quota unavailable"}}`, `{"type":"turn.completed"}`} {
		t.Run(event, func(t *testing.T) {
			packet, in := fixture(t)
			req := acceptance.Request{Packet: packet, PacketPath: in.PacketPath, PacketHash: in.PacketHash, ExecutionConfig: *in.ExecutionConfig, GoalVersion: 1, RecoveryLimit: 2}
			dir := filepath.Join(in.WorkDir, "adapters", "codex", "acceptances", packet.ID)
			if err := os.MkdirAll(filepath.Join(dir, "events"), 0700); err != nil {
				t.Fatal(err)
			}
			metadata, err := canonical.Marshal(in.Record())
			if err != nil {
				t.Fatal(err)
			}
			for path, data := range map[string][]byte{"invocation.json": metadata, "events/000001.json": []byte(`{"type":"thread.started","thread_id":"session"}`), "events/000002.json": []byte(event)} {
				if err := os.WriteFile(filepath.Join(dir, path), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if claim, err := acceptance.ReadClaim(in.WorkDir, req); err == nil || errors.Is(err, acceptance.ErrNoClaim) || claim != nil {
				t.Fatalf("terminal error became interruption: %+v %v", claim, err)
			}
		})
	}
}

func TestPersistedClaudeErrorOrMissingStructuredResultRequiresGate(t *testing.T) {
	for _, event := range []string{`{"type":"result","session_id":"session","is_error":true,"result":"quota unavailable"}`, `{"type":"result","session_id":"session","is_error":false}`} {
		t.Run(event, func(t *testing.T) {
			packet, in := fixture(t)
			effective, err := (config.Agent{ID: packet.ProfileID, Adapter: "claude-cli", Roles: []string{"acceptance"}}).Effective("acceptance", "fixture")
			if err != nil {
				t.Fatal(err)
			}
			in.ExecutionConfig = &effective
			req := acceptance.Request{Packet: packet, PacketPath: in.PacketPath, PacketHash: in.PacketHash, ExecutionConfig: effective, GoalVersion: 1, RecoveryLimit: 2}
			dir := filepath.Join(in.WorkDir, "adapters", "claude", "acceptances", packet.ID)
			if err := os.MkdirAll(filepath.Join(dir, "events"), 0700); err != nil {
				t.Fatal(err)
			}
			metadata, err := canonical.Marshal(in.Record())
			if err != nil {
				t.Fatal(err)
			}
			for path, data := range map[string][]byte{"invocation.json": metadata, "events/000001.json": []byte(`{"type":"system","subtype":"init","session_id":"session"}`), "events/000002.json": []byte(event)} {
				if err := os.WriteFile(filepath.Join(dir, path), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if claim, err := acceptance.ReadClaim(in.WorkDir, req); err == nil || errors.Is(err, acceptance.ErrNoClaim) || claim != nil {
				t.Fatalf("Claude error became interruption: %+v %v", claim, err)
			}
		})
	}
}
