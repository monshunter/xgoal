package invocation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/planner"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/redact"
)

type Summary struct {
	ID          string      `json:"id"`
	GoalID      string      `json:"goal_id"`
	Role        string      `json:"role"`
	ProfileID   string      `json:"profile_id"`
	OwnerKind   string      `json:"owner_kind"`
	OwnerID     string      `json:"owner_id"`
	Generation  int64       `json:"generation"`
	Observation Observation `json:"observation"`
}

func (r Record) Summary() Summary {
	return Summary{ID: r.Input.ID, GoalID: r.Input.GoalID, Role: r.Input.Role, ProfileID: r.Input.ProfileID, OwnerKind: r.Input.OwnerKind, OwnerID: r.Input.OwnerID, Generation: r.Input.Generation, Observation: r.Observation}
}

type Context struct {
	Invocation      Record            `json:"invocation"`
	Packet          json.RawMessage   `json:"packet"`
	Metadata        json.RawMessage   `json:"metadata,omitempty"`
	Result          json.RawMessage   `json:"result,omitempty"`
	ArtifactErrors  map[string]string `json:"artifact_errors,omitempty"`
	LoadObservation string            `json:"load_observation"`
}

func ReadContext(ctx context.Context, root string, record Record) (Context, error) {
	result := Context{Invocation: record, LoadObservation: "unknown", ArtifactErrors: map[string]string{}}
	if err := record.Input.Validate(); err != nil {
		return result, err
	}
	data, _, err := ReadFile(root, record.Input.PacketPath, 16<<20)
	if err != nil {
		return result, err
	}
	if SHA256(data) != record.Input.PacketSHA256 {
		return result, errors.New("registered invocation Packet content changed")
	}
	result.Packet, err = redactedJSON(data)
	if err != nil {
		return result, err
	}
	for _, name := range []string{"invocation.json", "result.json"} {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		data, _, err := ReadFile(root, filepath.Join(record.Input.ProviderDir, name), 16<<20)
		if err != nil {
			result.ArtifactErrors[name] = redact.String(err.Error())
			continue
		}
		if name == "invocation.json" {
			data, err = publicMetadata(data, record.Input)
		} else {
			data, err = publicResult(data, record.Input.Role)
		}
		if err != nil {
			result.ArtifactErrors[name] = redact.String(err.Error())
			continue
		}
		if name == "invocation.json" {
			result.Metadata = data
		} else {
			result.Result = data
		}
	}
	return result, nil
}
func redactedJSON(data []byte) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return json.Marshal(redact.Value(value))
}

type LogPage struct {
	Invocation    Summary    `json:"invocation"`
	Stream        string     `json:"stream"`
	After         int64      `json:"after"`
	Next          int64      `json:"next"`
	DurableCursor int64      `json:"durable_cursor"`
	Events        []LogEvent `json:"events"`
	Complete      bool       `json:"complete"`
}

func Page(ctx context.Context, root string, record Record, stream string, after int64, limit int) (LogPage, error) {
	page := LogPage{Invocation: record.Summary(), Stream: stream, After: after, Next: after}
	if err := record.Input.Validate(); err != nil {
		return page, err
	}
	if record.Observation.Unavailable != "" {
		return page, errors.New(record.Observation.Unavailable)
	}
	dir := record.Input.ProviderDir
	boundary := record.Observation.Cursor
	switch stream {
	case "stdout":
	case "stderr":
		dir = filepath.Join(dir, "stderr")
		boundary = record.Observation.StderrCursor
	default:
		return page, errors.New("stream must be stdout or stderr")
	}
	page.DurableCursor = boundary
	events, err := ReadPage(ctx, root, dir, after, boundary, limit)
	if err != nil {
		return page, err
	}
	page.Events = events
	if len(events) > 0 {
		page.Next = events[len(events)-1].Sequence
	}
	if record.Observation.Status != "running" && page.Next == boundary {
		_, _, err := readEvent(root, dir, boundary+1)
		if errors.Is(err, os.ErrNotExist) {
			page.Complete = true
		} else if err != nil {
			return page, err
		}
	}
	return page, nil
}

func publicMetadata(data []byte, in Input) (json.RawMessage, error) {
	var raw map[string]any
	if !json.Valid(data) {
		return nil, errors.New("invalid Provider metadata JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	if raw["invocation_id"] != in.ID || raw["packet_hash"] != in.PacketHash {
		return nil, errors.New("Provider metadata does not match registered invocation")
	}
	expected := map[string]any{"delegation_hash": in.DelegationHash, "execution_config": in.ExecutionConfig}
	switch in.Role {
	case "planner":
		expected["profile_id"] = in.ProfileID
		expected["request_hash"] = in.RequestHash
		expected["input_tree"] = in.InputTree
		expected["generation"] = in.Generation
	case "implementer":
		expected["profile_id"] = in.ProfileID
		expected["attempt_id"] = in.OwnerID
		expected["role"] = in.Role
		expected["base_tree"] = in.InputTree
		expected["goal_revision_hash"] = in.GoalRevisionHash
		expected["plan_revision_hash"] = in.PlanRevisionHash
	case "reviewer":
		expected["reviewer_profile_id"] = in.ProfileID
		expected["candidate_tree"] = in.InputTree
	case "acceptance":
		expected["profile_id"] = in.ProfileID
		expected["role"] = in.Role
		expected["config_hash"] = in.ConfigHash
		expected["tree_hash"] = in.InputTree
		expected["goal_revision_hash"] = in.GoalRevisionHash
	}
	if _, ok := raw["sandbox_policy"]; ok {
		expected["sandbox_policy"] = in.ExecutionConfig.Sandbox
	}
	if _, ok := raw["permission_mode"]; ok {
		expected["permission_mode"] = in.ExecutionConfig.PermissionMode
	}
	for _, key := range []string{"tools", "tool_policy"} {
		if value, ok := raw[key]; ok {
			data, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			var names []string
			if err := json.Unmarshal(data, &names); err != nil {
				return nil, errors.New("invalid metadata tool list")
			}
			sort.Strings(names)
			want := append([]string(nil), in.ExecutionConfig.Tools...)
			sort.Strings(want)
			a, _ := json.Marshal(names)
			b, _ := json.Marshal(want)
			if !bytes.Equal(a, b) {
				return nil, errors.New("Provider metadata tools differ from registered input")
			}
			expected[key] = value
		}
	}
	for key, want := range expected {
		actual, ok := raw[key]
		if !ok {
			return nil, fmt.Errorf("Provider metadata is missing %s", key)
		}
		a, err := canonical.Marshal(actual)
		if err != nil {
			return nil, err
		}
		b, err := canonical.Marshal(want)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(a, b) {
			return nil, fmt.Errorf("Provider metadata %s differs from registered input", key)
		}
	}
	// Only verified metadata fields are exposed; schema-transform hashes and
	// native-only bookkeeping remain in the referenced private artifact.
	out := map[string]any{"invocation_id": in.ID, "packet_hash": in.PacketHash}
	for key := range expected {
		out[key] = raw[key]
	}

	return json.Marshal(redact.Value(out))
}
func publicResult(data []byte, role string) (json.RawMessage, error) {
	var value any
	var err error
	switch role {
	case "planner":
		value, err = planner.Decode(bytes.NewReader(data), 16<<20)
	case "reviewer":
		value, err = protocol.DecodeReviewResult(bytes.NewReader(data), 16<<20)
	default:
		value, err = protocol.DecodeAgentResult(bytes.NewReader(data), 16<<20)
	}
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return redactedJSON(encoded)
}
