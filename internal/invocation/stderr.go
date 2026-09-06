package invocation

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/monshunter/xgoal/internal/redact"
)

const maxStderrLine = 64 << 10
const maxStderrBytes = 4 << 20

// StderrLog publishes complete, redacted lines as immutable records. A partial
// credential is never written at a chunk boundary. Markers have their own
// small, reserved sidecar, independent of the exhausted output allowance.
type StderrLog struct {
	mu       sync.Mutex
	root     string
	dir      string
	pending  []byte
	seq      int64
	received int
	err      error
	closed   bool
}

func NewStderrLog(root, dir string) *StderrLog { return &StderrLog{root: root, dir: dir} }
func (l *StderrLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, os.ErrClosed
	}
	if l.err != nil {
		return 0, l.err
	}
	n := len(data)
	if n > maxStderrBytes-l.received {
		return n, l.fail(errors.New("stderr exceeded its durable output limit"))
	}
	l.received += n
	for len(data) > 0 {
		idx := bytes.IndexByte(data, '\n')
		take := len(data)
		if idx >= 0 {
			take = idx + 1
		}
		if len(l.pending)+take > maxStderrLine {
			return n, l.fail(errors.New("stderr exceeded its per-line limit"))
		}
		l.pending = append(l.pending, data[:take]...)
		data = data[take:]
		if idx >= 0 {
			if err := l.publish(); err != nil {
				return n, l.fail(err)
			}
		}
	}
	return n, nil
}
func (l *StderrLog) publish() error {
	if len(l.pending) == 0 {
		return nil
	}
	if l.seq >= MaxEvents {
		return errors.New("stderr exceeded its event count limit")
	}
	text := redact.String(strings.ToValidUTF8(string(l.pending), "�"))
	l.pending = nil
	data, err := json.Marshal(map[string]any{"type": "stderr", "text": text})
	if err != nil {
		return err
	}
	if err := writeLogFile(l.root, filepath.Join(l.dir, "stderr", "events", fmt.Sprintf("%06d.json", l.seq+1)), data); err != nil {
		return err
	}
	l.seq++
	return nil
}
func (l *StderrLog) fail(err error) error {
	l.pending = nil
	l.err = err
	_ = WriteLogStatus(l.root, l.dir, "stderr", l.seq, err, strings.Contains(err.Error(), "exceeded"))
	return err
}
func (l *StderrLog) Fail(err error) { l.mu.Lock(); defer l.mu.Unlock(); l.fail(err) }
func (l *StderrLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return l.err
	}
	l.closed = true
	if l.err == nil {
		if err := l.publish(); err != nil {
			l.fail(err)
		}
	}
	return l.err
}

type LogStatus struct {
	Stream    string `json:"stream"`
	Cursor    int64  `json:"cursor"`
	Error     string `json:"error"`
	Truncated bool   `json:"truncated"`
}

func WriteLogStatus(root, dir, stream string, cursor int64, cause error, truncated bool) error {
	if cause == nil {
		return nil
	}
	if stream != "stdout" && stream != "stderr" {
		return errors.New("invalid log stream")
	}
	reason := redact.String(cause.Error())
	if len(reason) > 2048 {
		reason = reason[:2048]
	}
	data, err := json.Marshal(LogStatus{Stream: stream, Cursor: cursor, Error: reason, Truncated: truncated})
	if err != nil {
		return err
	}
	err = writeLogFile(root, filepath.Join(dir, stream+"-status.json"), data)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}
func writeLogFile(root, path string, data []byte) error {
	if root == "" {
		root = filepath.Dir(path)
	} // standalone parser tests have no runtime registration
	relative, err := filepath.Rel(root, path)
	if err != nil || !Relative(relative) {
		return errors.New("log path escapes runtime root")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe log root")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	parts := strings.Split(filepath.Dir(relative), string(filepath.Separator))
	for i := 1; i <= len(parts); i++ {
		dir := filepath.Join(parts[:i]...)
		if dir == "." {
			continue
		}
		err := r.Mkdir(dir, 0700)
		if err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := r.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return errors.New("unsafe intermediate log directory")
		}
		parent, err := r.Open(filepath.Dir(dir))
		if err != nil {
			return err
		}
		err = parent.Sync()
		parent.Close()
		if err != nil {
			return err
		}
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(relative), ".artifact-"+hex.EncodeToString(nonce))
	f, err := r.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer r.Remove(temporary)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := r.Link(temporary, relative); err != nil {
		return err
	}
	d, err := r.Open(filepath.Dir(relative))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
