package claude

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
)

func TestNativeProviderErrorPreservesBoundedRedactedCause(t *testing.T) {
	s := newStream(t.TempDir(), "fixture", nil, time.Now, &outputLimiter{remaining: 1 << 20}, nil)
	_, err := s.Write([]byte(`{"type":"result","is_error":true,"result":"Not logged in; token=private-token-value","terminal_reason":"api_error"}` + "\n"))
	if !errors.Is(err, adapter.ErrUnavailable) || errors.Is(err, adapter.ErrInvalidOutput) || !strings.Contains(err.Error(), "Not logged in") || strings.Contains(err.Error(), "private-token-value") {
		t.Fatalf("native operational failure lost or unsafe: %v", err)
	}
	if len(s.structured) != 0 {
		t.Fatal("native failure became a role result")
	}
}
