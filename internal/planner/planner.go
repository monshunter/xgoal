// Package planner defines the bounded, read-only Planner adapter contract.
package planner

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/goalcompile"
)

const (
	ProposalVersion = "xgoal.planner-proposal/v1alpha1"
	PacketVersion   = "xgoal.planner-packet/v1alpha1"
	maxPacketBytes  = 8 << 20
)

type Proposal struct {
	ProtocolVersion string               `json:"protocol_version"`
	Contract        goalcompile.Contract `json:"contract"`
	Plan            goalcompile.Plan     `json:"plan"`
	Ambiguities     []string             `json:"ambiguities"`
}

type Packet struct {
	ProtocolVersion   string   `json:"protocol_version"`
	GoalID            string   `json:"goal_id"`
	RawGoal           string   `json:"raw_goal"`
	Mode              string   `json:"mode"`
	ConfigHash        string   `json:"config_hash"`
	TrustedValidators []string `json:"trusted_validators"`
	ProjectRoot       string   `json:"project_root"`
	ProjectNetwork    string   `json:"project_network"`
	ProjectSecrets    string   `json:"project_secrets"`
}

type Invocation struct {
	InvocationID   string
	ProfileID      string
	WorkDir        string
	PacketPath     string
	PacketHash     string
	Prompt         string
	OutputSchema   []byte
	Environment    map[string]string
	Timeout        time.Duration
	MaxOutputBytes int64
}

type Execution struct {
	Proposal  Proposal
	SessionID string
}

type Adapter interface {
	Plan(context.Context, Invocation, adapter.EventSink) (Execution, error)
}

//go:embed schema.json
var schemaFile embed.FS

func Schema() ([]byte, error) { return schemaFile.ReadFile("schema.json") }

