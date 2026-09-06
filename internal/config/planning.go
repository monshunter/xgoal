package config

import (
	"fmt"
	"github.com/monshunter/xgoal/internal/validationplan"
)

type Planning struct {
	AcceptanceFiles     []string `yaml:"acceptanceFiles,omitempty" json:"acceptanceFiles,omitempty"`
	GeneratedValidators string   `yaml:"generatedValidators,omitempty" json:"generatedValidators,omitempty"`
}

func (c Config) GeneratedValidationPolicy() string {
	if c.Planning == nil || c.Planning.GeneratedValidators == "" {
		return "allow"
	}
	return c.Planning.GeneratedValidators
}

func (c Config) validatePlanning() error {
	if c.Planning == nil {
		return nil
	}
	if !oneOf(c.GeneratedValidationPolicy(), "allow", "human-gate", "deny") {
		return fmt.Errorf("planning.generatedValidators must be allow, human-gate, or deny")
	}
	_, err := validationplan.Paths(c.Planning.AcceptanceFiles)
	return err
}
