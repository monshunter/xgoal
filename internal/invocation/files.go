package invocation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/monshunter/xgoal/internal/redact"
)

const MaxEventBytes int64 = 16 << 20
const MaxLogBytes int64 = 64 << 20
const MaxEvents int64 = 16384

func SHA256(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

// ReadFile confines reads to the registered runtime root. No intermediate or
// final symlinks, device nodes, public modes or unbounded reads are accepted.
func ReadFile(root, relative string, limit int64) ([]byte, os.FileInfo, error) {
	if !filepath.IsAbs(root) || !Relative(relative) || limit <= 0 {
		return nil, nil, errors.New("invalid private file path")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("unsafe runtime directory")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	parts := strings.Split(relative, string(filepath.Separator))
	for i := 1; i < len(parts); i++ {
		info, err := r.Lstat(filepath.Join(parts[:i]...))
		if err != nil {
			return nil, nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return nil, nil, errors.New("unsafe artifact directory")
		}
	}
	before, err := r.Lstat(relative)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() > limit {
		return nil, nil, errors.New("artifact is nonregular, public or oversized")
	}
	f, err := r.OpenFile(relative, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, nil, errors.New("artifact changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, nil, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || after.ModTime() != before.ModTime() || int64(len(data)) != before.Size() || int64(len(data)) > limit {
		return nil, nil, errors.New("artifact changed while reading")
	}
	return data, after, nil
}

// Public strips native private-thinking blocks before observation or delivery.
// Public text, tool calls and results remain available, after redaction.
func Public(value any) any {
	switch v := value.(type) {
	case map[string]any:
		kind, _ := v["type"].(string)
		if kind == "thinking" || kind == "redacted_thinking" || kind == "thinking_delta" || kind == "signature_delta" || kind == "reasoning" {
			return map[string]any{"type": "private_content_omitted"}
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			switch key {
			case "changes", "kind", "type", "subtype", "id", "session_id", "thread_id", "model", "role", "message", "item", "content", "text", "name", "command", "tool_use_id", "status", "exit_code", "aggregated_output", "is_error", "error", "result", "structured_output", "summary", "protocol_version", "blockers", "assumptions", "recommended_next_action", "checks_claimed", "changed_files_claimed", "review_status", "findings", "severity", "path", "claim", "basis", "recommended_fix":
				out[key] = Public(item)
			case "input", "arguments", "output":
				// Tool payloads have tool-defined keys. They are public actions,
				// not arbitrary top-level native metadata.
				out[key] = redact.Value(item)
			}
		}
		return redact.Value(out)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = Public(item)
		}
		return out
	default:
		return redact.Value(value)
	}
}

type LogEvent struct {
	Sequence int64           `json:"sequence"`
	At       time.Time       `json:"at"`
	SHA256   string          `json:"sha256"`
	Data     json.RawMessage `json:"data"`
}

func readEvent(root, dir string, seq int64) (LogEvent, int64, error) {
	data, info, err := ReadFile(root, filepath.Join(dir, "events", fmt.Sprintf("%06d.json", seq)), MaxEventBytes)
	if err != nil {
		return LogEvent{}, 0, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return LogEvent{}, 0, fmt.Errorf("invalid event %d: %w", seq, err)
	}
	if typ, _ := raw["type"].(string); typ == "" {
		return LogEvent{}, 0, errors.New("event type missing")
	}
	sanitized, err := json.Marshal(Public(raw))
	if err != nil {
		return LogEvent{}, 0, err
	}
	return LogEvent{Sequence: seq, At: info.ModTime().UTC(), SHA256: SHA256(data), Data: sanitized}, int64(len(data)), nil
}

// Scan advances only across contiguous, already durable events. It does no DB
// work, so hashing and log readers never hold the control connection.
func Scan(ctx context.Context, root, dir string, prior Observation) (Observation, error) {
	if !Relative(dir) || prior.Cursor < 0 || prior.Cursor > MaxEvents {
		return prior, errors.New("invalid invocation log cursor")
	}
	out := prior
	if out.ObservedModel == "" {
		out.ObservedModel = "unknown"
	}
	for scanned := 0; out.Cursor < MaxEvents && scanned < 256; scanned++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		entry, size, err := readEvent(root, dir, out.Cursor+1)
		if errors.Is(err, os.ErrNotExist) {
			return out, checkGap(root, dir, out.Cursor)
		}
		if err != nil {
			return out, err
		}
		if out.Bytes+size > MaxLogBytes {
			out.Truncated = true
			return out, errors.New("durable invocation log exceeded limit")
		}
		var raw map[string]any
		_ = json.Unmarshal(entry.Data, &raw)
		typ, _ := raw["type"].(string)
		session, _ := raw["session_id"].(string)
		if typ == "thread.started" {
			session, _ = raw["thread_id"].(string)
		}
		if session != "" {
			if out.SessionID != "" && out.SessionID != session {
				return out, errors.New("event session identity changed")
			}
			out.SessionID = session
		}
		if typ == "system" || typ == "thread.started" {
			if model, _ := raw["model"].(string); model != "" {
				out.ObservedModel = model
			}
		}
		out.Cursor = entry.Sequence
		out.Bytes += size
		at := entry.At
		out.LastOutputAt = &at
	}
	return out, nil
}

func checkGap(root, dir string, cursor int64) error {
	// OpenRoot also prevents directory replacement from escaping the root.
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	path := filepath.Join(dir, "events")
	info, err := r.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if cursor == 0 {
			return nil
		}
		return errors.New("previously indexed events are missing")
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe events directory")
	}
	f, err := r.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	count := 0
	hasNext, hasLater := false, false
	for {
		names, err := f.Readdirnames(256)
		for _, name := range names {
			count++
			if count > int(MaxEvents)+64 {
				return errors.New("too many invocation event files")
			}
			if strings.HasPrefix(name, ".artifact-") {
				continue
			}
			n, e := strconv.ParseInt(strings.TrimSuffix(name, ".json"), 10, 64)
			if e != nil || name != fmt.Sprintf("%06d.json", n) || n <= 0 || n > MaxEvents {
				return errors.New("invalid invocation event filename")
			}
			if n == cursor+1 {
				hasNext = true
			}
			if n > cursor+1 {
				hasLater = true
			}
		}
		if errors.Is(err, io.EOF) {
			if hasLater && !hasNext {
				return fmt.Errorf("invocation log gap after %d", cursor)
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func ReadPage(ctx context.Context, root, dir string, after, boundary int64, limit int) ([]LogEvent, error) {
	if after < 0 || boundary < 0 || after > boundary || boundary > MaxEvents || limit < 1 || limit > 256 {
		return nil, errors.New("invalid invocation log page")
	}
	result := make([]LogEvent, 0)
	var bytes int64
	for seq := after + 1; seq <= boundary && len(result) < limit; seq++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry, size, err := readEvent(root, dir, seq)
		if err != nil {
			return nil, err
		}
		if bytes+size > MaxEventBytes && len(result) > 0 {
			break
		}
		result = append(result, entry)
		bytes += size
	}
	return result, nil
}

// Inspect combines independently resumable stdout/stderr streams. Markers are
// read even when the failing write never produced an event file.
func Inspect(ctx context.Context, root, dir string, prior Observation) (Observation, error) {
	out, stdoutErr := Scan(ctx, root, dir, prior)
	stderr, stderrErr := Scan(ctx, root, filepath.Join(dir, "stderr"), Observation{Cursor: prior.StderrCursor, Bytes: prior.StderrBytes})
	out.StderrCursor = stderr.Cursor
	out.StderrBytes = stderr.Bytes
	out.Truncated = out.Truncated || stderr.Truncated
	if stderr.LastOutputAt != nil && (out.LastOutputAt == nil || stderr.LastOutputAt.After(*out.LastOutputAt)) {
		out.LastOutputAt = stderr.LastOutputAt
	}
	for _, stream := range []string{"stdout", "stderr"} {
		data, _, err := ReadFile(root, filepath.Join(dir, stream+"-status.json"), 4096)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, err
		}
		var status LogStatus
		if err := json.Unmarshal(data, &status); err != nil {
			return out, err
		}
		if status.Stream != stream || status.Cursor < 0 || status.Cursor > MaxEvents {
			return out, errors.New("invalid log status marker")
		}
		out.Truncated = out.Truncated || status.Truncated
		out.LogError = redact.String(stream + ": " + status.Error)
	}
	return out, errors.Join(stdoutErr, stderrErr)
}