func Decode(reader io.Reader, limit int64) (Proposal, error) {
	if limit <= 0 {
		return Proposal{}, errors.New("Planner proposal limit must be positive")
	}
	decoder := json.NewDecoder(io.LimitReader(reader, limit+1))
	decoder.DisallowUnknownFields()
	var proposal Proposal
	if err := decoder.Decode(&proposal); err != nil {
		return Proposal{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Proposal{}, errors.New("Planner proposal has trailing data")
	}
	if proposal.ProtocolVersion != ProposalVersion {
		return Proposal{}, fmt.Errorf("protocol_version must be %q", ProposalVersion)
	}
	for _, ambiguity := range proposal.Ambiguities {
		if strings.TrimSpace(ambiguity) == "" {
			return Proposal{}, errors.New("Planner ambiguity must be non-empty")
		}
	}
	return proposal, nil
}

func Prepare(runtimeRoot string, packet Packet) (string, string, error) {
	if !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot || validatePacket(packet) != nil {
		return "", "", errors.New("invalid Planner packet")
	}
	directory := filepath.Join(runtimeRoot, "planner", packet.GoalID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", "", err
	}
	content, err := canonical.Marshal(packet)
	if err != nil {
		return "", "", err
	}
	hash, err := canonical.Hash("planner-packet", PacketVersion, packet)
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(directory, "packet.json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400)
	if err != nil {
		return "", "", err
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	return path, hash, err
}

// PrepareInvocation keeps each planning generation's immutable packet separate.
// A crash after writing a packet is recoverable only with identical bytes.
func PrepareInvocation(runtimeRoot, invocationID string, generation int64, packet Packet) (string, string, error) {
	if !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot || !validComponent(invocationID) || generation <= 0 || validatePacket(packet) != nil {
		return "", "", errors.New("invalid Planner generation packet")
	}
	directory := runtimeRoot
	for _, component := range []string{"planner", packet.GoalID, fmt.Sprintf("%d-%s", generation, invocationID)} {
		directory = filepath.Join(directory, component)
		if err := os.Mkdir(directory, 0700); err != nil && !os.IsExist(err) {
			return "", "", err
		}
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return "", "", errors.New("Planner packet directory is linked or unsafe")
		}
	}
	content, err := canonical.Marshal(packet)
	if err != nil {
		return "", "", err
	}
	hash, err := canonical.Hash("planner-packet", PacketVersion, packet)
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(directory, "packet.json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0400)
	if os.IsExist(err) {
		info, inspectErr := os.Lstat(path)
		if inspectErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 || info.Size() > maxPacketBytes {
			return "", "", errors.New("existing Planner packet is unsafe")
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(existing, content) {
			return "", "", errors.New("existing Planner packet differs from this generation")
		}
		return path, hash, nil
	}
	if err != nil {
		return "", "", err
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err == nil {
		dir, openErr := os.Open(directory)
		if openErr != nil {
			return "", "", openErr
		}
		err = errors.Join(dir.Sync(), dir.Close())
	}
	return path, hash, err
}

func ValidateInvocation(invocation Invocation) (Packet, error) {
	if invocation.InvocationID == "" || invocation.ProfileID == "" || !filepath.IsAbs(invocation.WorkDir) || filepath.Clean(invocation.WorkDir) != invocation.WorkDir || !filepath.IsAbs(invocation.PacketPath) || filepath.Clean(invocation.PacketPath) != invocation.PacketPath || len(invocation.PacketHash) != 64 || strings.TrimSpace(invocation.Prompt) == "" || invocation.Timeout <= 0 || invocation.MaxOutputBytes <= 0 || invocation.MaxOutputBytes > 128<<20 {
		return Packet{}, errors.New("invalid Planner invocation")
	}
	info, err := os.Lstat(invocation.PacketPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o222 != 0 || info.Size() > maxPacketBytes {
		return Packet{}, errors.New("Planner packet is missing, linked, writable, or oversized")
	}
	content, err := os.ReadFile(invocation.PacketPath)
	if err != nil {
		return Packet{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var packet Packet
	if err := decoder.Decode(&packet); err != nil {
		return Packet{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Packet{}, errors.New("Planner packet has trailing data")
	}
	if err := validatePacket(packet); err != nil {
		return Packet{}, err
	}
	canonicalPacket, err := canonical.Marshal(packet)
	if err != nil || !bytes.Equal(content, canonicalPacket) {
		return Packet{}, errors.New("Planner packet is not canonical")
	}
	hash, err := canonical.Hash("planner-packet", PacketVersion, packet)
	if err != nil || hash != invocation.PacketHash || packet.ProjectRoot != invocation.WorkDir {
		return Packet{}, errors.New("Planner packet does not match invocation")
	}
	expected, err := Schema()
	if err != nil {
		return Packet{}, err
	}
	if !sameJSON(expected, invocation.OutputSchema) {
		return Packet{}, errors.New("Planner output schema is not the xgoal proposal contract")
	}
	return packet, nil
}

func validatePacket(packet Packet) error {
	if packet.ProtocolVersion != PacketVersion || !validComponent(packet.GoalID) || strings.TrimSpace(packet.RawGoal) == "" || (packet.Mode != "fast" && packet.Mode != "standard") || !validHex(packet.ConfigHash, 64) || len(packet.TrustedValidators) == 0 || !filepath.IsAbs(packet.ProjectRoot) || filepath.Clean(packet.ProjectRoot) != packet.ProjectRoot {
		return errors.New("invalid Planner packet")
	}
	seen := make(map[string]struct{}, len(packet.TrustedValidators))
	for _, validator := range packet.TrustedValidators {
		if !validComponent(validator) {
			return errors.New("invalid trusted Validator id")
		}
		if _, exists := seen[validator]; exists {
			return errors.New("duplicate trusted Validator id")
		}
		seen[validator] = struct{}{}
	}
	if packet.ProjectNetwork != "deny" && packet.ProjectNetwork != "require-gate" && packet.ProjectNetwork != "allow" {
		return errors.New("invalid project network policy")
	}
	if packet.ProjectSecrets != "deny" && packet.ProjectSecrets != "require-gate" {
		return errors.New("invalid project secret policy")
	}
	return nil
}

func validComponent(value string) bool {
	return value != "" && value != "." && value != ".." && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\\r\n\x00")
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func sameJSON(left, right []byte) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || rightDecoder.Decode(&rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := canonical.Marshal(leftValue)
	rightCanonical, rightErr := canonical.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}
