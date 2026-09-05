package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/validator"
	"github.com/monshunter/xgoal/internal/workspace"
)

func (engine *Engine) prepareEnvironment(ctx context.Context, snapshot workspace.Snapshot, revision domain.GoalRevision, registry *validator.Registry, validatorIDs, serviceIDs []string) (environment.Handle, protocol.EnvironmentSnapshot, error) {
	if registry == nil || registry.ConfigHash() != engine.configHash {
		return environment.Handle{}, protocol.EnvironmentSnapshot{}, errors.New("environment requires the frozen configuration registry")
	}
	requested := append([]string(nil), serviceIDs...)
	var names []string
	for _, id := range validatorIDs {
		definition, exists := registry.Definition(id)
		if !exists {
			return environment.Handle{}, protocol.EnvironmentSnapshot{}, fmt.Errorf("unknown environment validator %q", id)
		}
		requested = append(requested, definition.Services...)
		names = append(names, definition.EnvironmentAllowlist...)
	}
	services, err := engine.config.ServiceOrder(requested)
	if err != nil {
		return environment.Handle{}, protocol.EnvironmentSnapshot{}, err
	}
	for _, command := range engine.config.Bootstrap.Commands {
		names = append(names, command.Environment.Allow...)
	}
	for _, service := range services {
		names = append(names, service.Environment.Allow...)
	}
	bootstrapHash := ""
	if len(engine.config.Bootstrap.Commands) > 0 {
		bootstrapHash, err = canonical.Hash("bootstrap", config.APIVersion, engine.config.Bootstrap.Commands)
		if err != nil {
			return environment.Handle{}, protocol.EnvironmentSnapshot{}, err
		}
	}
	handle, err := engine.environment.Prepare(ctx, environment.Spec{ID: "environment_" + snapshot.ID, WorktreePath: snapshot.Path, BaseCommit: snapshot.Identity.HeadCommit, BaseTree: snapshot.InputTree, Identity: snapshot.Identity, ExcludePaths: snapshot.ExcludePaths, ConfigHash: engine.configHash, GoalRevisionHash: revision.Hash, EnvironmentAllowlist: uniqueSorted(names), BootstrapHash: bootstrapHash})
	if err != nil {
		return environment.Handle{}, protocol.EnvironmentSnapshot{}, err
	}
	fail := func(cause error) (environment.Handle, protocol.EnvironmentSnapshot, error) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		cleanupErr := engine.environment.Cleanup(cleanupCtx, handle)
		return environment.Handle{}, protocol.EnvironmentSnapshot{}, fmt.Errorf("environment preparation failed; diagnostics: %s/logs: %w", handle.Root, errors.Join(cause, cleanupErr))
	}
	checkCommand := func(commandCtx context.Context, key string, argv []string, cwd string, allow []string, logID string) error {
		if err := registry.VerifyControl(handle.Worktree, key); err != nil {
			return err
		}
		run, runErr := engine.environment.RunCommand(commandCtx, handle, environment.CommandSpec{Argv: argv, CWD: cwd, EnvironmentAllowlist: allow, LogID: logID, GracePeriod: time.Second})
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(commandCtx), 30*time.Second)
		defer cancel()
		trustErr := registry.VerifyControl(handle.Worktree, key)
		treeErr := engine.environment.VerifyTree(checkCtx, handle, snapshot.InputTree)
		if runErr == nil && run.ExitCode != 0 {
			runErr = fmt.Errorf("command exited %d", run.ExitCode)
		}
		if runErr != nil || trustErr != nil || treeErr != nil {
			return fmt.Errorf("%s exited %d: %w", key, run.ExitCode, errors.Join(runErr, trustErr, treeErr))
		}
		return nil
	}
	for _, command := range engine.config.Bootstrap.Commands {
		if err := engine.projectCommandNetwork(command.Network); err != nil {
			return fail(err)
		}
		runCtx, cancel := context.WithTimeout(ctx, command.Timeout.Duration)
		err := checkCommand(runCtx, "bootstrap/"+command.ID, command.Argv, command.CWD, command.Environment.Allow, "bootstrap_"+command.ID)
		cancel()
		if err != nil {
			return fail(err)
		}
	}
	var specs []environment.ServiceSpec
	for _, service := range services {
		if err := engine.projectCommandNetwork(service.Network); err != nil {
			return fail(err)
		}
		if err := registry.VerifyControl(handle.Worktree, "service/"+service.ID+"/start"); err != nil {
			return fail(err)
		}
		specs = append(specs, environment.ServiceSpec{ID: service.ID, Argv: service.Argv, CWD: service.CWD, EnvironmentAllowlist: service.Environment.Allow, GracePeriod: service.StopGracePeriod.Duration, ProbeTimeout: service.Readiness.Timeout.Duration, ProbeInterval: service.Readiness.Interval.Duration, Probe: func(probeCtx context.Context) error {
			return checkCommand(probeCtx, "service/"+service.ID+"/readiness", service.Readiness.Argv, service.CWD, service.Environment.Allow, "readiness_"+service.ID)
		}})
	}
	if err := engine.environment.StartServices(ctx, handle, specs); err != nil {
		return fail(err)
	}
	for _, service := range services {
		if err := registry.VerifyControl(handle.Worktree, "service/"+service.ID+"/start"); err != nil {
			return fail(err)
		}
	}
	observed, err := engine.environment.Snapshot(ctx, handle)
	if err != nil {
		return fail(err)
	}
	return handle, observed, nil
}

func (engine *Engine) projectCommandNetwork(policy string) error {
	if policy == "require-gate" || (policy == "allow" && engine.config.Runtime.ProjectNetwork != "allow") {
		return errProjectNetworkGate
	}
	return nil
}
