// Package projectinit creates the minimal trusted-repository xgoal configuration.
package projectinit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/project"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

type Options struct {
	ProjectRoot       string
	StateDir          string
	SocketPath        string
	AvailableCommands map[string]string
}

type Result struct {
	ProjectRoot    string `json:"project_root"`
	ProjectID      string `json:"project_id"`
	GitCommonDir   string `json:"git_common_dir"`
	ConfigPath     string `json:"config_path"`
	StateDir       string `json:"state_dir"`
	BaseBranch     string `json:"base_branch"`
	CreatedConfig  bool   `json:"created_config"`
	CreatedIgnore  bool   `json:"created_ignore"`
	IsolationLevel string `json:"isolation_level"`
	RemoteChanges  bool   `json:"remote_changes"`
}

func Initialize(ctx context.Context, options Options) (Result, error) {
	paths, err := project.Resolve(ctx, options.ProjectRoot, options.StateDir, options.SocketPath)
	if err != nil {
		return Result{}, err
	}
	root := paths.ProjectRoot
	if err := ensureOnlyInitPathsDirty(ctx, root); err != nil {
		return Result{}, err
	}
	baseBranch, err := git(ctx, root, "branch", "--show-current")
	if err != nil || strings.TrimSpace(baseBranch) == "" {
		return Result{}, errors.New("xgoal init requires a named local base branch")
	}
	commands := options.AvailableCommands
	if commands == nil {
		commands = discoverCommands()
	}
	if commands["codex"] == "" && commands["claude"] == "" {
		return Result{}, errors.New("xgoal init requires Codex CLI or Claude Code CLI")
	}
	owner, err := project.Acquire(ctx, paths)
	if err != nil {
		return Result{}, err
	}
	defer owner.Close()
	if err := sqlite.CheckProjectBinding(ctx, paths.StateDir, sqlite.ProjectBinding{ProjectID: paths.ProjectID, CommonDir: paths.CommonDir, ProjectRoot: paths.ProjectRoot}, paths.LegacyState); err != nil {
		return Result{}, err
	}
	if err := owner.Bind(ctx); err != nil {
		return Result{}, err
	}
	if err := ensureOnlyInitPathsDirty(ctx, root); err != nil {
		return Result{}, err
	}

	configPath := filepath.Join(root, "xgoal.yaml")
	createdConfig := false
	if _, err := os.Lstat(configPath); errors.Is(err, os.ErrNotExist) {
		contents := generatedConfig(filepath.Base(root), baseBranch, commands, fileExists(filepath.Join(root, "go.mod")))
		if _, err := config.Load(strings.NewReader(contents)); err != nil {
			return Result{}, fmt.Errorf("generated xgoal config is invalid: %w", err)
		}
		if err := writeExclusive(configPath, []byte(contents), 0o600); err != nil {
			return Result{}, err
		}
		createdConfig = true
	} else if err != nil {
		return Result{}, err
	} else {
		if err := regularInitFile(configPath); err != nil {
			return Result{}, err
		}
		if _, err := config.LoadFile(configPath); err != nil {
			return Result{}, fmt.Errorf("existing xgoal.yaml is invalid: %w", err)
		}
	}

	createdIgnore := false
	xgoalIgnore := filepath.Join(root, ".xgoalignore")
	if _, err := os.Lstat(xgoalIgnore); errors.Is(err, os.ErrNotExist) {
		if err := writeExclusive(xgoalIgnore, []byte(".git/\n.xgoal/\n"), 0o600); err != nil {
			if createdConfig {
				_ = os.Remove(configPath)
			}
			return Result{}, err
		}
		createdIgnore = true
	} else if err != nil {
		return Result{}, err
	} else if err := regularInitFile(xgoalIgnore); err != nil {
		return Result{}, err
	}
	if err := ensureGitIgnore(root); err != nil {
		return Result{}, err
	}
	return Result{
		ProjectRoot: root, ProjectID: paths.ProjectID, GitCommonDir: paths.CommonDir,
		ConfigPath: configPath, StateDir: paths.StateDir, BaseBranch: baseBranch,
		CreatedConfig: createdConfig, CreatedIgnore: createdIgnore,
		IsolationLevel: "L0", RemoteChanges: false,
	}, nil
}

