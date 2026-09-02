package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/planner"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/redact"
)

const maxEventBytes = 8 << 20

type outputLimiter struct {
	mu        sync.Mutex
	remaining int64
}

func (limiter *outputLimiter) consume(size int) error {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if size < 0 || int64(size) > limiter.remaining {
		limiter.remaining = 0
		return errors.New("Claude output exceeded its configured limit")
	}
	limiter.remaining -= int64(size)
	return nil
}

type stream struct {
	mu                   sync.Mutex
	pending              []byte
	directory, rawPrefix string
	sink                 adapter.EventSink
	now                  func() time.Time
	limiter              *outputLimiter
	cancel               func()
	sequence             int
	err                  error
	sessionID            string
	structured           json.RawMessage
}

func newStream(directory, rawPrefix string, sink adapter.EventSink, now func() time.Time, limiter *outputLimiter, cancel func()) *stream {
	return &stream{directory: directory, rawPrefix: rawPrefix, sink: sink, now: now, limiter: limiter, cancel: cancel}
}

func (s *stream) Write(value []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return len(value), s.err
	}
	if err := s.limiter.consume(len(value)); err != nil {
		s.fail(err)
		return len(value), err
	}
	s.pending = append(s.pending, value...)
	for {
		index := bytes.IndexByte(s.pending, '\n')
		if index < 0 {
			if len(s.pending) > maxEventBytes {
				err := errors.New("Claude stream event exceeded its per-line limit")
				s.fail(err)
				return len(value), err
			}
			return len(value), nil
		}
		line := append([]byte(nil), s.pending[:index]...)
		s.pending = s.pending[index+1:]
		if len(line) == 0 {
			err := fmt.Errorf("%w: Claude stream contains an empty event", adapter.ErrInvalidOutput)
			s.fail(err)
			return len(value), err
		}
		if err := s.process(line); err != nil {
			s.fail(err)
			return len(value), err
		}
	}
}

func (s *stream) process(line []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("%w: decode Claude stream: %v", adapter.ErrInvalidOutput, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: Claude stream event has trailing JSON", adapter.ErrInvalidOutput)
	}
	typeName, ok := raw["type"].(string)
	if !ok || typeName == "" {
		return fmt.Errorf("%w: Claude event type is missing", adapter.ErrInvalidOutput)
	}
	s.sequence++
	name := fmt.Sprintf("%06d.json", s.sequence)
	sanitized := redact.Value(raw)
	if sanitizedMap, ok := sanitized.(map[string]any); ok {
		delete(sanitizedMap, "usage")
		delete(sanitizedMap, "total_cost_usd")
	}
	content, err := json.Marshal(sanitized)
	if err != nil {
		return err
	}
	if err := writeImmutable(filepath.Join(s.directory, name), content, 0o600); err != nil {
		return err
	}
	rawRef := filepath.ToSlash(filepath.Join(s.rawPrefix, "events", name))
	event := protocol.AgentEvent{ProtocolVersion: protocol.AgentEventVersion, At: s.now().UTC(), RawRef: rawRef}
	switch typeName {
	case "system":
		if subtype, _ := raw["subtype"].(string); subtype == "init" {
			session, _ := raw["session_id"].(string)
			if !validSessionID(session) {
				return fmt.Errorf("%w: Claude init has invalid session id", adapter.ErrInvalidOutput)
			}
			if s.sessionID != "" && s.sessionID != session {
				return fmt.Errorf("%w: Claude session id changed", adapter.ErrInvalidOutput)
			}
			s.sessionID = session
			event.Type = "session"
			event.SessionID = session
			event.Summary = "Claude session started"
		} else {
			event.Type = "unknown"
			event.Summary = "Claude system " + redact.String(subtype)
		}
	case "assistant":
		event.Type = "message"
		event.Summary = "Claude assistant message"
	case "user":
		event.Type = "message"
		event.Summary = "Claude tool result"
	case "result":
		isError, _ := raw["is_error"].(bool)
		if isError {
			return fmt.Errorf("%w: Claude result reported an error", adapter.ErrInvalidOutput)
		}
		session, _ := raw["session_id"].(string)
		if session != "" && (!validSessionID(session) || (s.sessionID != "" && s.sessionID != session)) {
			return fmt.Errorf("%w: Claude result session mismatch", adapter.ErrInvalidOutput)
		}
		if s.sessionID == "" {
			s.sessionID = session
		}
		structured, ok := raw["structured_output"]
		if !ok {
			return fmt.Errorf("%w: Claude result lacks structured_output", adapter.ErrInvalidOutput)
		}
		s.structured, err = canonical.Marshal(structured)
		if err != nil {
			return err
		}
		event.Type = "result"
		event.Summary = "Claude result reported"
		event.SessionID = s.sessionID
	default:
		event.Type = "unknown"
		event.Summary = "Claude event type " + redact.String(typeName)
	}
	if err := event.Validate(); err != nil {
		return err
	}
	if s.sink != nil {
		return s.sink(event)
	}
	return nil
}

