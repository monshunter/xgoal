package config_test

import (
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/config"
)

const configuredScenarios = `scenarios:
  - id: business
    description: response contains the requested result
    steps: [Call the application, Assert the returned business value]
    services: [app]
    validators: [unit]
    artifactPaths: [response.json]
`

func TestScenarioConfigurationBindsDeclaredAssertionsAndPaths(t *testing.T) {
	text := strings.Replace(validConfig, "validators: []", "validators: [{id: unit, type: command, phases: [change, final], argv: [true], timeout: 1s, required: true}]", 1) + configuredServices + configuredScenarios
	if _, err := config.Load(strings.NewReader(text)); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]string{
		"unknown-service":     strings.Replace(text, "services: [app]", "services: [missing]", 1),
		"unknown-validator":   strings.Replace(text, "validators: [unit]", "validators: [missing]", 1),
		"no-assertion":        strings.Replace(text, "validators: [unit]", "validators: []", 1),
		"escaping-artifact":   strings.Replace(text, "response.json", "../response.json", 1),
		"glob-artifact":       strings.Replace(text, "response.json", "*.json", 1),
		"missing-description": strings.Replace(text, "description: response contains the requested result", "description: ''", 1),
		"unknown-acceptance":  text + "acceptance: {scenarioIDs: [missing]}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := config.Load(strings.NewReader(input)); err == nil {
				t.Fatal("invalid scenario accepted")
			}
		})
	}
}

func TestAcceptanceProviderMatrixRejectsUnsupportedLocalInteraction(t *testing.T) {
	base := strings.Replace(validConfig, "validators: []", "validators: [{id: unit, type: command, phases: [final], argv: [true], timeout: 1s, required: true}]", 1) + configuredServices + configuredScenarios
	cfg, err := config.Load(strings.NewReader(base))
	if err != nil {
		t.Fatal(err)
	}
	profile := cfg.Agents[0]
	profile.ID, profile.Roles, profile.Sandbox = "acceptance", []string{"acceptance"}, ""
	cfg.Agents = append(cfg.Agents, profile)
	cfg.Acceptance = &config.Acceptance{ScenarioIDs: []string{"business"}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "Codex acceptance") {
		t.Fatalf("network unsupported: %v", err)
	}
	cfg.Agents[1].Adapter, cfg.Agents[1].Command = "claude-cli", "claude"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "trusted-script") {
		t.Fatalf("missing scoped client: %v", err)
	}
	cfg.Agents[1].AllowedTools = []string{"Read", "Bash(./client.sh *)"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidationCapabilitiesDistinguishesUnspecifiedCoverageAndCopiesInputs(t *testing.T) {
	base := strings.Replace(validConfig, "validators: []", "validators: [{id: unit, type: command, phases: [final], argv: [true], timeout: 1s, required: true}]", 1) + configuredServices + configuredScenarios
	cfg, err := config.Load(strings.NewReader(base))
	if err != nil {
		t.Fatal(err)
	}
	capabilities := cfg.ValidationCapabilities()
	if capabilities.Validators[0].Coverage != "unspecified" || len(capabilities.Validators[0].ScenarioIDs) != 1 {
		t.Fatalf("capability: %+v", capabilities)
	}
	cfg.Scenarios[0].Steps[0] = "mutated"
	cfg.Validators[0].Phases[0] = "mutated"
	if capabilities.Scenarios[0].Steps[0] == "mutated" || capabilities.Validators[0].Phases[0] == "mutated" {
		t.Fatal("capability input changed through caller alias")
	}
}
