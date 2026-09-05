package config_test

import (
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/config"
)

func TestProfileRejectsIgnoredOrIncompatibleExecutionSettings(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{"unimplemented credential provider", "credentialSource: cli-session", "credentialSource: secret-provider"},
		{"mixed explicit sandbox", "roles: [implementer]", "roles: [planner, implementer]"},
		{"interactive approval", "sandbox: workspace-write", "sandbox: workspace-write\n    permissionMode: plan"},
		{"codex tool policy", "sandbox: workspace-write", "sandbox: workspace-write\n    allowedTools: [Read]"},
		{"readonly role write policy", "roles: [implementer]", "roles: [reviewer]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(strings.NewReader(strings.Replace(validConfig, tc.old, tc.replacement, 1)))
			if err == nil {
				t.Fatal("incompatible execution setting was silently accepted")
			}
		})
	}
}

func TestProfileBindingAndEffectiveRoleDefaults(t *testing.T) {
	input := strings.Replace(validConfig, "roles: [implementer]", "roles: [planner, implementer]", 1)
	input = strings.Replace(input, "    sandbox: workspace-write\n", "    model: gpt-example\n    reasoningEffort: high\n", 1)
	input = strings.Replace(input, "  defaultMode: standard", "  defaultMode: standard\n  roleProfiles: {planner: codex-implementer}", 1)
	cfg, err := config.Load(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	p, source, err := cfg.SelectProfile("planner", "")
	if err != nil || source != "explicit-role-binding" {
		t.Fatalf("selection %v %q %v", p, source, err)
	}
	for role, sandbox := range map[string]string{"planner": "read-only", "implementer": "workspace-write"} {
		e, err := p.Effective(role, "codex-cli 0.153.4")
		if err != nil || e.Sandbox != sandbox || e.Model != "gpt-example" || e.ReasoningEffort != "high" || !e.Resumable() {
			t.Fatalf("effective %s: %+v, %v", role, e, err)
		}
	}
	p.Model = ""
	e, err := p.Effective("planner", "codex-cli 0.153.4")
	if err != nil || e.Resumable() || e.ModelSource != "native-inheritance" {
		t.Fatalf("inherited identity %+v %v", e, err)
	}
	for _, binding := range []string{"planner: missing", "reviewer: codex-implementer", "manager: codex-implementer"} {
		_, err := config.Load(strings.NewReader(strings.Replace(input, "planner: codex-implementer", binding, 1)))
		if err == nil {
			t.Fatalf("bad binding accepted: %s", binding)
		}
	}
}

func TestClaudeProfileSeparatesToolNamesFromPermissionRules(t *testing.T) {
	p := config.Agent{ID: "accept", Adapter: "claude-cli", Roles: []string{"acceptance"}, Model: "claude-example", ReasoningEffort: "high", AllowedTools: []string{"Read", "Bash(./scripts/client *)"}}
	e, err := p.Effective("acceptance", "2.1.235")
	if err != nil || strings.Join(e.Tools, ",") != "Bash,Read" || strings.Join(e.AllowedTools, ",") != "Bash(./scripts/client *),Read" {
		t.Fatalf("effective %+v %v", e, err)
	}
	p.Roles = []string{"reviewer"}
	if _, err := p.Effective("reviewer", "2.1.235"); err == nil {
		t.Fatal("reviewer received Bash")
	}
	p.Roles = []string{"acceptance"}
	for _, rule := range []string{"Bash", "Bash(*)", "Bash( *)", "Bash(:*)", "Bash(./*)", "Bash(./script{1..3})", "Bash(./../outside *)", "Bash(./client;evil *)", "Bash(./client $(evil))"} {
		p.AllowedTools = []string{rule}
		if _, err := p.Effective("acceptance", "2.1.235"); err == nil {
			t.Fatalf("unbounded command accepted: %s", rule)
		}
	}
}
