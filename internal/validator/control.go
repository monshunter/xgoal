package validator

import (
	"context"
	"fmt"
	"slices"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/gitrepo"
)

// Control definitions reuse the trusted-file contract, but are not business
// Validators and cannot be selected as evidence by a Planner.
func bindControls(ctx context.Context, repo *gitrepo.Repository, base string, cfg config.Config) (map[string]Definition, error) {
	controls := map[string]Definition{}
	bind := func(id string, argv, files []string, cwd string, timeout config.Duration) error {
		definition, err := buildDefinition(ctx, repo, base, config.Validator{ID: id, Type: "command", Phases: []string{"change", "final"}, Argv: argv, TrustedFiles: files, CWD: cwd, Timeout: timeout})
		if err != nil {
			return fmt.Errorf("control %q: %w", id, err)
		}
		controls[id] = definition
		return nil
	}
	for _, command := range cfg.Bootstrap.Commands {
		if err := bind("bootstrap/"+command.ID, command.Argv, command.TrustedFiles, command.CWD, command.Timeout); err != nil {
			return nil, err
		}
	}
	for _, service := range cfg.Services {
		if err := bind("service/"+service.ID+"/readiness", service.Readiness.Argv, service.Readiness.TrustedFiles, service.CWD, service.Readiness.Timeout); err != nil {
			return nil, err
		}
		// The actual service argv points at candidate business code. Freeze only
		// explicitly declared control files, without inferring the business entry.
		if err := bind("service/"+service.ID+"/start", []string{"true"}, service.TrustedFiles, service.CWD, service.Readiness.Timeout); err != nil {
			return nil, err
		}
	}
	if cfg.Acceptance != nil {
		profile, _, err := cfg.SelectProfile("acceptance", "")
		if err != nil {
			return nil, err
		}
		files := append([]string(nil), cfg.Acceptance.TrustedFiles...)
		for _, rule := range profile.AllowedTools {
			if command, ok := config.AcceptanceCommand(rule); ok {
				files = append(files, command)
			}
		}
		slices.Sort(files)
		if err := bind("acceptance/client", []string{"true"}, slices.Compact(files), ".", profile.Timeout); err != nil {
			return nil, err
		}
	}
	return controls, nil
}

func (registry *Registry) ControlDefinition(id string) (Definition, bool) {
	definition, exists := registry.controls[id]
	return cloneDefinition(definition), exists
}

func (registry *Registry) VerifyControl(root, id string) error {
	definition, exists := registry.controls[id]
	if !exists {
		return fmt.Errorf("unknown trusted control %q", id)
	}
	if err := verifyTrustedExecutable(root, definition); err != nil {
		return fmt.Errorf("%w: %v", ErrTrustedFileChanged, err)
	}
	return verifyTrustedFiles(root, definition)
}
