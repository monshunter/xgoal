// Package doctor collects local facts without opening the state database or constructing execution adapters.
package doctor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/app"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/harness"
	"github.com/monshunter/xgoal/internal/project"
	"github.com/monshunter/xgoal/internal/testdiscovery"
)

func Inspect(ctx context.Context, paths app.Paths) map[string]any {
	unmet := []string{}
	tools := map[string]any{}
	for _, name := range []string{"git", "codex", "claude"} {
		tools[name] = commandVersion(ctx, name)
	}
	gitFacts := map[string]any{"repository": true, "top_level": paths.ProjectRoot, "common_dir": paths.CommonDir}
	for key, args := range map[string][]string{"head": {"rev-parse", "HEAD"}, "tree": {"rev-parse", "HEAD^{tree}"}, "status_porcelain": {"status", "--porcelain=v1"}} {
		output, err := passiveCommand(ctx, "git", append([]string{"-C", paths.ProjectRoot}, args...)...)
		gitFacts[key] = strings.TrimSpace(output)
		if err != nil {
			gitFacts[key+"_error"] = err.Error()
		}
	}
	configHash := ""
	network, secrets := "deny", "deny"
	profiles := []any{}
	validators := []any{}
	configuration, configErr := config.LoadFile(filepath.Join(paths.ProjectRoot, "xgoal.yaml"))
	if configErr != nil {
		unmet = append(unmet, "valid xgoal.yaml is unavailable: "+configErr.Error())
	} else {
		configHash, _ = configuration.Hash()
		network, secrets = configuration.Runtime.ProjectNetwork, configuration.Runtime.ProjectSecrets
		for _, profile := range configuration.Agents {
			capability := commandVersion(ctx, profile.Command)
			capability["id"] = profile.ID
			capability["adapter"] = profile.Adapter
			capability["roles"] = profile.Roles
			version, _ := capability["version"].(string)
			capability["effective_roles"] = profile.EffectiveRoles(version)
			capability["provider_transport"] = profile.ProviderTransport
			capability["credential_source"] = profile.CredentialSource
			capability["credential_status"] = "passive_not_inspected"
			harnessInput, harnessErr := harness.Discover(paths.ProjectRoot, profile.Adapter, configuration.Project.Harness)
			capability["harness"] = harnessInput
			if harnessErr != nil {
				unmet = append(unmet, harnessErr.Error())
			}
			profiles = append(profiles, capability)
			if capability["available"] != true {
				unmet = append(unmet, "agent command unavailable: "+profile.ID)
			}
		}
		for _, validator := range configuration.Validators {
			command := ""
			if len(validator.Argv) > 0 {
				command, _ = exec.LookPath(validator.Argv[0])
			}
			cwd := filepath.Join(paths.ProjectRoot, filepath.FromSlash(validator.CWD))
			info, err := os.Stat(cwd)
			available := command != "" && err == nil && info.IsDir()
			validators = append(validators, map[string]any{"id": validator.ID, "required": validator.Required, "type": validator.Type, "argv": validator.Argv, "resolved_command": command, "cwd": cwd, "available": available, "probe": "passive"})
			if validator.Required && !available {
				unmet = append(unmet, "required validator unavailable: "+validator.ID)
			}
		}
	}
	dbPath := filepath.Join(paths.StateDir, "state.db")
	_, dbErr := os.Lstat(dbPath)
	store := map[string]any{"opened": false, "path": dbPath, "exists": dbErr == nil, "integrity": "not_checked"}
	if dbErr != nil && !errors.Is(dbErr, os.ErrNotExist) {
		store["error"] = dbErr.Error()
	}
	daemonStatus, daemonErr := app.Status(ctx, paths)
	if daemonErr != nil {
		unmet = append(unmet, "daemon: "+daemonErr.Error())
	}
	configError := ""
	if configErr != nil {
		configError = configErr.Error()
	}
	return map[string]any{
		"validation_preparation": testdiscovery.Inspect(paths.ProjectRoot, &configuration),
		"project_id":             paths.ProjectID, "project_root": paths.ProjectRoot, "state_dir": paths.StateDir, "socket_path": paths.SocketPath, "repository_identity": paths.RepositoryIdentity,
		"store": store, "daemon": daemonStatus, "os": runtime.GOOS, "arch": runtime.GOARCH, "git": gitFacts, "tools": tools, "config_hash": configHash,
		"agent_profiles": profiles, "role_selections": configuration.RoleSelections(), "validators": validators, "unmet_capabilities": unmet,
		"configuration_compatible": configErr == nil, "config_error": configError, "config_migration_required": errors.Is(configErr, config.ErrMigrationRequired),
		"provider_transport": "trusted_profiles_only", "provider_credential_status": "passive_not_inspected", "active_probe_evidence": "none", "project_network_policy": network, "project_secrets_policy": secrets, "isolation_level": "L0",
		"isolation_limit":  "local-process L0 shares host resources, user home, network, credentials and provider quotas",
		"shared_resources": []string{"CPU", "memory", "disk", "ports", "external databases and Docker", "provider credentials and quotas"}, "model_calls": 0,
	}
}

func commandVersion(ctx context.Context, name string) map[string]any {
	path, err := exec.LookPath(name)
	result := map[string]any{"available": err == nil, "path": path, "probe": "passive", "version_available": false}
	if err == nil {
		output, runErr := passiveCommand(ctx, path, "--version")
		result["version"] = strings.TrimSpace(output)
		result["version_available"] = runErr == nil
	}
	return result
}

type cappedBuffer struct{ bytes.Buffer }

func (buffer *cappedBuffer) Write(value []byte) (int, error) {
	n := len(value)
	remaining := 8192 - buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = buffer.Buffer.Write(value)
	}
	return n, nil
}

func passiveCommand(ctx context.Context, name string, args ...string) (string, error) {
	probeContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(probeContext, name, args...)
	command.Env = append(project.GitEnvironment(), "GIT_OPTIONAL_LOCKS=0")
	command.WaitDelay = 250 * time.Millisecond
	var output cappedBuffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return output.String(), err
}
