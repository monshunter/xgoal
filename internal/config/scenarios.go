package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Scenario declares a workflow and its deterministic assertions. Artifacts are
// exact files relative to the private XGOAL_SCENARIO_DIR, never source paths.
type Scenario struct {
	ID            string   `yaml:"id" json:"id"`
	Description   string   `yaml:"description" json:"description"`
	Steps         []string `yaml:"steps" json:"steps"`
	Services      []string `yaml:"services,omitempty" json:"services,omitempty"`
	Validators    []string `yaml:"validators" json:"validators"`
	ArtifactPaths []string `yaml:"artifactPaths,omitempty" json:"artifactPaths,omitempty"`
}

type Acceptance struct {
	TrustedFiles []string `yaml:"trustedFiles,omitempty" json:"trustedFiles,omitempty"`
	ScenarioIDs  []string `yaml:"scenarioIDs" json:"scenarioIDs"`
	ReplaySafe   bool     `yaml:"replaySafe,omitempty" json:"replaySafe,omitempty"`
}

// ValidationCapabilities describes configured coverage, not observed success.
// No argv, inherited environment or credentials are needed by the Planner.
type ValidationCapabilities struct {
	Validators          []ValidatorCapability `json:"validators"`
	Scenarios           []Scenario            `json:"scenarios,omitempty"`
	RequiredScenarioIDs []string              `json:"required_scenario_ids,omitempty"`
}

type ValidatorCapability struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Coverage    string   `json:"coverage"`
	Type        string   `json:"type"`
	Phases      []string `json:"phases"`
	Services    []string `json:"services,omitempty"`
	ScenarioIDs []string `json:"scenario_ids,omitempty"`
}

func (c Config) validateScenarios() error {
	if len(c.Scenarios) > 64 {
		return fmt.Errorf("scenarios exceeds 64 entries")
	}
	validators := make(map[string]Validator, len(c.Validators))
	for _, v := range c.Validators {
		validators[v.ID] = v
		if strings.ContainsRune(v.Description, '\x00') || len(v.Description) > 8000 {
			return fmt.Errorf("validator %q description is invalid or oversized", v.ID)
		}
		if err := validateNames("validator "+v.ID+" scenarioIDs", v.ScenarioIDs, validComponentID); err != nil {
			return err
		}
	}
	scenarios := make(map[string]Scenario, len(c.Scenarios))
	for _, s := range c.Scenarios {
		if !validComponentID(s.ID) || strings.TrimSpace(s.Description) == "" || len(s.Description) > 8000 || strings.ContainsRune(s.Description, '\x00') || len(s.Steps) == 0 || len(s.Steps) > 64 || len(s.ArtifactPaths) > 64 || len(s.Validators) == 0 {
			return fmt.Errorf("scenario %q requires a safe ID, description, steps and deterministic validators", s.ID)
		}
		if _, duplicate := scenarios[s.ID]; duplicate {
			return fmt.Errorf("duplicate scenario %q", s.ID)
		}
		scenarios[s.ID] = s
		for _, step := range s.Steps {
			if strings.TrimSpace(step) == "" || strings.ContainsRune(step, '\x00') || len(step) > 8000 {
				return fmt.Errorf("scenario %q has an invalid step", s.ID)
			}
		}
		for name, values := range map[string][]string{"services": s.Services, "validators": s.Validators} {
			if err := validateNames("scenario "+s.ID+" "+name, values, validComponentID); err != nil {
				return err
			}
		}
		if _, err := c.ServiceOrder(s.Services); err != nil {
			return fmt.Errorf("scenario %q: %w", s.ID, err)
		}
		if err := validateNames("scenario "+s.ID+" artifactPaths", s.ArtifactPaths, validTrustedPath); err != nil {
			return err
		}
		for _, id := range s.Validators {
			v, exists := validators[id]
			if !exists || !slices.Contains(v.Phases, "final") {
				return fmt.Errorf("scenario %q validator %q must exist and support final validation", s.ID, id)
			}
		}
	}
	// scenarios[].validators owns the relationship. A Validator may annotate
	// it, but cannot create a contradictory second coverage declaration.
	for _, v := range c.Validators {
		for _, id := range v.ScenarioIDs {
			if s, exists := scenarios[id]; !exists || !slices.Contains(s.Validators, v.ID) {
				return fmt.Errorf("validator %q scenario %q lacks the reciprocal scenario validator reference", v.ID, id)
			}
		}
	}
	if c.Acceptance == nil {
		return nil
	}
	if err := validateNames("acceptance.trustedFiles", c.Acceptance.TrustedFiles, validTrustedPath); err != nil {
		return err
	}
	if len(c.Acceptance.ScenarioIDs) == 0 {
		return fmt.Errorf("acceptance requires at least one scenarioID; omit acceptance to disable it")
	}
	if err := validateNames("acceptance.scenarioIDs", c.Acceptance.ScenarioIDs, validComponentID); err != nil {
		return err
	}
	var services []string
	for _, id := range c.Acceptance.ScenarioIDs {
		s, exists := scenarios[id]
		if !exists {
			return fmt.Errorf("acceptance references unknown scenario %q", id)
		}
		services = append(services, s.Services...)
		for _, v := range s.Validators {
			services = append(services, validators[v].Services...)
		}
	}
	profile, _, err := c.SelectProfile("acceptance", "")
	if err != nil {
		return fmt.Errorf("acceptance requires a compatible profile: %w", err)
	}
	if profile.Adapter == "codex-cli" && len(services) != 0 {
		return fmt.Errorf("acceptance with local services requires a Claude profile with scoped trusted client commands; Codex acceptance supports read-only observation without network")
	}
	if profile.Adapter == "claude-cli" && len(services) != 0 {
		for _, rule := range profile.AllowedTools {
			if _, scoped := AcceptanceCommand(rule); scoped {
				return nil
			}
		}
		return fmt.Errorf("acceptance with local services requires an allowedTools Bash(./trusted-script) client entry")
	}
	return nil
}

func (c Config) ValidationCapabilities() *ValidationCapabilities {
	result := &ValidationCapabilities{}
	for _, s := range c.Scenarios {
		s.Steps, s.Services = slices.Clone(s.Steps), slices.Clone(s.Services)
		s.Validators, s.ArtifactPaths = slices.Clone(s.Validators), slices.Clone(s.ArtifactPaths)
		result.Scenarios = append(result.Scenarios, s)
	}
	for _, v := range c.Validators {
		capability := ValidatorCapability{ID: v.ID, Description: v.Description, Coverage: "configured", Type: v.Type, Phases: slices.Clone(v.Phases), Services: slices.Clone(v.Services)}
		if strings.TrimSpace(v.Description) == "" {
			capability.Coverage = "unspecified"
			capability.Description = "Coverage is not described; inspect the trusted assertion before mapping a business criterion."
		}
		for _, s := range c.Scenarios {
			if slices.Contains(s.Validators, v.ID) {
				capability.ScenarioIDs = append(capability.ScenarioIDs, s.ID)
			}
		}
		sort.Strings(capability.ScenarioIDs)
		result.Validators = append(result.Validators, capability)
	}
	sort.Slice(result.Validators, func(i, j int) bool { return result.Validators[i].ID < result.Validators[j].ID })
	sort.Slice(result.Scenarios, func(i, j int) bool { return result.Scenarios[i].ID < result.Scenarios[j].ID })
	if c.Acceptance != nil {
		result.RequiredScenarioIDs = slices.Clone(c.Acceptance.ScenarioIDs)
		sort.Strings(result.RequiredScenarioIDs)
	}
	return result
}
