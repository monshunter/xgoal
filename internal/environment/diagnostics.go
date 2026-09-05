package environment

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/monshunter/xgoal/internal/redact"
)

const maxDiagnosticBytes = 4 << 20
const maxDiagnosticLine = 64 << 10

// diagnosticLog redacts complete lines before writing. Oversized partial lines
// are discarded, so split tokens cannot escape through a truncation boundary.
type diagnosticLog struct {
	mu        sync.Mutex
	file      *os.File
	pending   []byte
	received  int
	dropping  bool
	truncated bool
	closed    bool
	err       error
}

func newDiagnosticLog(path string) (*diagnosticLog, error) {
	file, err := openPrivateLog(path)
	if err != nil {
		return nil, err
	}
	return &diagnosticLog{file: file}, nil
}

func (log *diagnosticLog) Write(data []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.closed {
		return 0, os.ErrClosed
	}
	if log.err != nil {
		return 0, log.err
	}
	n := len(data)
	if log.truncated {
		return n, nil
	}
	if len(data) > maxDiagnosticBytes-log.received {
		log.truncated = true
		log.pending = nil
		log.write("[TRUNCATED: diagnostic output limit]\n")
		return n, log.err
	}
	log.received += len(data)
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		end := len(data)
		if i >= 0 {
			end = i + 1
		}
		if !log.dropping {
			if len(log.pending)+end > maxDiagnosticLine {
				log.pending = nil
				log.dropping = true
				log.write("[TRUNCATED: diagnostic line limit]\n")
			} else {
				log.pending = append(log.pending, data[:end]...)
			}
		}
		if i >= 0 {
			if !log.dropping {
				log.write(redact.String(string(log.pending)))
			}
			log.pending = nil
			log.dropping = false
		}
		data = data[end:]
	}
	return n, log.err
}

func (log *diagnosticLog) write(value string) {
	if log.err == nil {
		_, log.err = log.file.WriteString(value)
	}
}

func (log *diagnosticLog) Close() error {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.closed {
		return log.err
	}
	if !log.dropping && len(log.pending) > 0 {
		log.write(redact.String(string(log.pending)))
	}
	log.pending = nil
	log.closed = true
	log.err = errors.Join(log.err, log.file.Sync(), log.file.Close())
	return log.err
}

func commandDiagnostics(managed *managedEnvironment, id string) ([]*diagnosticLog, error) {
	if !validComponent(id) {
		return nil, fmt.Errorf("invalid diagnostic command id")
	}
	managed.logsMu.Lock()
	defer managed.logsMu.Unlock()
	if logs, ok := managed.commandLogs[id]; ok {
		return logs, nil
	}
	stdout, err := newDiagnosticLog(filepath.Join(managed.handle.Root, "logs", "commands", id+".stdout.log"))
	if err != nil {
		return nil, err
	}
	stderr, err := newDiagnosticLog(filepath.Join(managed.handle.Root, "logs", "commands", id+".stderr.log"))
	if err != nil {
		return nil, errors.Join(err, stdout.Close())
	}
	logs := []*diagnosticLog{stdout, stderr}
	managed.commandLogs[id] = logs
	return logs, nil
}
