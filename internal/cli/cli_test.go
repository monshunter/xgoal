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
	if got := strings.TrimSpace(stdout.String()); got != "xgoal v0.1.0" {
		t.Fatalf("stdout = %q, want %q", got, "xgoal v0.1.0")
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

func TestRootHelpExposesCobraCommandTree(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{"Usage:", "Available Commands:", "completion", "daemon", "goal", "work"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("root help missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestNestedCommandHelpIsSideEffectFree(t *testing.T) {
	for _, args := range [][]string{
		{"goal", "replan", "--help"},
		{"work", "retry", "--help"},
		{"benchmark", "run", "--help"},
	} {
		var stdout, stderr bytes.Buffer
		code := cli.Run(args, &stdout, &stderr)
		if code != 0 {
			t.Errorf("Run(%v) code = %d, stderr = %q", args, code, stderr.String())
			continue
		}
		if !strings.Contains(stdout.String(), "Usage:") {
			t.Errorf("Run(%v) help = %q", args, stdout.String())
		}
	}
}

func TestUnknownFlagReturnsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"version", "--unknown"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown flag") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestVersionFlagsPreserveOutput(t *testing.T) {
	for _, flag := range []string{"--version", "-v"} {
		var stdout, stderr bytes.Buffer
		code := cli.Run([]string{flag}, &stdout, &stderr)
		if code != 0 || strings.TrimSpace(stdout.String()) != "xgoal v0.1.0" {
			t.Errorf("Run(%q) = code %d, stdout %q, stderr %q", flag, code, stdout.String(), stderr.String())
		}
	}
}

func TestCompletionSupportsDocumentedShells(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		var stdout, stderr bytes.Buffer
		code := cli.Run([]string{"completion", shell}, &stdout, &stderr)
		if code != 0 {
			t.Errorf("completion %s code = %d, stderr = %q", shell, code, stderr.String())
			continue
		}
		if stdout.Len() == 0 {
			t.Errorf("completion %s produced no output", shell)
		}
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
