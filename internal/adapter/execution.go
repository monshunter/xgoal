package adapter

import (
	"fmt"
	"reflect"

	"github.com/monshunter/xgoal/internal/config"
)

// ValidateExecution also protects direct adapter callers from recording an
// effective identity that differs from the provider arguments. Legacy fresh
// invocations may omit it; omitted identities never authorize session resume.
func ValidateExecution(e *config.ExecutionConfig, profileID, provider, role string) error {
	if e == nil {
		return nil
	}
	if e.ProfileID != profileID || e.Provider != provider || e.Role != role {
		return fmt.Errorf("effective profile identity does not match invocation")
	}
	a := config.Agent{ID: profileID, Adapter: provider, Roles: []string{role}, Model: e.Model, ReasoningEffort: e.ReasoningEffort, Sandbox: e.Sandbox, PermissionMode: e.PermissionMode, AllowedTools: e.AllowedTools}
	want, err := a.Effective(role, e.CLIVersion)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(*e, want) {
		return fmt.Errorf("effective profile settings do not match resolved role permissions")
	}
	return nil
}

// Probes use read-only permissions even when checking an implementer profile's
// model settings. The recorded role makes this narrower contract explicit.
func ProbeExecution(spec ProbeSpec, provider, version string) (config.ExecutionConfig, error) {
	return (config.Agent{ID: spec.ProfileID, Adapter: provider, Roles: []string{"reviewer"}, Model: spec.Model, ReasoningEffort: spec.ReasoningEffort}).Effective("reviewer", version)
}
