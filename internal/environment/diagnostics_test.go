package environment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticLogsRedactSplitSecretsAndBoundOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.log")
	log, err := newDiagnosticLog(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"starting\nTOKEN=split-", "secret\nnormal\n", strings.Repeat("x", maxDiagnosticLine+1), "\nrecovered\n", strings.Repeat("z", maxDiagnosticBytes), "never written"} {
		if _, err := log.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "split-") || !strings.Contains(string(data), "TOKEN=[REDACTED]") || !strings.Contains(string(data), "recovered") || !strings.Contains(string(data), "output limit") || len(data) > 1024 {
		t.Fatalf("unsafe/unbounded diagnostics: size=%d err=%v", len(data), err)
	}
}
