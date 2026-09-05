package codex

import (
	"context"
	"os"
	"testing"

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
