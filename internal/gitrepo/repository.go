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

type WorktreeInfo struct {
	Path       string
	GitDir     string
	CommonDir  string
	HeadCommit string
	HeadTree   string
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

func (repository *Repository) EnsureIntegrationBranch(ctx context.Context, branchName, baseCommit string) (Revision, bool, error) {
	if !validArgument(branchName) || !validArgument(baseCommit) {
		return Revision{}, false, errors.New("invalid integration branch or base commit")
	}
	if _, err := run(ctx, repository.root, "check-ref-format", "--branch", branchName); err != nil {
		return Revision{}, false, fmt.Errorf("invalid integration branch %q: %w", branchName, err)
	}
	base, err := repository.ResolveRevision(ctx, baseCommit)
	if err != nil {
		return Revision{}, false, err
	}
	ref := "refs/heads/" + branchName
	existing, exitCode, err := runWithExit(ctx, repository.root, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return Revision{}, false, fmt.Errorf("inspect integration ref %q: %w", ref, err)
	}
	if exitCode == 0 {
		revision, err := repository.ResolveRevision(ctx, strings.TrimSpace(existing))
		return revision, false, err
	}
	if exitCode != 1 {
		return Revision{}, false, fmt.Errorf("inspect integration ref %q exited %d", ref, exitCode)
	}
	zero := strings.Repeat("0", len(base.Commit))
	if _, err := run(ctx, repository.root, "update-ref", ref, base.Commit, zero); err != nil {
		return Revision{}, false, fmt.Errorf("create integration ref %q: %w", ref, err)
	}
	return base, true, nil
}

func (repository *Repository) AddDetachedWorktree(ctx context.Context, worktreePath, commit string) (WorktreeInfo, error) {
	if !filepath.IsAbs(worktreePath) || filepath.Clean(worktreePath) != worktreePath || !validArgument(worktreePath) || !validArgument(commit) {
		return WorktreeInfo{}, errors.New("worktree path must be a clean absolute path and commit must be valid")
	}
	if _, err := os.Lstat(worktreePath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return WorktreeInfo{}, fmt.Errorf("worktree path %q already exists", worktreePath)
		}
		return WorktreeInfo{}, fmt.Errorf("inspect worktree path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o700); err != nil {
		return WorktreeInfo{}, fmt.Errorf("create worktree parent: %w", err)
	}
	if _, err := run(ctx, repository.root, "worktree", "add", "--detach", worktreePath, commit); err != nil {
		return WorktreeInfo{}, fmt.Errorf("add detached worktree: %w", err)
	}
	info, err := repository.InspectWorktree(ctx, worktreePath)
	if err != nil {
		return WorktreeInfo{}, err
	}
	return info, nil
}

func (repository *Repository) InspectWorktree(ctx context.Context, worktreePath string) (WorktreeInfo, error) {
	canonical, err := canonicalDirectory(worktreePath)
	if err != nil {
		return WorktreeInfo{}, fmt.Errorf("canonicalize worktree: %w", err)
	}
	rootOutput, err := run(ctx, canonical, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return WorktreeInfo{}, fmt.Errorf("resolve worktree root: %w", err)
	}
	root, err := canonicalDirectory(strings.TrimSpace(rootOutput))
	if err != nil || root != canonical {
		return WorktreeInfo{}, fmt.Errorf("worktree root mismatch: got %q want %q", root, canonical)
	}
	commonOutput, err := run(ctx, canonical, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return WorktreeInfo{}, fmt.Errorf("resolve worktree common directory: %w", err)
	}
	commonDir, err := canonicalDirectory(strings.TrimSpace(commonOutput))
	if err != nil || commonDir != repository.commonDir {
		return WorktreeInfo{}, fmt.Errorf("worktree common directory mismatch: got %q want %q", commonDir, repository.commonDir)
	}
	gitMarker := filepath.Join(canonical, ".git")
	markerInfo, err := os.Lstat(gitMarker)
	if err != nil || !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 {
		return WorktreeInfo{}, errors.New("worktree .git marker is missing or unsafe")
	}
	marker, err := os.ReadFile(gitMarker)
	if err != nil {
		return WorktreeInfo{}, fmt.Errorf("read worktree .git marker: %w", err)
	}
	gitDirText := strings.TrimSpace(string(marker))
	if !strings.HasPrefix(gitDirText, "gitdir: ") {
		return WorktreeInfo{}, errors.New("worktree .git marker is malformed")
	}
	gitDirPath := strings.TrimPrefix(gitDirText, "gitdir: ")
	if !filepath.IsAbs(gitDirPath) {
		gitDirPath = filepath.Join(canonical, gitDirPath)
	}
	gitDir, err := canonicalDirectory(gitDirPath)
	if err != nil || !isWithin(filepath.Join(repository.commonDir, "worktrees"), gitDir) {
		return WorktreeInfo{}, errors.New("worktree gitdir is outside the expected common directory")
	}
	registered, err := repository.worktreeRegistered(ctx, canonical)
	if err != nil {
		return WorktreeInfo{}, err
	}
	if !registered {
		return WorktreeInfo{}, errors.New("worktree is not registered")
	}
	revision, err := repository.resolveFromDirectory(ctx, canonical, "HEAD")
	if err != nil {
		return WorktreeInfo{}, err
	}
	return WorktreeInfo{Path: canonical, GitDir: gitDir, CommonDir: commonDir, HeadCommit: revision.Commit, HeadTree: revision.Tree}, nil
}

func (repository *Repository) RemoveWorktree(ctx context.Context, worktreePath string) error {
	info, err := repository.InspectWorktree(ctx, worktreePath)
	if err != nil {
		return err
	}
	if _, err := run(ctx, repository.root, "worktree", "remove", "--force", info.Path); err != nil {
		return fmt.Errorf("remove worktree: %w", err)
	}
	registered, err := repository.worktreeRegistered(ctx, info.Path)
	if err != nil {
		return err
	}
	if registered {
		return errors.New("worktree remains registered after removal")
	}
	if _, err := os.Lstat(info.Path); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("worktree path remains after removal: %w", err)
	}
	return nil
}

func (repository *Repository) resolveFromDirectory(ctx context.Context, directory, revision string) (Revision, error) {
	commit, err := run(ctx, directory, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return Revision{}, fmt.Errorf("resolve worktree commit: %w", err)
	}
	commit = strings.TrimSpace(commit)
	tree, err := run(ctx, directory, "rev-parse", "--verify", "--end-of-options", commit+"^{tree}")
	if err != nil {
		return Revision{}, fmt.Errorf("resolve worktree tree: %w", err)
	}
	return Revision{Commit: commit, Tree: strings.TrimSpace(tree)}, nil
}

func (repository *Repository) worktreeRegistered(ctx context.Context, worktreePath string) (bool, error) {
	output, err := run(ctx, repository.root, "worktree", "list", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("list Git worktrees: %w", err)
	}
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		listed, err := canonicalDirectory(strings.TrimPrefix(line, "worktree "))
		if err == nil && listed == worktreePath {
			return true, nil
		}
	}
	return false, nil
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
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var output limitedBuffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
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
	data bytes.Buffer
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	remaining := maxGitOutput - buffer.data.Len()
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
