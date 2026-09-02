package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/redact"
)

const maxCodexEventBytes = 8 << 20

type outputBudget struct {
	mu        sync.Mutex
	remaining int64
}

func (budget *outputBudget) consume(size int) error {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if size < 0 || int64(size) > budget.remaining {
		budget.remaining = 0
		return errors.New("Codex output exceeded its configured limit")
	}
	budget.remaining -= int64(size)
	return nil
}

type jsonlStream struct {
	mu          sync.Mutex
	pending     []byte
	directory   string
	rawPrefix   string
	sink        adapter.EventSink
	clock       gistClock
	budget      *outputBudget
	cancel      func()
	sequence    int
	parseErr    error
	sessionID   string
	finalText   string
	latestUsage *protocol.Usage
}

type gistClock interface {
	Now() time.Time
}

func newJSONLStream(directory, rawPrefix string, sink adapter.EventSink, source gistClock, budget *outputBudget, cancel func()) *jsonlStream {
	return &jsonlStream{directory: directory, rawPrefix: rawPrefix, sink: sink, clock: source, budget: budget, cancel: cancel}
}

func (stream *jsonlStream) Write(value []byte) (int, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.parseErr != nil {
		return len(value), stream.parseErr
	}
	if err := stream.budget.consume(len(value)); err != nil {
		stream.fail(err)
		return len(value), err
	}
	stream.pending = append(stream.pending, value...)
	for {
		newline := bytes.IndexByte(stream.pending, '\n')
		if newline < 0 {
			if len(stream.pending) > maxCodexEventBytes {
				err := errors.New("Codex JSONL event exceeded its per-line limit")
				stream.fail(err)
				return len(value), err
			}
			return len(value), nil
		}
		line := append([]byte(nil), stream.pending[:newline]...)
		stream.pending = stream.pending[newline+1:]
		if len(line) == 0 {
			err := errors.New("Codex JSONL contains an empty event")
			stream.fail(err)
			return len(value), err
		}
		if err := stream.processLine(line); err != nil {
			stream.fail(err)
			return len(value), err
		}
	}
}

func (stream *jsonlStream) Finalize(maxResultBytes int64) (protocol.AgentResult, string, *protocol.Usage, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.parseErr != nil {
		return protocol.AgentResult{}, stream.sessionID, cloneUsage(stream.latestUsage), stream.parseErr
	}
	if len(stream.pending) != 0 {
		return protocol.AgentResult{}, stream.sessionID, cloneUsage(stream.latestUsage), fmt.Errorf("%w: Codex JSONL ended without a newline", adapter.ErrInvalidOutput)
	}
	if stream.sessionID == "" || stream.finalText == "" {
		return protocol.AgentResult{}, stream.sessionID, cloneUsage(stream.latestUsage), fmt.Errorf("%w: Codex JSONL is missing session or final agent message", adapter.ErrInvalidOutput)
	}
	result, err := protocol.DecodeAgentResult(strings.NewReader(stream.finalText), maxResultBytes)
	if err != nil {
		return protocol.AgentResult{}, stream.sessionID, cloneUsage(stream.latestUsage), fmt.Errorf("%w: %v", adapter.ErrInvalidOutput, err)
	}
	result = redactResult(result)
	content, err := canonical.Marshal(result)
	if err != nil {
		return protocol.AgentResult{}, stream.sessionID, cloneUsage(stream.latestUsage), err
	}
	resultPath := filepath.Join(filepath.Dir(stream.directory), "result.json")
	if err := writeImmutable(resultPath, content, 0o600); err != nil {
		return protocol.AgentResult{}, stream.sessionID, cloneUsage(stream.latestUsage), err
	}
	if stream.sink != nil {
		event := protocol.AgentEvent{
			ProtocolVersion: protocol.AgentEventVersion, Type: "result", At: stream.clock.Now().UTC(),
			SessionID: stream.sessionID, Summary: result.Summary,
			RawRef: filepath.ToSlash(filepath.Join(stream.rawPrefix, "result.json")),
		}
		if err := event.Validate(); err != nil {
			return protocol.AgentResult{}, stream.sessionID, cloneUsage(stream.latestUsage), err
		}
		if err := stream.sink(event); err != nil {
			return protocol.AgentResult{}, stream.sessionID, cloneUsage(stream.latestUsage), err
		}
	}
	return result, stream.sessionID, cloneUsage(stream.latestUsage), nil
}

