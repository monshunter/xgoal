// Package acceptance defines independent, read-only-source scenario sessions.
// Results are Agent claims; deterministic validators retain completion authority.
package acceptance

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/protocol"
)

const PacketVersion = "xgoal.acceptance-packet/v1"
const maxPacketBytes = 8 << 20

type Packet struct {
	ProtocolVersion  string                    `json:"protocol_version"`
	ID               string                    `json:"id"`
	GoalID           string                    `json:"goal_id"`
	GoalRevisionHash string                    `json:"goal_revision_hash"`
	ConfigHash       string                    `json:"config_hash"`
	TreeHash         string                    `json:"tree_hash"`
	ProfileID        string                    `json:"profile_id"`
	OwnerAttemptID   string                    `json:"owner_attempt_id"`
	OwnerGeneration  int64                     `json:"owner_generation"`
	Workspace        string                    `json:"workspace"`
	EnvironmentID    string                    `json:"environment_id"`
	ScenarioDir      string                    `json:"scenario_dir"`
	RuntimeServices  []string                  `json:"runtime_services,omitempty"`
	Scenarios        []config.Scenario         `json:"scenarios"`
	ProjectNetwork   string                    `json:"project_network"`
	Harness          *protocol.HarnessInput    `json:"harness,omitempty"`
	Decisions        []protocol.PacketDecision `json:"decisions,omitempty"`
}

type Invocation struct {
	InvocationID     string
	ProfileID        string
	GoalRevisionHash string
	ConfigHash       string
	TreeHash         string
	PacketPath       string
	PacketHash       string
	WorkDir          string
	Prompt           string
	OutputSchema     []byte
	ExecutionConfig  *config.ExecutionConfig
	Environment      map[string]string
	Timeout          time.Duration
	MaxOutputBytes   int64
}

type Execution struct {
	Result    protocol.AgentResult `json:"result"`
	SessionID string               `json:"session_id"`
}

type Adapter interface {
	Accept(context.Context, Invocation, adapter.EventSink) (Execution, error)
}

type InvocationRecord struct {
	ProtocolVersion  string                  `json:"protocol_version"`
	Role             string                  `json:"role"`
	InvocationID     string                  `json:"invocation_id"`
	ProfileID        string                  `json:"profile_id"`
	GoalRevisionHash string                  `json:"goal_revision_hash"`
	ConfigHash       string                  `json:"config_hash"`
	TreeHash         string                  `json:"tree_hash"`
	PacketPath       string                  `json:"packet_path"`
	PacketHash       string                  `json:"packet_hash"`
	DelegationHash   string                  `json:"delegation_hash"`
	ExecutionConfig  *config.ExecutionConfig `json:"execution_config"`
}

func (i Invocation) Record() InvocationRecord {
	return InvocationRecord{ProtocolVersion: "xgoal.acceptance-invocation/v1", Role: "acceptance", InvocationID: i.InvocationID, ProfileID: i.ProfileID, GoalRevisionHash: i.GoalRevisionHash, ConfigHash: i.ConfigHash, TreeHash: i.TreeHash, PacketPath: i.PacketPath, PacketHash: i.PacketHash, DelegationHash: protocol.DelegationHash(), ExecutionConfig: i.ExecutionConfig}
}

func (p Packet) Validate() error {
	if p.ProtocolVersion != PacketVersion || !component(p.ID) || !component(p.GoalID) || !component(p.ProfileID) || !component(p.OwnerAttemptID) || p.OwnerGeneration <= 0 || !component(p.EnvironmentID) || !cleanAbsolute(p.Workspace) || !cleanAbsolute(p.ScenarioDir) || len(p.Scenarios) == 0 || len(p.Scenarios) > 64 {
		return errors.New("invalid acceptance packet identity, ownership or scenarios")
	}
	if !hash(p.GoalRevisionHash, 64) || !hash(p.ConfigHash, 64) || (!hash(p.TreeHash, 40) && !hash(p.TreeHash, 64)) {
		return errors.New("invalid acceptance packet revision/config/Tree binding")
	}
	if p.ProjectNetwork != "deny" && p.ProjectNetwork != "allow" && p.ProjectNetwork != "require-gate" {
		return errors.New("invalid acceptance project network")
	}
	seen := map[string]bool{}
	for _, scenario := range p.Scenarios {
		if !component(scenario.ID) || seen[scenario.ID] || strings.TrimSpace(scenario.Description) == "" || len(scenario.Steps) == 0 || len(scenario.Validators) == 0 {
			return errors.New("invalid acceptance scenario input")
		}
		seen[scenario.ID] = true
	}
	if p.Harness != nil {
		return p.Harness.Validate()
	}
	return nil
}

