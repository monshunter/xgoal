package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/cli"
)

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "xgoal dev" {
		t.Fatalf("stdout = %q, want %q", got, "xgoal dev")
	}
}

func TestConfigValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xgoal.yaml")
	if err := os.WriteFile(path, []byte(validConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"config", "validate", "--file", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "valid xgoal.dev/v1alpha1 Project") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestUnknownCommandReturnsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"completed-but-not-implemented"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

const validConfig = `apiVersion: xgoal.dev/v1alpha1
kind: Project
metadata: {name: demo}
project: {baseBranch: main, trustedRepository: true}
orchestration:
  defaultMode: standard
  maxParallel: 1
  leaseTTL: 90s
  heartbeatInterval: 20s
agents:
  - id: codex-implementer
    adapter: codex-cli
    command: codex
    roles: [implementer]
    timeout: 45m
    sandbox: workspace-write
    providerTransport: allow
    credentialSource: cli-session
    activeProbe: explicit
runtime: {provider: local-process, isolationLevelRequired: L0, projectNetwork: deny, projectSecrets: deny}
validators: []
`
