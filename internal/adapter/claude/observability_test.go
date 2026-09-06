package claude

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/protocol"
)

func TestEventLimitIncludesLineWithTerminatingNewline(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-write", true: "split-final-newline"}[split], func(t *testing.T) {
			s := newStream(t.TempDir(), "events", nil, time.Now, &outputLimiter{remaining: 32 << 20}, func() {})
			data := append([]byte(`{"type":"system","subtype":"notice","text":"`), bytes.Repeat([]byte("x"), maxEventBytes)...)
			data = append(data, []byte("\"}\n")...)
			var err error
			if split {
				_, err = s.Write(data[:maxEventBytes])
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.Write(data[maxEventBytes:])
			} else {
				_, err = s.Write(data)
			}
			if err == nil || !strings.Contains(err.Error(), "per-line limit") {
				t.Fatalf("oversized newline event accepted: %v", err)
			}
		})
	}
}
func TestStructuredChecksAreRedacted(t *testing.T) {
	result := redactAgentResult(protocol.AgentResult{ChecksClaimed: []protocol.CheckClaim{{Name: "token=check-sentinel", Status: "password=status-sentinel"}}})
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "sentinel") {
		t.Fatalf("result leaks: %s", data)
	}
}
