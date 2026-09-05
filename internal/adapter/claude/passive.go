package claude

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"

	"github.com/monshunter/xgoal/internal/adapter"
)

// PassiveProbe checks CLI capabilities without creating runtime artifacts.
func PassiveProbe(ctx context.Context, configuration Config, spec adapter.ProbeSpec) (adapter.Capabilities, error) {
	if spec.Mode != adapter.ProbePassive || !validComponent(spec.ProfileID) {
		return adapter.Capabilities{}, errors.New("passive probe requires a valid profile and passive mode")
	}
	binary, err := exec.LookPath(configuration.Binary)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	root, err := canonicalDirectory(configuration.ProjectRoot)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	environment, err := buildEnvironment(configuration.Environment)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	runtime := &Adapter{binary: binary, projectRoot: root, environment: environment}
	return runtime.passiveProbe(ctx, spec)
}
