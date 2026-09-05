package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// ExecutionConfig records requested, resolved settings. Empty model/effort mean
// native inheritance, never an observation of the provider's actual selection.
type ExecutionConfig struct {
	ProfileID       string   `json:"profile_id"`
	Provider        string   `json:"provider"`
	Role            string   `json:"role"`
	Model           string   `json:"model,omitempty"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
	ModelSource     string   `json:"model_source"`
	EffortSource    string   `json:"effort_source"`
	CLIVersion      string   `json:"cli_version"`
	Sandbox         string   `json:"sandbox,omitempty"`
	PermissionMode  string   `json:"permission_mode,omitempty"`
	Tools           []string `json:"tools,omitempty"`
	AllowedTools    []string `json:"allowed_tools,omitempty"`
}

func (e ExecutionConfig) Resumable() bool {
	return e.ProfileID != "" && e.CLIVersion != "" && e.Model != "" && e.ReasoningEffort != "" && e.ModelSource == "explicit" && e.EffortSource == "explicit"
}

func (e *ExecutionConfig) Clone() *ExecutionConfig {
	if e == nil {
		return nil
	}
	cloned := *e
	cloned.Tools = slices.Clone(e.Tools)
	cloned.AllowedTools = slices.Clone(e.AllowedTools)
	return &cloned
}

func validRole(role string) bool {
	return oneOf(role, "planner", "implementer", "reviewer", "acceptance")
}

// Effective is shared by all roles; explicit incompatible settings are errors,
// rather than hints that one of the invocation paths can silently ignore.
func (a Agent) Effective(role, cliVersion string) (ExecutionConfig, error) {
	e := ExecutionConfig{ProfileID: a.ID, Provider: a.Adapter, Role: role, Model: a.Model, ReasoningEffort: a.ReasoningEffort, CLIVersion: cliVersion, ModelSource: "native-inheritance", EffortSource: "native-inheritance"}
	if !validRole(role) || !slices.Contains(a.Roles, role) {
		return e, fmt.Errorf("profile %q does not support role %q", a.ID, role)
	}
	if a.Model != "" {
		if strings.ContainsFunc(a.Model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || strings.HasPrefix(a.Model, "-") {
			return e, fmt.Errorf("profile %q model must be a nonblank model identifier without whitespace or control characters", a.ID)
		}
		e.ModelSource = "explicit"
	}
	if a.ReasoningEffort != "" {
		e.EffortSource = "explicit"
	}
	switch a.Adapter {
	case "codex-cli":
		if a.ReasoningEffort != "" && !oneOf(a.ReasoningEffort, "minimal", "low", "medium", "high", "xhigh") {
			return e, fmt.Errorf("profile %q reasoningEffort is unsupported by the Codex contract; use minimal, low, medium, high or xhigh and verify model support with an active probe", a.ID)
		}
		e.Sandbox = "read-only"
		if role == "implementer" {
			e.Sandbox = "workspace-write"
		}
		if a.Sandbox != "" && a.Sandbox != e.Sandbox {
			return e, fmt.Errorf("profile %q sandbox %q conflicts with role %q; omit sandbox for role defaults or split and bind separate profiles", a.ID, a.Sandbox, role)
		}
		e.PermissionMode = "never"
		if a.PermissionMode != "" && a.PermissionMode != e.PermissionMode {
			return e, fmt.Errorf("profile %q permissionMode must be never for unattended Codex execution", a.ID)
		}
		if len(a.AllowedTools) != 0 {
			return e, fmt.Errorf("profile %q allowedTools cannot be enforced by the Codex adapter; remove the field or select a compatible provider", a.ID)
		}
	case "claude-cli":
		if a.ReasoningEffort != "" && !oneOf(a.ReasoningEffort, "low", "medium", "high", "xhigh", "max") {
			return e, fmt.Errorf("profile %q reasoningEffort is unsupported by the Claude contract; use low, medium, high, xhigh or max and verify model support with an active probe", a.ID)
		}
		if a.Sandbox != "" {
			return e, fmt.Errorf("profile %q sandbox is a Codex field; Claude uses dontAsk and explicit tools under L0 isolation", a.ID)
		}
		e.PermissionMode = "dontAsk"
		if a.PermissionMode != "" && a.PermissionMode != e.PermissionMode {
			return e, fmt.Errorf("profile %q permissionMode must be dontAsk for unattended Claude execution", a.ID)
		}
		e.AllowedTools = []string{"Read", "Glob", "Grep"}
		if role == "implementer" {
			e.AllowedTools = append(e.AllowedTools, "Edit", "Write")
		}
		if len(a.AllowedTools) > 0 {
			e.AllowedTools = slices.Clone(a.AllowedTools)
		}
		for _, rule := range e.AllowedTools {
			name := rule
			allowed := oneOf(rule, "Read", "Glob", "Grep") || (role == "implementer" && oneOf(rule, "Edit", "Write"))
			if _, scoped := AcceptanceCommand(rule); role == "acceptance" && scoped {
				name, allowed = "Bash", true
			}
			if !allowed {
				return e, fmt.Errorf("profile %q allowedTools rule %q exceeds role %q; readonly roles allow Read, Glob and Grep, acceptance additionally permits Bash(./trusted-script) or Bash(./trusted-script *)", a.ID, rule, role)
			}
			if !slices.Contains(e.Tools, name) {
				e.Tools = append(e.Tools, name)
			}
		}
		sort.Strings(e.Tools)
		sort.Strings(e.AllowedTools)
	case "fake":
	default:
		return e, fmt.Errorf("unsupported profile provider %q", a.Adapter)
	}
	return e, nil
}

// AcceptanceCommand recognizes only a literal repository script entrypoint,
// optionally followed by argument wildcard. Arbitrary shell patterns are not
// proof of a bounded command, and cannot be mapped to trustedFiles.
func AcceptanceCommand(rule string) (string, bool) {
	if !strings.HasPrefix(rule, "Bash(./") || !strings.HasSuffix(rule, ")") {
		return "", false
	}
	command := strings.TrimSuffix(strings.TrimPrefix(rule, "Bash(./"), ")")
	command = strings.TrimSuffix(command, " *")
	if command == "" || command == "." || !validRelativePath(command) || strings.ContainsAny(command, "*?[]{}()|;&<>`$\"'\\,:") || strings.ContainsFunc(command, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", false
	}
	return command, true
}

