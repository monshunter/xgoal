package validator

import (
	"fmt"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/validationplan"
	"sort"
	"time"
)

// WithAcceptance overlays only this frozen Goal's checks. Existing definitions keep their identity.
func (registry *Registry) WithAcceptance(generated []validationplan.Generated, inputs []validationplan.Input) (*Registry, error) {
	if err := validationplan.ValidateGenerated(generated); err != nil {
		return nil, err
	}
	if err := validationplan.ValidateInputs(inputs); err != nil {
		return nil, err
	}
	copy := *registry
	copy.definitions = map[string]Definition{}
	for id, d := range registry.definitions {
		copy.definitions[id] = cloneDefinition(d)
	}
	copy.acceptanceInputs = nil
	for _, i := range inputs {
		copy.acceptanceInputs = append(copy.acceptanceInputs, TrustedFile{Path: i.Path, Mode: i.Mode, SHA256: i.SHA256})
	}
	for _, g := range generated {
		if _, exists := copy.definitions[g.ID]; exists {
			return nil, fmt.Errorf("generated validator would overwrite %s", g.ID)
		}
		d := Definition{ID: g.ID, Type: "command", Phases: []string{"change", "final"}, Argv: g.Argv(), CWD: ".", Timeout: time.Duration(g.TimeoutSeconds) * time.Second, ExpectedExitCodes: []int{0}, EnvironmentAllowlist: []string{"PATH", "TMPDIR"}, Required: true, TrustedFiles: append([]TrustedFile(nil), copy.acceptanceInputs...)}
		hash, err := canonical.Hash("validator-definition", definitionVersion, d.identity())
		if err != nil {
			return nil, err
		}
		d.Hash = hash
		if err := d.Validate(); err != nil {
			return nil, err
		}
		copy.definitions[g.ID] = d
	}
	return &copy, nil
}

func (registry *Registry) VerifyAcceptanceInputs(root string) error {
	return verifyTrustedFiles(root, Definition{TrustedFiles: registry.acceptanceInputs})
}

func (registry *Registry) acceptancePaths() []string {
	var result []string
	for _, i := range registry.acceptanceInputs {
		result = append(result, i.Path)
	}
	sort.Strings(result)
	return result
}
