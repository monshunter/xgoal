package codex

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
)

func TestPassiveProbeDoesNotCreateRuntime(t *testing.T) {
	fixture := newFixture(t, "normal")
	capabilities, err := PassiveProbe(context.Background(), Config{Binary: fixture.binaryPath, ProjectRoot: fixture.projectRoot, RuntimeRoot: fixture.runtimeRoot}, adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: "passive"})
	if err != nil || !capabilities.StructuredOutput {
		t.Fatalf("capabilities=%+v err=%v", capabilities, err)
	}
	if _, err := os.Stat(fixture.runtimeRoot); !os.IsNotExist(err) {
		t.Fatalf("passive probe created runtime: %v", err)
	}
}

func TestPassiveProbeLoginTimeoutPreservesDeadline(t *testing.T) {
	f := newFixture(t, "normal")
	data, err := os.ReadFile(f.binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	marker := f.projectRoot + "/login-entered"
	script := strings.Replace(string(data), "printf '%s\\n' 'Logged in using fixture'", "touch '"+marker+"'; exec sleep 30", 1)
	if err := os.WriteFile(f.binaryPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	// Each supervised probe crosses the executable barrier; race builds add
	// shutdown overhead before login. Keep enough time to reach that branch.
	capabilities, err := PassiveProbe(context.Background(), Config{Binary: f.binaryPath, ProjectRoot: f.projectRoot, Environment: f.environment}, adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: "passive", Timeout: 10 * time.Second})
	if _, markerErr := os.Stat(marker); markerErr != nil {
		t.Fatalf("login probe did not begin: %v; probe: %v", markerErr, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) || capabilities.CredentialStatus == "missing" {
		t.Fatalf("timeout became missing credentials: %+v %v", capabilities, err)
	}
}
