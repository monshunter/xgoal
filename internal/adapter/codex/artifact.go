package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/protocol"
)

const (
	invocationArtifactVersion = "xgoal.codex-invocation/v1alpha1"
	sessionBindingVersion     = "xgoal.codex-session-binding/v1alpha1"
	probeArtifactVersion      = "xgoal.codex-probe/v1alpha1"
)

type invocationArtifact struct {
	DelegationHash   string                  `json:"delegation_hash,omitempty"`
	ExecutionConfig  *config.ExecutionConfig `json:"execution_config,omitempty"`
	ProtocolVersion  string                  `json:"protocol_version"`
	InvocationID     string                  `json:"invocation_id"`
	AttemptID        string                  `json:"attempt_id"`
	WorkItemID       string                  `json:"work_item_id"`
	ProfileID        string                  `json:"profile_id"`
	GoalRevisionHash string                  `json:"goal_revision_hash"`
	PlanRevisionHash string                  `json:"plan_revision_hash"`
	BaseTree         string                  `json:"base_tree"`
	PacketHash       string                  `json:"packet_hash"`
	Role             string                  `json:"role"`
	WorkDir          string                  `json:"work_dir"`
	PacketPath       string                  `json:"packet_path"`
	SandboxPolicy    string                  `json:"sandbox_policy"`
	ToolPolicy       []string                `json:"tool_policy"`
	EnvironmentNames []string                `json:"environment_names"`
	OutputSchemaHash string                  `json:"output_schema_hash"`
	SessionPolicy    adapter.SessionPolicy   `json:"session_policy"`
	CreatedAt        time.Time               `json:"created_at"`
}

type sessionBinding struct {
	ProtocolVersion string             `json:"protocol_version"`
	SessionID       string             `json:"session_id"`
	Invocation      invocationArtifact `json:"invocation"`
	BindingHash     string             `json:"binding_hash"`
}

type probeArtifact struct {
	ProtocolVersion string               `json:"protocol_version"`
	ID              string               `json:"id"`
	ProfileID       string               `json:"profile_id"`
	Mode            adapter.ProbeMode    `json:"mode"`
	Capabilities    adapter.Capabilities `json:"capabilities"`
	Result          protocol.AgentResult `json:"result"`
	SessionID       string               `json:"session_id"`
	CreatedAt       time.Time            `json:"created_at"`
	ArtifactHash    string               `json:"artifact_hash"`
}

func invocationMetadata(invocation adapter.Invocation, workDir, packetPath, schemaHash string, createdAt time.Time) invocationArtifact {
	environmentNames := make([]string, 0, len(invocation.Environment))
	for name := range invocation.Environment {
		environmentNames = append(environmentNames, name)
	}
	sort.Strings(environmentNames)
	toolPolicy := append([]string(nil), invocation.ToolPolicy...)
	sort.Strings(toolPolicy)
	return invocationArtifact{
		DelegationHash:  protocol.DelegationHash(),
		ExecutionConfig: invocation.ExecutionConfig,
		ProtocolVersion: invocationArtifactVersion,
		InvocationID:    invocation.InvocationID, AttemptID: invocation.AttemptID,
		WorkItemID: invocation.WorkItemID, ProfileID: invocation.ProfileID,
		GoalRevisionHash: invocation.GoalRevisionHash, PlanRevisionHash: invocation.PlanRevisionHash,
		BaseTree: invocation.BaseTree, PacketHash: invocation.PacketHash,
		Role: string(invocation.Role), WorkDir: workDir, PacketPath: packetPath,
		SandboxPolicy: invocation.SandboxPolicy, ToolPolicy: toolPolicy,
		EnvironmentNames: environmentNames, OutputSchemaHash: schemaHash,
		SessionPolicy: invocation.SessionPolicy, CreatedAt: createdAt.UTC(),
	}
}