func generatedConfig(name, baseBranch string, commands map[string]string, goProject bool) string {
	var agents strings.Builder
	if commands["codex"] != "" {
		fmt.Fprintf(&agents, "  - id: codex\n    adapter: codex-cli\n    command: %s\n    roles: [planner, implementer, reviewer]\n    timeout: 45m\n    sandbox: workspace-write\n    providerTransport: allow\n    credentialSource: cli-session\n    activeProbe: explicit\n    environmentAllowlist: [PATH, HOME, TMPDIR]\n", strconv.Quote(commands["codex"]))
	}
	if commands["claude"] != "" {
		fmt.Fprintf(&agents, "  - id: claude\n    adapter: claude-cli\n    command: %s\n    roles: [planner, implementer, reviewer]\n    timeout: 45m\n    permissionMode: dontAsk\n    providerTransport: allow\n    credentialSource: cli-session\n    activeProbe: explicit\n    environmentAllowlist: [PATH, HOME, TMPDIR]\n", strconv.Quote(commands["claude"]))
	}
	validator := "  - id: git-diff-check\n    type: command\n    phases: [change, final]\n    argv: [git, diff, --check]\n    timeout: 2m\n    required: true\n"
	if goProject {
		validator += "  - id: go-test-all\n    type: command\n    phases: [change, final]\n    argv: [go, test, ./...]\n    timeout: 20m\n    required: true\n"
	}
	return fmt.Sprintf(`apiVersion: xgoal.dev/v1alpha1
kind: Project
metadata:
  name: %s
project:
  baseBranch: %s
  trustedRepository: true
orchestration:
  defaultMode: standard
  maxParallel: 1
  leaseTTL: 90s
  heartbeatInterval: 20s
  noProgressLimit: 2
  integrationBranchPrefix: xgoal/
agents:
%sworkspace:
  provider: git-worktree
  keepFailed: true
  cleanupCompletedAfter: 168h
runtime:
  provider: local-process
  isolationLevelRequired: L0
  projectNetwork: deny
  projectSecrets: deny
scopePolicy:
  deny: [/.git/**, /.env, /**/credentials*]
  validatorChanges: human-gate
validators:
%sreview:
  requiredInStandard: true
  blockSeverities: [blocker, high]
  requireIndependentSession: true
  preferDifferentProvider: true
policy:
  gitPush: deny
  publishArtifact: deny
  production: deny
  destructiveCommands: human-gate
  expandScope: human-gate
report:
  formats: [markdown, json]
  includeAgentRawLogs: false
  includeReproductionCommands: true
`, strconv.Quote(name), strconv.Quote(baseBranch), agents.String(), validator)
}

func discoverCommands() map[string]string {
	result := make(map[string]string)
	for _, name := range []string{"codex", "claude"} {
		if path, err := exec.LookPath(name); err == nil {
			result[name] = path
		}
	}
	return result
}

func ensureOnlyInitPathsDirty(ctx context.Context, root string) error {
	output, err := git(ctx, root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	allowed := map[string]bool{"xgoal.yaml": true, ".xgoalignore": true, ".gitignore": true}
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		if len(line) < 4 {
			return errors.New("cannot parse Git worktree status")
		}
		path := strings.TrimSpace(line[3:])
		if old, newPath, renamed := strings.Cut(path, " -> "); renamed {
			if !allowed[old] || !allowed[newPath] {
				return errors.New("xgoal init requires a clean worktree outside initialization files")
			}
			continue
		}
		if !allowed[path] && path != ".xgoal/" && !strings.HasPrefix(path, ".xgoal/") {
			return errors.New("xgoal init requires a clean worktree outside initialization files")
		}
	}
	return nil
}

func ensureGitIgnore(root string) error {
	path := filepath.Join(root, ".gitignore")
	if err := regularInitFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	contents, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSpace(line) == ".xgoal/" {
			return nil
		}
	}
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		contents = append(contents, '\n')
	}
	contents = append(contents, []byte(".xgoal/\n")...)
	return writeReplace(path, contents, 0o600)
}

func writeExclusive(path string, contents []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	return file.Close()
}

func writeReplace(path string, contents []byte, mode os.FileMode) error {
	temporary := path + ".xgoal-init-tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Rename(temporary, path)
}

func regularInitFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("initialization file must be a regular file: %s", path)
	}
	return nil
}

func git(ctx context.Context, root string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = root
	command.Env = project.GitEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

func fileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}