func Prepare(runtimeRoot string, packet Packet) (string, string, error) {
	if !cleanAbsolute(runtimeRoot) {
		return "", "", errors.New("invalid acceptance runtime root")
	}
	resolved, err := filepath.EvalSymlinks(runtimeRoot)
	if err != nil || resolved != runtimeRoot {
		return "", "", errors.New("acceptance runtime is unavailable or linked")
	}
	if err := packet.Validate(); err != nil {
		return "", "", err
	}
	directory := runtimeRoot
	for _, part := range []string{"acceptance", packet.ID} {
		directory = filepath.Join(directory, part)
		if err := os.Mkdir(directory, 0700); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return "", "", err
			}
		} else {
			parent, err := os.Open(filepath.Dir(directory))
			if err != nil {
				return "", "", err
			}
			if err := errors.Join(parent.Sync(), parent.Close()); err != nil {
				return "", "", err
			}
		}
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return "", "", errors.New("unsafe acceptance packet directory")
		}
	}
	content, err := canonical.Marshal(packet)
	if err != nil || len(content) > maxPacketBytes {
		return "", "", errors.New("acceptance packet cannot be serialized within limit")
	}
	packetHash, err := canonical.Hash("acceptance-packet", PacketVersion, packet)
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(directory, "packet.json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0400)
	if err != nil {
		return "", "", err
	}
	_, writeErr := f.Write(content)
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return "", "", err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return "", "", err
	}
	return path, packetHash, errors.Join(dir.Sync(), dir.Close())
}

func ValidateInvocation(i Invocation) (Packet, error) {
	if !component(i.InvocationID) || !component(i.ProfileID) || !cleanAbsolute(i.WorkDir) || !cleanAbsolute(i.PacketPath) || !hash(i.PacketHash, 64) || strings.TrimSpace(i.Prompt) == "" || i.Timeout <= 0 || i.MaxOutputBytes <= 0 || i.MaxOutputBytes > 128<<20 || i.ExecutionConfig == nil {
		return Packet{}, errors.New("invalid acceptance invocation")
	}
	if err := adapter.ValidateExecution(i.ExecutionConfig, i.ProfileID, i.ExecutionConfig.Provider, "acceptance"); err != nil {
		return Packet{}, err
	}
	info, err := os.Lstat(i.PacketPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 || info.Size() > maxPacketBytes {
		return Packet{}, errors.New("acceptance packet is unavailable, linked, mutable or oversized")
	}
	resolved, err := filepath.EvalSymlinks(i.PacketPath)
	if err != nil || resolved != i.PacketPath {
		return Packet{}, errors.New("acceptance packet path contains a linked component")
	}
	content, err := os.ReadFile(i.PacketPath)
	if err != nil {
		return Packet{}, err
	}
	var packet Packet
	d := json.NewDecoder(bytes.NewReader(content))
	d.DisallowUnknownFields()
	if err := d.Decode(&packet); err != nil {
		return Packet{}, err
	}
	if err := packet.Validate(); err != nil {
		return Packet{}, err
	}
	canonicalData, err := canonical.Marshal(packet)
	if err != nil || !bytes.Equal(content, canonicalData) {
		return Packet{}, errors.New("acceptance packet is not canonical")
	}
	packetHash, err := canonical.Hash("acceptance-packet", PacketVersion, packet)
	if err != nil || packetHash != i.PacketHash || packet.ID != i.InvocationID || packet.ProfileID != i.ProfileID || packet.Workspace != i.WorkDir || packet.GoalRevisionHash != i.GoalRevisionHash || packet.ConfigHash != i.ConfigHash || packet.TreeHash != i.TreeHash {
		return Packet{}, errors.New("acceptance packet differs from invocation identity")
	}
	want, err := protocol.Schema(protocol.SchemaAgentResult)
	if err != nil {
		return Packet{}, err
	}
	var expected, actual any
	if json.Unmarshal(want, &expected) != nil || json.Unmarshal(i.OutputSchema, &actual) != nil {
		return Packet{}, errors.New("invalid acceptance output schema")
	}
	expectedJSON, _ := canonical.Marshal(expected)
	actualJSON, _ := canonical.Marshal(actual)
	if !bytes.Equal(expectedJSON, actualJSON) {
		return Packet{}, errors.New("acceptance requires the xgoal AgentResult claim schema")
	}
	if i.ExecutionConfig.Provider == "codex-cli" {
		if len(packet.RuntimeServices) != 0 {
			return Packet{}, errors.New("Codex acceptance does not support service interaction")
		}
		for _, scenario := range packet.Scenarios {
			if len(scenario.Services) != 0 {
				return Packet{}, errors.New("Codex acceptance does not support service interaction")
			}
		}
	}
	return packet, nil
}

func component(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 160 && !strings.ContainsAny(value, "/\\\r\n\x00")
}
func cleanAbsolute(value string) bool { return filepath.IsAbs(value) && filepath.Clean(value) == value }
func hash(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
