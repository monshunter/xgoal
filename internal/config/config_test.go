package config_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/config"
)

const validConfig = `apiVersion: xgoal.dev/v1alpha1
kind: Project
metadata:
  name: demo
project:
  baseBranch: main
  trustedRepository: true
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
runtime:
  provider: local-process
  isolationLevelRequired: L0
  projectNetwork: deny
  projectSecrets: deny
validators: []
`

func TestLoadValidConfig(t *testing.T) {
	cfg, err := config.Load(strings.NewReader(validConfig))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Metadata.Name != "demo" {
		t.Fatalf("Metadata.Name = %q, want demo", cfg.Metadata.Name)
	}
	if cfg.Orchestration.LeaseTTL.Duration != 90*time.Second {
		t.Fatalf("LeaseTTL = %s, want 90s", cfg.Orchestration.LeaseTTL.Duration)
	}
	if got := cfg.Agents[0].Roles[0]; got != "implementer" {
		t.Fatalf("role = %q, want implementer", got)
	}
}

func TestWorkspaceProviderMigrationDoesNotHideOtherConfigErrors(t *testing.T) {
	for _, provider := range []string{"current-directory", "git-worktree"} {
		t.Run(provider, func(t *testing.T) {
			input := strings.Replace(validConfig, "runtime:\n", "workspace: {provider: "+provider+"}\nruntime:\n", 1)
			cfg, err := config.Load(strings.NewReader(input))
			if provider == "current-directory" {
				if err != nil || cfg.Workspace.Provider != provider {
					t.Fatalf("current directory config: %+v, %v", cfg.Workspace, err)
				}
				return
			}
			if !errors.Is(err, config.ErrMigrationRequired) {
				t.Fatalf("legacy provider did not require migration: %v", err)
			}
			for _, invalid := range []string{input + "unknownField: true\n", strings.Replace(input, "projectNetwork: deny", "projectNetwork: invalid", 1), strings.Replace(input, "validators: []", "validators: [{id: missing-command}]", 1)} {
				_, err := config.Load(strings.NewReader(invalid))
				if err == nil || errors.Is(err, config.ErrMigrationRequired) {
					t.Fatalf("legacy provider hid another invalid configuration: %v", err)
				}
			}
		})
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := config.Load(strings.NewReader(validConfig + "unknownField: true\n"))
	if err == nil || !strings.Contains(err.Error(), "unknownField") {
		t.Fatalf("Load() error = %v, want unknown field error", err)
	}
}

func TestLoadRejectsRemovedBudgetConfiguration(t *testing.T) {
	_, err := config.Load(strings.NewReader(validConfig + "budget: {maxAttemptsPerGoal: 10}\n"))
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("Load() error = %v, want removed budget field error", err)
	}
}

func TestLoadRejectsDuplicateKey(t *testing.T) {
	input := strings.Replace(validConfig, "kind: Project", "kind: Project\nkind: Other", 1)
	_, err := config.Load(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "already defined") {
		t.Fatalf("Load() error = %v, want duplicate key error", err)
	}
}

func TestLoadRejectsUnsafeV01Configuration(t *testing.T) {
	tests := []struct {
		name    string
		old     string
		new     string
		wantErr string
	}{
		{name: "untrusted repository", old: "trustedRepository: true", new: "trustedRepository: false", wantErr: "trustedRepository"},
		{name: "parallelism", old: "maxParallel: 1", new: "maxParallel: 2", wantErr: "maxParallel"},
		{name: "heartbeat", old: "heartbeatInterval: 20s", new: "heartbeatInterval: 90s", wantErr: "leaseTTL"},
		{name: "provider transport", old: "providerTransport: allow", new: "providerTransport: deny", wantErr: "providerTransport"},
		{name: "workspace provider", old: "runtime:\n", new: "workspace: {provider: shared-directory}\nruntime:\n", wantErr: "workspace.provider"},
		{name: "scope policy", old: "runtime:\n", new: "scopePolicy: {deny: [../escape]}\nruntime:\n", wantErr: "scopePolicy.deny"},
		{name: "bootstrap argv", old: "runtime:\n", new: "bootstrap: {commands: [{id: setup, timeout: 1m}]}\nruntime:\n", wantErr: "bootstrap.commands[0].argv"},
		{name: "bootstrap network", old: "runtime:\n", new: "bootstrap: {commands: [{id: setup, argv: [go, env], timeout: 1m, network: unrestricted}]}\nruntime:\n", wantErr: "bootstrap.commands[0].network"},
		{name: "validator phases", old: "validators: []", new: "validators: [{id: test, type: command, argv: [go, test], timeout: 1m, required: true}]", wantErr: "validators[0].phases"},
		{name: "validator timeout", old: "validators: []", new: "validators: [{id: test, type: command, phases: [final], argv: [go, test], required: true}]", wantErr: "validators[0].timeout"},
		{name: "validator phase value", old: "validators: []", new: "validators: [{id: test, type: command, phases: [whenever], argv: [go, test], timeout: 1m, required: true}]", wantErr: "validators[0].phases"},
		{name: "policy", old: "runtime:\n", new: "policy: {gitPush: allow}\nruntime:\n", wantErr: "policy.gitPush"},
		{name: "report format", old: "runtime:\n", new: "report: {formats: [html]}\nruntime:\n", wantErr: "report.formats"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := strings.Replace(validConfig, tt.old, tt.new, 1)
			_, err := config.Load(strings.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