func (stream *jsonlStream) processLine(line []byte) error {
	if len(line) > maxCodexEventBytes {
		return errors.New("Codex JSONL event exceeded its per-line limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("%w: decode Codex JSONL: %v", adapter.ErrInvalidOutput, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: Codex JSONL event has trailing data", adapter.ErrInvalidOutput)
	}
	rawType, ok := stringField(raw, "type")
	if !ok || rawType == "" {
		return fmt.Errorf("%w: Codex JSONL event type is missing", adapter.ErrInvalidOutput)
	}
	stream.sequence++
	name := fmt.Sprintf("%06d.json", stream.sequence)
	sanitized, ok := redact.Value(raw).(map[string]any)
	if !ok {
		return errors.New("redacted Codex event changed JSON shape")
	}
	content, err := json.Marshal(sanitized)
	if err != nil {
		return err
	}
	if err := writeImmutable(filepath.Join(stream.directory, name), content, 0o600); err != nil {
		return err
	}
	rawRef := filepath.ToSlash(filepath.Join(stream.rawPrefix, "events", name))
	events, finalText, sessionID, usage, err := normalizeEvent(rawType, raw, rawRef, stream.clock.Now().UTC())
	if err != nil {
		return fmt.Errorf("%w: %v", adapter.ErrInvalidOutput, err)
	}
	if sessionID != "" {
		if stream.sessionID != "" && stream.sessionID != sessionID {
			return fmt.Errorf("%w: Codex session id changed", adapter.ErrInvalidOutput)
		}
		stream.sessionID = sessionID
	}
	if finalText != "" {
		stream.finalText = finalText
	}
	if usage != nil {
		stream.latestUsage = usage
	}
	for _, event := range events {
		if err := event.Validate(); err != nil {
			return err
		}
		if stream.sink != nil {
			if err := stream.sink(event); err != nil {
				return err
			}
		}
	}
	return nil
}

func (stream *jsonlStream) fail(err error) {
	stream.parseErr = err
	if stream.cancel != nil {
		stream.cancel()
	}
}

func (stream *jsonlStream) failure() error {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.parseErr
}

func normalizeEvent(rawType string, raw map[string]any, rawRef string, at time.Time) ([]protocol.AgentEvent, string, string, *protocol.Usage, error) {
	base := protocol.AgentEvent{ProtocolVersion: protocol.AgentEventVersion, At: at, RawRef: rawRef}
	switch rawType {
	case "thread.started":
		sessionID, ok := stringField(raw, "thread_id")
		if !ok || !validSessionID(sessionID) {
			return nil, "", "", nil, errors.New("thread.started has an invalid thread_id")
		}
		base.Type = "session"
		base.SessionID = sessionID
		base.Summary = "Codex session started"
		return []protocol.AgentEvent{base}, "", sessionID, nil, nil
	case "turn.started":
		base.Type = "turn"
		base.Summary = "Codex turn started"
		return []protocol.AgentEvent{base}, "", "", nil, nil
	case "turn.completed":
		usage := decodeUsage(raw["usage"])
		if usage == nil {
			base.Type = "turn"
			base.Summary = "Codex turn completed"
			return []protocol.AgentEvent{base}, "", "", nil, nil
		}
		base.Type = "usage"
		base.Summary = "Codex usage reported"
		base.Usage = usage
		return []protocol.AgentEvent{base}, "", "", usage, nil
	case "turn.failed", "error":
		base.Type = "message"
		base.Summary = redact.String(firstString(raw, "message", "error"))
		if base.Summary == "" {
			base.Summary = rawType
		}
		return []protocol.AgentEvent{base}, "", "", nil, nil
	case "item.started", "item.updated", "item.completed":
		return normalizeItem(rawType, raw["item"], base)
	default:
		base.Type = "unknown"
		base.Summary = redact.String(rawType)
		return []protocol.AgentEvent{base}, "", "", nil, nil
	}
}

