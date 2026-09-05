package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	invocationVersion = "xgoal.claude-invocation/v1alpha1"
	sessionVersion    = "xgoal.claude-session-binding/v1alpha1"
	probeVersion      = "xgoal.claude-probe/v1alpha1"
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
	PermissionMode   string                  `json:"permission_mode"`
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

func metadata(inv adapter.Invocation, workDir, packetPath, schemaHash string, now time.Time) invocationArtifact {
	names := make([]string, 0, len(inv.Environment))
	for name := range inv.Environment {
		names = append(names, name)
	}
	sort.Strings(names)
	tools := append([]string(nil), inv.ToolPolicy...)
	sort.Strings(tools)
	return invocationArtifact{
		DelegationHash:  protocol.DelegationHash(),
		ExecutionConfig: inv.ExecutionConfig,
		ProtocolVersion: invocationVersion, InvocationID: inv.InvocationID, AttemptID: inv.AttemptID,
		WorkItemID: inv.WorkItemID, ProfileID: inv.ProfileID, GoalRevisionHash: inv.GoalRevisionHash,
		PlanRevisionHash: inv.PlanRevisionHash, BaseTree: inv.BaseTree, PacketHash: inv.PacketHash,
		Role: string(inv.Role), WorkDir: workDir, PacketPath: packetPath, PermissionMode: inv.PermissionMode,
		ToolPolicy: tools, EnvironmentNames: names, OutputSchemaHash: schemaHash,
		SessionPolicy: inv.SessionPolicy, CreatedAt: now.UTC(),
	}
}

func comparable(value invocationArtifact) invocationArtifact {
	value.InvocationID = ""
	value.CreatedAt = time.Time{}
	value.SessionPolicy = adapter.SessionResumeCompatible
	return value
}

func newBinding(sessionID string, invocation invocationArtifact) (sessionBinding, error) {
	if !validSessionID(sessionID) {
		return sessionBinding{}, errors.New("invalid Claude session id")
	}
	identity := struct {
		ProtocolVersion string             `json:"protocol_version"`
		SessionID       string             `json:"session_id"`
		Invocation      invocationArtifact `json:"invocation"`
	}{sessionVersion, sessionID, comparable(invocation)}
	hash, err := canonical.Hash("claude-session-binding", sessionVersion, identity)
	if err != nil {
		return sessionBinding{}, err
	}
	return sessionBinding{ProtocolVersion: sessionVersion, SessionID: sessionID, Invocation: invocation, BindingHash: hash}, nil
}

func sessionPath(root, sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return filepath.Join(root, "sessions", hex.EncodeToString(digest[:])+".json")
}

func writeSession(root string, binding sessionBinding) error {
	content, err := canonical.Marshal(binding)
	if err != nil {
		return err
	}
	path := sessionPath(root, binding.SessionID)
	if existing, err := readSession(root, binding.SessionID); err == nil {
		wanted, _ := newBinding(binding.SessionID, binding.Invocation)
		if existing.BindingHash == wanted.BindingHash {
			return nil
		}
		return adapter.ErrSessionMismatch
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeImmutable(path, content, 0o600)
}

func readSession(root, sessionID string) (sessionBinding, error) {
	var stored sessionBinding
	if err := readCanonical(sessionPath(root, sessionID), 0o600, 1<<20, &stored); err != nil {
		return stored, err
	}
	verified, err := newBinding(sessionID, stored.Invocation)
	if err != nil || stored.ProtocolVersion != sessionVersion || stored.SessionID != sessionID || stored.BindingHash != verified.BindingHash {
		return sessionBinding{}, adapter.ErrSessionMismatch
	}
	return stored, nil
}

func sameBinding(stored sessionBinding, current invocationArtifact) bool {
	wanted, err := newBinding(stored.SessionID, current)
	return err == nil && stored.BindingHash == wanted.BindingHash
}

func readCanonical(path string, mode os.FileMode, limit int64, target any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode || info.Size() > limit {
		return errors.New("Claude artifact is linked, unsafe, or oversized")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("Claude artifact contains trailing JSON")
	}
	canonicalContent, err := canonical.Marshal(target)
	if err != nil || !bytes.Equal(content, canonicalContent) {
		return errors.New("Claude artifact is not canonical")
	}
	return nil
}

func writeImmutable(path string, content []byte, mode os.FileMode) error {
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".artifact-")
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
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Claude artifact directory is missing or unsafe")
	}
	return os.Chmod(path, 0o700)
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
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
