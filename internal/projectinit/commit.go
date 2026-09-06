package projectinit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/monshunter/xgoal/internal/project"
)

// commitUnborn is an initialization-only exception to the runtime's HEAD/index
// preservation rule. The caller holds the project lock and has rejected unrelated
// dirty files. Git's --only also excludes anything else already in the index.
func commitUnborn(ctx context.Context, paths project.Paths) (string, error) {
	if _, err := git(ctx, paths.ProjectRoot, "rev-parse", "--verify", "HEAD^{commit}"); err == nil {
		return "", nil
	}
	ref, err := git(ctx, paths.ProjectRoot, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return "", fmt.Errorf("initialization requires an unborn named branch: %w", err)
	}
	if !strings.HasPrefix(ref, "refs/heads/") {
		return "", errors.New("initialization requires an unborn local branch")
	}
	_, err = git(ctx, paths.ProjectRoot, "show-ref", "--verify", "--quiet", ref)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return "", fmt.Errorf("HEAD cannot be resolved and its branch is not unborn: %v", err)
	}
	// show-ref --quiet can report a malformed loose ref as absent. Never replace
	// that user-owned metadata with a new root commit.
	if _, err := os.Lstat(filepath.Join(paths.CommonDir, ref)); !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("unresolved HEAD has an existing or unreadable branch reference: %s", ref)
	}
	files := []string{".gitignore", ".xgoalignore", "xgoal.yaml"}
	if _, err := git(ctx, paths.ProjectRoot, append([]string{"add", "--intent-to-add", "--"}, files...)...); err != nil {
		return "", err
	}
	args := []string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "commit", "--only", "-m", "chore: initialize xgoal", "--"}
	if _, err := git(ctx, paths.ProjectRoot, append(args, files...)...); err != nil {
		return "", err
	}
	return git(ctx, paths.ProjectRoot, "rev-parse", "--verify", "HEAD^{commit}")
}
