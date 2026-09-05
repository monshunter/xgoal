package acceptance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/protocol"
)

// ReadClaim recovers only a complete result bound to the exact immutable
// invocation metadata. A valid claim does not imply a successful process exit.
func ReadClaim(runtimeRoot string, r Request) (*protocol.AgentResult, error) {
	provider := strings.TrimSuffix(r.ExecutionConfig.Provider, "-cli")
	if provider != "codex" && provider != "claude" {
		return nil, errors.New("unknown acceptance provider")
	}
	dir := filepath.Join(runtimeRoot, "adapters", provider, "acceptances", r.Packet.ID)
	metadata, err := readRegular(filepath.Join(dir, "invocation.json"), 1<<20)
	if err != nil {
		return nil, err
	}
	want, err := canonical.Marshal((Invocation{InvocationID: r.Packet.ID, ProfileID: r.Packet.ProfileID, GoalRevisionHash: r.Packet.GoalRevisionHash, ConfigHash: r.Packet.ConfigHash, TreeHash: r.Packet.TreeHash, PacketPath: r.PacketPath, PacketHash: r.PacketHash, ExecutionConfig: &r.ExecutionConfig}).Record())
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(metadata, want) {
		return nil, errors.New("acceptance result metadata differs from request")
	}
	data, err := readRegular(filepath.Join(dir, "result.json"), 16<<20)
	if errors.Is(err, os.ErrNotExist) {
		return readEventClaim(dir, provider)
	}
	if err != nil {
		return nil, err
	}
	result, err := protocol.DecodeAgentResult(bytes.NewReader(data), 16<<20)
	if err != nil {
		return nil, err
	}
	return &result, nil
}
func readRegular(path string, limit int64) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if resolved != path {
		return nil, errors.New("linked acceptance artifact")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("invalid acceptance artifact")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if len(data) > int(limit) {
		return nil, errors.New("acceptance artifact exceeds limit")
	}
	return data, err
}

var ErrNoClaim = errors.New("no complete acceptance claim has been persisted")

// readEventClaim closes the crash window between a fsynced complete Provider
// event and result.json. It never infers process success from a result event.
func readEventClaim(dir, provider string) (*protocol.AgentResult, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "events"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoClaim
	}
	if err != nil {
		return nil, err
	}
	if len(entries) > 16384 {
		return nil, errors.New("acceptance event limit exceeded")
	}
	var claim *protocol.AgentResult
	session := ""
	total := 0
	providerFinished := false
	for i, entry := range entries {
		if entry.Name() != fmt.Sprintf("%06d.json", i+1) {
			return claim, errors.New("acceptance event sequence is incomplete")
		}
		data, err := readRegular(filepath.Join(dir, "events", entry.Name()), 16<<20)
		if err != nil {
			return claim, err
		}
		total += len(data)
		if total > 32<<20 {
			return claim, errors.New("acceptance event byte limit exceeded")
		}
		var raw struct {
			Type       string          `json:"type"`
			Subtype    string          `json:"subtype"`
			SessionID  string          `json:"session_id"`
			ThreadID   string          `json:"thread_id"`
			IsError    bool            `json:"is_error"`
			Structured json.RawMessage `json:"structured_output"`
			Item       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return claim, err
		}
		if raw.Type == "turn.failed" || raw.Type == "error" || (provider == "claude" && raw.Type == "result" && raw.IsError) {
			return claim, errors.New("Provider persisted a terminal error event")
		}
		if raw.Type == "turn.completed" || (provider == "claude" && raw.Type == "result") {
			providerFinished = true
		}
		if raw.Type == "" {
			return claim, errors.New("acceptance event is missing type")
		}
		nextSession := ""
		var candidate []byte
		if provider == "codex" {
			if raw.Type == "thread.started" {
				nextSession = raw.ThreadID
			}
			if raw.Type == "item.completed" && raw.Item.Type == "agent_message" {
				candidate = []byte(raw.Item.Text)
			}
		} else {
			if (raw.Type == "system" && raw.Subtype == "init") || raw.Type == "result" {
				nextSession = raw.SessionID
			}
			if raw.Type == "result" {
				candidate = raw.Structured
			}
		}
		if nextSession != "" {
			if !component(nextSession) || (session != "" && session != nextSession) {
				return claim, errors.New("acceptance event session changed")
			}
			session = nextSession
		}
		if len(candidate) == 0 {
			continue
		}
		value, err := protocol.DecodeAgentResult(bytes.NewReader(candidate), 16<<20)
		if err != nil {
			if provider == "claude" {
				return claim, err
			}
			continue
		} // Ordinary non-final assistant messages are not claims.
		if session == "" {
			return claim, errors.New("acceptance claim lacks a Provider session")
		}
		// Once a complete blocking question/failure has been recorded, later events
		// cannot turn a crash recovery into a blind successful/safe replay.
		if claim == nil || (claim.Status == protocol.ResultCompleted && value.Status != protocol.ResultCompleted) {
			claim = &value
		}
		if raw.IsError {
			return claim, errors.New("Provider marked the result as an error")
		}
	}
	if claim == nil {
		if providerFinished {
			return nil, errors.New("Provider ended without a valid Acceptance claim")
		}
		return nil, ErrNoClaim
	}
	return claim, nil
}
