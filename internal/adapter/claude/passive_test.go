package claude

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
	capabilities, err := PassiveProbe(context.Background(), Config{Binary: fixture.binary, ProjectRoot: fixture.projectRoot, RuntimeRoot: fixture.runtimeRoot}, adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: "passive"})
	if err != nil || !capabilities.StructuredOutput {
		t.Fatalf("capabilities=%+v err=%v", capabilities, err)
	}
	if _, err := os.Stat(fixture.runtimeRoot); !os.IsNotExist(err) {
		t.Fatalf("passive probe created runtime: %v", err)
	}
}

func TestPassiveProbeAuthTimeoutPreservesDeadline(t *testing.T) {
	f := newFixture(t, "normal")
	data, err := os.ReadFile(f.binary)
	if err != nil {
		t.Fatal(err)
	}
	marker := f.projectRoot + "/auth-entered"
	script := strings.Replace(string(data), "echo '{\"loggedIn\": true, \"authMethod\": \"oauth_token\"}'", "touch '"+marker+"'; exec sleep 30", 1)
	if err := os.WriteFile(f.binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	// Reach auth after the supervised version/help barriers, including race
	// runtime shutdown overhead, before enforcing the blocked auth deadline.
	capabilities, err := PassiveProbe(context.Background(), Config{Binary: f.binary, ProjectRoot: f.projectRoot}, adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: "passive", Timeout: 10 * time.Second})
	if _, markerErr := os.Stat(marker); markerErr != nil {
		t.Fatalf("auth probe did not begin: %v; probe: %v", markerErr, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) || capabilities.CredentialStatus == "missing" {
		t.Fatalf("timeout became missing credentials: %+v %v", capabilities, err)
	}
}