func (s *stream) finalizeAgent(limit int64) (protocol.AgentResult, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.processPendingAtEOF(); err != nil {
		return protocol.AgentResult{}, s.sessionID, err
	}
	if s.err != nil {
		return protocol.AgentResult{}, s.sessionID, s.err
	}
	if len(s.pending) != 0 || s.sessionID == "" || len(s.structured) == 0 {
		return protocol.AgentResult{}, s.sessionID, fmt.Errorf("%w: Claude stream is incomplete", adapter.ErrInvalidOutput)
	}
	result, err := protocol.DecodeAgentResult(bytes.NewReader(s.structured), limit)
	if err != nil {
		return protocol.AgentResult{}, s.sessionID, fmt.Errorf("%w: %v", adapter.ErrInvalidOutput, err)
	}
	result = redactAgentResult(result)
	content, err := canonical.Marshal(result)
	if err != nil {
		return protocol.AgentResult{}, s.sessionID, err
	}
	if err := writeImmutable(filepath.Join(filepath.Dir(s.directory), "result.json"), content, 0o600); err != nil {
		return protocol.AgentResult{}, s.sessionID, err
	}
	return result, s.sessionID, nil
}

func (s *stream) finalizeReview(limit int64) (protocol.ReviewResult, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.processPendingAtEOF(); err != nil {
		return protocol.ReviewResult{}, s.sessionID, err
	}
	if s.err != nil {
		return protocol.ReviewResult{}, s.sessionID, s.err
	}
	if len(s.pending) != 0 || s.sessionID == "" || len(s.structured) == 0 {
		return protocol.ReviewResult{}, s.sessionID, fmt.Errorf("%w: Claude review stream is incomplete", adapter.ErrInvalidOutput)
	}
	result, err := protocol.DecodeReviewResult(bytes.NewReader(s.structured), limit)
	if err != nil {
		return protocol.ReviewResult{}, s.sessionID, fmt.Errorf("%w: %v", adapter.ErrInvalidOutput, err)
	}
	return redactReviewResult(result), s.sessionID, nil
}

func (s *stream) finalizePlanner(limit int64) (planner.Proposal, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.processPendingAtEOF(); err != nil {
		return planner.Proposal{}, s.sessionID, err
	}
	if s.err != nil {
		return planner.Proposal{}, s.sessionID, s.err
	}
	if len(s.pending) != 0 || s.sessionID == "" || len(s.structured) == 0 {
		return planner.Proposal{}, s.sessionID, fmt.Errorf("%w: Claude Planner stream is incomplete", adapter.ErrInvalidOutput)
	}
	result, err := planner.Decode(bytes.NewReader(s.structured), limit)
	if err != nil {
		return planner.Proposal{}, s.sessionID, fmt.Errorf("%w: %v", adapter.ErrInvalidOutput, err)
	}
	return result, s.sessionID, nil
}

func (s *stream) processPendingAtEOF() error {
	if len(s.pending) == 0 {
		return nil
	}
	line := append([]byte(nil), s.pending...)
	s.pending = nil
	if err := s.process(line); err != nil {
		s.err = err
		return err
	}
	return nil
}

func (s *stream) fail(err error) {
	s.err = err
	if s.cancel != nil {
		s.cancel()
	}
}
func (s *stream) failure() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }

func redactAgentResult(result protocol.AgentResult) protocol.AgentResult {
	result.Summary = redact.String(result.Summary)
	for i := range result.ChangedFilesClaimed {
		result.ChangedFilesClaimed[i] = redact.String(result.ChangedFilesClaimed[i])
	}
	for i := range result.Blockers {
		result.Blockers[i] = redact.String(result.Blockers[i])
	}
	for i := range result.Assumptions {
		result.Assumptions[i] = redact.String(result.Assumptions[i])
	}
	result.RecommendedNextAction = redact.String(result.RecommendedNextAction)
	return result
}

func redactReviewResult(result protocol.ReviewResult) protocol.ReviewResult {
	for i := range result.Findings {
		finding := &result.Findings[i]
		finding.Path = redact.String(finding.Path)
		finding.Claim = redact.String(finding.Claim)
		finding.Basis = redact.String(finding.Basis)
		finding.RecommendedFix = redact.String(finding.RecommendedFix)
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

var _ io.Writer = (*stream)(nil)
