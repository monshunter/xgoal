package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrRefConflict = errors.New("Git ref compare-and-swap conflict")

type CommitSpec struct {
	Tree      string
	Parent    string
	Message   string
	Timestamp time.Time
}

type Commit struct {
	ID       string
	Tree     string
	Parent   string
	Message  string
	Trailers map[string]string
}

func (repository *Repository) ResolveRef(ctx context.Context, ref string) (Revision, error) {
	if err := repository.validateReadableRef(ctx, ref); err != nil {
		return Revision{}, err
	}
	if strings.HasPrefix(ref, "refs/xgoal/") {
		if err := repository.rejectSymbolicRef(ctx, ref); err != nil {
			return Revision{}, err
		}
	}
	return repository.ResolveRevision(ctx, ref)
}

func (repository *Repository) CreateCommit(ctx context.Context, spec CommitSpec) (Commit, error) {
	if !repository.validObjectID(spec.Tree) || !repository.validObjectID(spec.Parent) || spec.Message == "" || strings.ContainsRune(spec.Message, '\x00') || spec.Timestamp.IsZero() {
		return Commit{}, errors.New("invalid Git commit specification")
	}
	date := spec.Timestamp.UTC().Format(time.RFC3339)
	environment := []string{
		"GIT_AUTHOR_NAME=xgoal", "GIT_AUTHOR_EMAIL=xgoal@localhost", "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=xgoal", "GIT_COMMITTER_EMAIL=xgoal@localhost", "GIT_COMMITTER_DATE=" + date,
		"GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
	}
	command := gitCommand(ctx, repository.root, "commit-tree", spec.Tree, "-p", spec.Parent)
	command.Env = append(command.Env, environment...)
	command.Stdin = strings.NewReader(spec.Message)
	stdout := boundedBuffer{limit: maxGitOutput}
	var stderr limitedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return Commit{}, fmt.Errorf("create Git commit: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	commitID := strings.TrimSpace(stdout.data.String())
	if stdout.exceeded || !repository.validObjectID(commitID) {
		return Commit{}, errors.New("Git returned an invalid commit id")
	}
	commit, err := repository.ReadCommit(ctx, commitID)
	if err != nil {
		return Commit{}, err
	}
	if commit.Tree != spec.Tree || commit.Parent != spec.Parent || commit.Message != strings.TrimSuffix(spec.Message, "\n") {
		return Commit{}, errors.New("created Git commit does not match requested tree, parent, or message")
	}
	return commit, nil
}

func (repository *Repository) ReadCommit(ctx context.Context, commitID string) (Commit, error) {
	if !repository.validObjectID(commitID) {
		return Commit{}, errors.New("invalid Git commit id")
	}
	content, err := runBytes(ctx, repository.root, maxGitOutput, "cat-file", "commit", commitID)
	if err != nil {
		return Commit{}, err
	}
	headers, messageBytes, found := bytes.Cut(content, []byte("\n\n"))
	if !found {
		return Commit{}, errors.New("Git commit object has no message boundary")
	}
	commit := Commit{ID: commitID, Message: strings.TrimSuffix(string(messageBytes), "\n"), Trailers: make(map[string]string)}
	for _, line := range strings.Split(string(headers), "\n") {
		name, value, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		switch name {
		case "tree":
			commit.Tree = value
		case "parent":
			if commit.Parent != "" {
				return Commit{}, errors.New("merge commits are not valid promotion commits")
			}
			commit.Parent = value
		}
	}
	if !repository.validObjectID(commit.Tree) || !repository.validObjectID(commit.Parent) {
		return Commit{}, errors.New("Git commit tree or parent is invalid")
	}
	for _, line := range strings.Split(commit.Message, "\n") {
		key, value, found := strings.Cut(line, ": ")
		if !found || !strings.HasPrefix(key, "XGoal-") {
			continue
		}
		if _, exists := commit.Trailers[key]; exists || value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return Commit{}, fmt.Errorf("invalid or duplicate promotion trailer %q", key)
		}
		commit.Trailers[key] = value
	}
	return commit, nil
}

func (repository *Repository) UpdateRefCAS(ctx context.Context, ref, newCommit, oldCommit string) error {
	if err := repository.validatePrivateRef(ctx, ref); err != nil {
		return err
	}
	if !repository.validObjectID(newCommit) || !repository.validObjectID(oldCommit) {
		return errors.New("invalid Git ref update object id")
	}
	if err := repository.rejectSymbolicRef(ctx, ref); err != nil {
		return err
	}
	// --no-deref protects user branches even if the private ref becomes symbolic
	// after the check above and before the atomic update.
	output, exitCode, err := runWithExit(ctx, repository.root, "update-ref", "--no-deref", ref, newCommit, oldCommit)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("%w: %s", ErrRefConflict, strings.TrimSpace(output))
	}
	resolved, err := repository.ResolveRef(ctx, ref)
	if err != nil {
		return err
	}
	if resolved.Commit != newCommit {
		return errors.New("Git ref read-back did not match CAS target")
	}
	return nil
}

func (repository *Repository) rejectSymbolicRef(ctx context.Context, ref string) error {
	_, exitCode, err := runWithExit(ctx, repository.root, "symbolic-ref", "--quiet", ref)
	if err != nil {
		return err
	}
	switch exitCode {
	case 0:
		return fmt.Errorf("%w: private integration ref must not be symbolic", ErrRefConflict)
	case 1:
		return nil
	default:
		return errors.New("cannot inspect private integration ref type")
	}
}

func (repository *Repository) validateReadableRef(ctx context.Context, ref string) error {
	if !validArgument(ref) || (!strings.HasPrefix(ref, "refs/xgoal/") && !strings.HasPrefix(ref, "refs/heads/")) {
		return errors.New("unsupported integration ref")
	}
	if _, err := run(ctx, repository.root, "check-ref-format", ref); err != nil {
		return err
	}
	return nil
}
func (repository *Repository) validatePrivateRef(ctx context.Context, ref string) error {
	parts := strings.Split(ref, "/")
	if len(parts) != 5 || parts[0] != "refs" || parts[1] != "xgoal" || parts[2] != "goals" || parts[3] == "" || parts[4] != "integration" {
		return errors.New("new integration writes require refs/xgoal/goals/<goal-id>/integration")
	}
	return repository.validateReadableRef(ctx, ref)
}