func newSessionBinding(sessionID string, artifact invocationArtifact) (sessionBinding, error) {
	if !validSessionID(sessionID) {
		return sessionBinding{}, errors.New("invalid Codex session id")
	}
	identity := struct {
		ProtocolVersion string             `json:"protocol_version"`
		SessionID       string             `json:"session_id"`
		Invocation      invocationArtifact `json:"invocation"`
	}{sessionBindingVersion, sessionID, comparableInvocation(artifact)}
	hash, err := canonical.Hash("codex-session-binding", sessionBindingVersion, identity)
	if err != nil {
		return sessionBinding{}, err
	}
	return sessionBinding{ProtocolVersion: sessionBindingVersion, SessionID: sessionID, Invocation: artifact, BindingHash: hash}, nil
}

func comparableInvocation(artifact invocationArtifact) invocationArtifact {
	artifact.InvocationID = ""
	artifact.CreatedAt = time.Time{}
	artifact.SessionPolicy = adapter.SessionResumeCompatible
	return artifact
}

func sameSessionBinding(stored sessionBinding, invocation invocationArtifact) bool {
	want, err := newSessionBinding(stored.SessionID, invocation)
	if err != nil {
		return false
	}
	storedComparable, err := newSessionBinding(stored.SessionID, stored.Invocation)
	return err == nil && stored.BindingHash == storedComparable.BindingHash && stored.BindingHash == want.BindingHash
}

func sessionFilename(root, sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return filepath.Join(root, "sessions", hex.EncodeToString(digest[:])+".json")
}

func writeSession(root string, binding sessionBinding) error {
	if binding.ProtocolVersion != sessionBindingVersion || !validHash(binding.BindingHash, 64) {
		return errors.New("invalid Codex session binding")
	}
	content, err := canonical.Marshal(binding)
	if err != nil {
		return err
	}
	filename := sessionFilename(root, binding.SessionID)
	if existing, err := readSession(root, binding.SessionID); err == nil {
		if sameSessionBinding(existing, binding.Invocation) {
			return nil
		}
		return fmt.Errorf("%w: session %q already has different provenance", adapter.ErrSessionMismatch, binding.SessionID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeImmutable(filename, content, 0o600)
}

func readSession(root, sessionID string) (sessionBinding, error) {
	read, err := readCanonicalFile[sessionBinding](sessionFilename(root, sessionID), 1<<20)
	if err != nil {
		return sessionBinding{}, err
	}
	stored := read.value
	if stored.ProtocolVersion != sessionBindingVersion || stored.SessionID != sessionID || !validHash(stored.BindingHash, 64) {
		return sessionBinding{}, fmt.Errorf("%w: invalid persisted Codex session", adapter.ErrSessionMismatch)
	}
	verified, err := newSessionBinding(sessionID, stored.Invocation)
	if err != nil || verified.BindingHash != stored.BindingHash {
		return sessionBinding{}, fmt.Errorf("%w: Codex session binding hash changed", adapter.ErrSessionMismatch)
	}
	return stored, nil
}

type canonicalRead[T any] struct {
	value T
}

func readCanonicalFile[T any](filename string, limit int64) (canonicalRead[T], error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return canonicalRead[T]{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() > limit {
		return canonicalRead[T]{}, errors.New("Codex artifact is linked, unsafe, or oversized")
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		return canonicalRead[T]{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return canonicalRead[T]{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return canonicalRead[T]{}, errors.New("Codex artifact contains trailing JSON")
	}
	canonicalContent, err := canonical.Marshal(value)
	if err != nil || !bytes.Equal(content, canonicalContent) {
		return canonicalRead[T]{}, errors.New("Codex artifact is not canonical")
	}
	return canonicalRead[T]{value: value}, nil
}

func writeImmutable(filename string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(filename)
	if err := ensurePrivateDirectory(directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".artifact-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, filename); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Codex artifact directory is missing or unsafe")
	}
	return os.Chmod(path, 0o700)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func validComponent(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 160 && utf8.ValidString(value) && !strings.ContainsAny(value, "/\\\r\n\x00")
}

func validSessionID(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\\r\n\x00")
}

func validHash(value string, length int) bool {
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