func normalizeItem(rawType string, value any, base protocol.AgentEvent) ([]protocol.AgentEvent, string, string, *protocol.Usage, error) {
	item, ok := value.(map[string]any)
	if !ok {
		return nil, "", "", nil, errors.New("Codex item event has no object item")
	}
	itemType, ok := stringField(item, "type")
	if !ok || itemType == "" {
		return nil, "", "", nil, errors.New("Codex item event has no item type")
	}
	completed := rawType == "item.completed"
	switch itemType {
	case "agent_message":
		text, _ := stringField(item, "text")
		base.Type = "message"
		base.Summary = truncate(redact.String(text), 4096)
		if base.Summary == "" {
			base.Summary = "Codex agent message"
		}
		if completed {
			return []protocol.AgentEvent{base}, text, "", nil, nil
		}
		return []protocol.AgentEvent{base}, "", "", nil, nil
	case "command_execution":
		command := firstString(item, "command", "cmd")
		base.Type = "command"
		base.Summary = truncate(redact.String(command), 4096)
		if base.Summary == "" {
			base.Summary = "Codex command claim"
		}
		if argv := stringArray(item["argv"]); len(argv) > 0 {
			claim := &protocol.CommandClaim{Argv: argv}
			if exitCode, ok := intField(item, "exit_code"); ok {
				claim.ExitStatus = &exitCode
			}
			base.Command = claim
		}
		return []protocol.AgentEvent{base}, "", "", nil, nil
	case "file_change":
		changes, _ := item["changes"].([]any)
		events := make([]protocol.AgentEvent, 0, len(changes))
		for _, changeValue := range changes {
			change, ok := changeValue.(map[string]any)
			if !ok {
				continue
			}
			path, pathOK := stringField(change, "path")
			kind, kindOK := stringField(change, "kind")
			if !pathOK || !kindOK || path == "" || kind == "" {
				continue
			}
			event := base
			event.Type = "file_change"
			event.Summary = "Codex file change claim"
			event.FileChange = &protocol.FileChangeClaim{Path: redact.String(path), Kind: redact.String(kind)}
			events = append(events, event)
		}
		if len(events) == 0 {
			base.Type = "unknown"
			base.Summary = "Codex file_change item without changes"
			events = append(events, base)
		}
		return events, "", "", nil, nil
	case "reasoning", "todo_list", "mcp_tool_call", "web_search":
		base.Type = "message"
		base.Summary = "Codex " + itemType + " event"
		return []protocol.AgentEvent{base}, "", "", nil, nil
	default:
		base.Type = "unknown"
		base.Summary = "Codex item type " + redact.String(itemType)
		return []protocol.AgentEvent{base}, "", "", nil, nil
	}
}

func decodeUsage(value any) *protocol.Usage {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	usage := &protocol.Usage{}
	if value, ok := int64Field(object, "input_tokens"); ok && value >= 0 {
		usage.InputTokens = &value
	}
	if value, ok := int64Field(object, "output_tokens"); ok && value >= 0 {
		usage.OutputTokens = &value
	}
	if value, ok := int64Field(object, "cost_micros"); ok && value >= 0 {
		usage.CostMicros = &value
		usage.Currency = firstString(object, "currency")
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.CostMicros == nil {
		return nil
	}
	return usage
}

func redactResult(result protocol.AgentResult) protocol.AgentResult {
	result.Summary = redact.String(result.Summary)
	for index := range result.ChangedFilesClaimed {
		result.ChangedFilesClaimed[index] = redact.String(result.ChangedFilesClaimed[index])
	}
	for index := range result.ChecksClaimed {
		result.ChecksClaimed[index].Name = redact.String(result.ChecksClaimed[index].Name)
		result.ChecksClaimed[index].Status = redact.String(result.ChecksClaimed[index].Status)
	}
	for index := range result.Blockers {
		result.Blockers[index] = redact.String(result.Blockers[index])
	}
	for index := range result.Assumptions {
		result.Assumptions[index] = redact.String(result.Assumptions[index])
	}
	result.RecommendedNextAction = redact.String(result.RecommendedNextAction)
	return result
}

func stringField(object map[string]any, key string) (string, bool) {
	value, ok := object[key].(string)
	return value, ok
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := stringField(object, key); ok {
			return value
		}
	}
	return ""
}

func intField(object map[string]any, key string) (int, bool) {
	value, ok := int64Field(object, key)
	return int(value), ok && int64(int(value)) == value
}

func int64Field(object map[string]any, key string) (int64, bool) {
	value, ok := object[key].(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseInt(string(value), 10, 64)
	return parsed, err == nil
}

func stringArray(value any) []string {
	raw, ok := value.([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok || text == "" {
			return nil
		}
		result = append(result, redact.String(text))
	}
	return result
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func cloneUsage(usage *protocol.Usage) *protocol.Usage {
	if usage == nil {
		return nil
	}
	clone := *usage
	if usage.InputTokens != nil {
		value := *usage.InputTokens
		clone.InputTokens = &value
	}
	if usage.OutputTokens != nil {
		value := *usage.OutputTokens
		clone.OutputTokens = &value
	}
	if usage.CostMicros != nil {
		value := *usage.CostMicros
		clone.CostMicros = &value
	}
	return &clone
}

var _ io.Writer = (*jsonlStream)(nil)
