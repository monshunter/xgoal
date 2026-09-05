package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/project"
)

const maxGitOutput = 4 << 20

type Repository struct {
	root         string
	commonDir    string
	objectFormat string
}

type Revision struct {
	Commit string
	Tree   string
}

func Open(ctx context.Context, directory string) (*Repository, error) {
	canonical, err := canonicalDirectory(directory)
	if err != nil {
		return nil, err
	}
	rootOutput, err := run(ctx, canonical, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("resolve Git repository root: %w", err)
	}
	root, err := canonicalDirectory(strings.TrimSpace(rootOutput))
	if err != nil {
		return nil, fmt.Errorf("canonicalize Git repository root: %w", err)
	}
	bare, err := run(ctx, root, "rev-parse", "--is-bare-repository")
	if err != nil {
		return nil, fmt.Errorf("inspect bare repository state: %w", err)
	}
	if strings.TrimSpace(bare) != "false" {
		return nil, errors.New("bare Git repositories are unsupported")
	}
	commonOutput, err := run(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("resolve Git common directory: %w", err)
	}
	commonDir, err := canonicalDirectory(strings.TrimSpace(commonOutput))
	if err != nil {
		return nil, fmt.Errorf("canonicalize Git common directory: %w", err)
	}
	objectFormat, err := run(ctx, root, "rev-parse", "--show-object-format")
	if err != nil {
		return nil, fmt.Errorf("read Git object format: %w", err)
	}
	format := strings.TrimSpace(objectFormat)
	if format != "sha1" && format != "sha256" {
		return nil, fmt.Errorf("unsupported Git object format %q", format)
	}
	return &Repository{root: root, commonDir: commonDir, objectFormat: format}, nil
}

func (repository *Repository) Root() string      { return repository.root }
func (repository *Repository) CommonDir() string { return repository.commonDir }

func (repository *Repository) ResolveRevision(ctx context.Context, revision string) (Revision, error) {
	if !validArgument(revision) {
		return Revision{}, errors.New("invalid Git revision")
	}
	commit, err := run(ctx, repository.root, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return Revision{}, fmt.Errorf("resolve Git commit %q: %w", revision, err)
	}
	commit = strings.TrimSpace(commit)
	tree, err := run(ctx, repository.root, "rev-parse", "--verify", "--end-of-options", commit+"^{tree}")
	if err != nil {
		return Revision{}, fmt.Errorf("resolve Git tree for %q: %w", revision, err)
	}
	return Revision{Commit: commit, Tree: strings.TrimSpace(tree)}, nil
}

func canonicalDirectory(directory string) (string, error) {
	if directory == "" || strings.ContainsAny(directory, "\r\n\x00") {
		return "", errors.New("directory is empty or contains forbidden characters")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("make directory absolute: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		return "", fmt.Errorf("resolve directory symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("directory is not accessible: %w", err)
	}
	return resolved, nil
}

func isWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func validArgument(value string) bool {
	return value != "" && !strings.ContainsAny(value, "\r\n\x00")
}

func run(ctx context.Context, directory string, arguments ...string) (string, error) {
	output, exitCode, err := runWithExit(ctx, directory, arguments...)
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		return "", fmt.Errorf("git %s exited %d: %s", arguments[0], exitCode, strings.TrimSpace(output))
	}
	return output, nil
}

func runWithExit(ctx context.Context, directory string, arguments ...string) (string, int, error) {
	command := gitCommand(ctx, directory, arguments...)
	var output limitedBuffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if output.exceeded {
		return "", -1, errors.New("Git output exceeds limit")
	}
	if err == nil {
		return output.String(), 0, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return output.String(), exitError.ExitCode(), nil
	}
	return output.String(), -1, fmt.Errorf("execute git: %w", err)
}

type limitedBuffer struct {
	data     bytes.Buffer
	exceeded bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	remaining := maxGitOutput - buffer.data.Len()
	if len(value) > remaining {
		buffer.exceeded = true
	}
	if remaining > 0 {
		if len(value) > remaining {
			_, _ = buffer.data.Write(value[:remaining])
		} else {
			_, _ = buffer.data.Write(value)
		}
	}
	return len(value), nil
}

func (buffer *limitedBuffer) String() string { return buffer.data.String() }

func gitCommand(ctx context.Context, directory string, args ...string) *exec.Cmd {
	arguments := []string{"--no-replace-objects", "-C", directory, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "commit.gpgSign=false"}
	command := exec.CommandContext(ctx, "git", append(arguments, args...)...)
	command.Env = append(project.GitEnvironment(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	command.WaitDelay = 250 * time.Millisecond
	return command
}

func (repository *Repository) EnsureIntegrationRef(ctx context.Context, ref, baseCommit string) (Revision, bool, error) {
	if err := repository.validatePrivateRef(ctx, ref); err != nil {
		return Revision{}, false, err
	}
	if err := repository.rejectSymbolicRef(ctx, ref); err != nil {
		return Revision{}, false, err
	}
	base, err := repository.ResolveRevision(ctx, baseCommit)
	if err != nil {
		return Revision{}, false, err
	}
	existing, exit, err := runWithExit(ctx, repository.root, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return Revision{}, false, err
	}
	if exit == 0 {
		revision, err := repository.ResolveRevision(ctx, strings.TrimSpace(existing))
		return revision, false, err
	}
	if exit != 1 {
		return Revision{}, false, errors.New("cannot inspect private integration ref")
	}
	zero := strings.Repeat("0", len(base.Commit))
	if _, err := run(ctx, repository.root, "update-ref", "--no-deref", ref, base.Commit, zero); err != nil {
		return Revision{}, false, err
	}
	return base, true, nil
}
