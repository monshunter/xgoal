package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"path/filepath"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/redact"
)

const maxEventBytes = 8 << 20

type outputBudget struct {
	mu        sync.Mutex
	remaining int64
}

func (budget *outputBudget) consume(size int) error {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if size < 0 || int64(size) > budget.remaining {
		budget.remaining = 0
		return errors.New("Claude output exceeded its configured limit")
	}
	budget.remaining -= int64(size)
	return nil
}

type stream struct {
	mu                   sync.Mutex
	pending              []byte
	directory, rawPrefix string
	sink                 adapter.EventSink
	now                  func() time.Time
	budget               *outputBudget
	cancel               func()
	sequence             int
	err                  error
	sessionID            string
	structured           json.RawMessage
	usage                *protocol.Usage
}

func newStream(directory, rawPrefix string, sink adapter.EventSink, now func() time.Time, budget *outputBudget, cancel func()) *stream {
	return &stream{directory: directory, rawPrefix: rawPrefix, sink: sink, now: now, budget: budget, cancel: cancel}
}

func (s *stream) Write(value []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return len(value), s.err
	}
	if err := s.budget.consume(len(value)); err != nil {
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
		s.usage = decodeUsage(raw)
		event.Type = "usage"
		event.Summary = "Claude result and usage reported"
		event.SessionID = s.sessionID
		event.Usage = cloneUsage(s.usage)
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

func (s *stream) finalizeAgent(limit int64) (protocol.AgentResult, string, *protocol.Usage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.processPendingAtEOF(); err != nil {
		return protocol.AgentResult{}, s.sessionID, cloneUsage(s.usage), err
	}
	if s.err != nil {
		return protocol.AgentResult{}, s.sessionID, cloneUsage(s.usage), s.err
	}
	if len(s.pending) != 0 || s.sessionID == "" || len(s.structured) == 0 {
		return protocol.AgentResult{}, s.sessionID, cloneUsage(s.usage), fmt.Errorf("%w: Claude stream is incomplete", adapter.ErrInvalidOutput)
	}
	result, err := protocol.DecodeAgentResult(bytes.NewReader(s.structured), limit)
	if err != nil {
		return protocol.AgentResult{}, s.sessionID, cloneUsage(s.usage), fmt.Errorf("%w: %v", adapter.ErrInvalidOutput, err)
	}
	result = redactAgentResult(result)
	content, err := canonical.Marshal(result)
	if err != nil {
		return protocol.AgentResult{}, s.sessionID, cloneUsage(s.usage), err
	}
	if err := writeImmutable(filepath.Join(filepath.Dir(s.directory), "result.json"), content, 0o600); err != nil {
		return protocol.AgentResult{}, s.sessionID, cloneUsage(s.usage), err
	}
	return result, s.sessionID, cloneUsage(s.usage), nil
}

func (s *stream) finalizeReview(limit int64) (protocol.ReviewResult, string, *protocol.Usage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.processPendingAtEOF(); err != nil {
		return protocol.ReviewResult{}, s.sessionID, cloneUsage(s.usage), err
	}
	if s.err != nil {
		return protocol.ReviewResult{}, s.sessionID, cloneUsage(s.usage), s.err
	}
	if len(s.pending) != 0 || s.sessionID == "" || len(s.structured) == 0 {
		return protocol.ReviewResult{}, s.sessionID, cloneUsage(s.usage), fmt.Errorf("%w: Claude review stream is incomplete", adapter.ErrInvalidOutput)
	}
	result, err := protocol.DecodeReviewResult(bytes.NewReader(s.structured), limit)
	if err != nil {
		return protocol.ReviewResult{}, s.sessionID, cloneUsage(s.usage), fmt.Errorf("%w: %v", adapter.ErrInvalidOutput, err)
	}
	return redactReviewResult(result), s.sessionID, cloneUsage(s.usage), nil
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

func decodeUsage(raw map[string]any) *protocol.Usage {
	usageObject, _ := raw["usage"].(map[string]any)
	usage := &protocol.Usage{}
	if value, ok := integer(usageObject["input_tokens"]); ok && value >= 0 {
		usage.InputTokens = &value
	}
	if value, ok := integer(usageObject["output_tokens"]); ok && value >= 0 {
		usage.OutputTokens = &value
	}
	if number, ok := raw["total_cost_usd"].(json.Number); ok {
		if micros, ok := usdMicros(string(number)); ok {
			usage.CostMicros = &micros
			usage.Currency = "USD"
		}
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.CostMicros == nil {
		return nil
	}
	return usage
}

func integer(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseInt(string(number), 10, 64)
	return parsed, err == nil
}
func usdMicros(value string) (int64, bool) {
	rational, ok := new(big.Rat).SetString(value)
	if !ok || rational.Sign() < 0 {
		return 0, false
	}
	rational.Mul(rational, big.NewRat(1_000_000, 1))
	quotient := new(big.Int)
	remainder := new(big.Int)
	quotient.QuoRem(rational.Num(), rational.Denom(), remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(rational.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, false
	}
	return quotient.Int64(), true
}

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

func cloneUsage(usage *protocol.Usage) *protocol.Usage {
	if usage == nil {
		return nil
	}
	clone := *usage
	if usage.InputTokens != nil {
		v := *usage.InputTokens
		clone.InputTokens = &v
	}
	if usage.OutputTokens != nil {
		v := *usage.OutputTokens
		clone.OutputTokens = &v
	}
	if usage.CostMicros != nil {
		v := *usage.CostMicros
		clone.CostMicros = &v
	}
	return &clone
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