// SelectProfile preserves default selection while allowing deterministic role bindings.
func (c Config) SelectProfile(role, implementerID string) (Agent, string, error) {
	if !validRole(role) {
		return Agent{}, "", fmt.Errorf("unsupported role %q", role)
	}
	if id, bound := c.Orchestration.RoleProfiles[role]; bound {
		for _, a := range c.Agents {
			if a.ID == id && slices.Contains(a.Roles, role) {
				return a, "explicit-role-binding", nil
			}
		}
		return Agent{}, "", fmt.Errorf("orchestration.roleProfiles.%s references missing or incompatible profile %q", role, id)
	}
	var candidates []Agent
	provider := ""
	for _, a := range c.Agents {
		if a.ID == implementerID {
			provider = a.Adapter
		}
		if slices.Contains(a.Roles, role) {
			candidates = append(candidates, a)
		}
	}
	if len(candidates) == 0 {
		return Agent{}, "", fmt.Errorf("no trusted Agent Profile supports role %q", role)
	}
	if role == "reviewer" {
		sort.SliceStable(candidates, func(i, j int) bool {
			left, right := candidates[i], candidates[j]
			if c.Review.PreferDifferentProvider && (left.Adapter != provider) != (right.Adapter != provider) {
				return left.Adapter != provider
			}
			if (left.ID != implementerID) != (right.ID != implementerID) {
				return left.ID != implementerID
			}
			return left.ID < right.ID
		})
	}
	return candidates[0], "default-role-selection", nil
}

func (c Config) validateRoleProfiles() error {
	roles := make([]string, 0, len(c.Orchestration.RoleProfiles))
	for role := range c.Orchestration.RoleProfiles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		if _, _, err := c.SelectProfile(role, ""); err != nil {
			return err
		}
	}
	return nil
}

type ProfileSelection struct {
	ProfileID string `json:"profile_id"`
	Source    string `json:"source"`
}

func (c Config) RoleSelections() map[string]ProfileSelection {
	result := map[string]ProfileSelection{}
	implementer, _, _ := c.SelectProfile("implementer", "")
	for _, role := range []string{"planner", "implementer", "reviewer", "acceptance"} {
		if profile, source, err := c.SelectProfile(role, implementer.ID); err == nil {
			result[role] = ProfileSelection{ProfileID: profile.ID, Source: source}
		}
	}
	return result
}

func (a Agent) EffectiveRoles(version string) map[string]ExecutionConfig {
	result := map[string]ExecutionConfig{}
	for _, role := range a.Roles {
		if effective, err := a.Effective(role, version); err == nil {
			result[role] = effective
		}
	}
	return result
}
