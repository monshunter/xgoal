package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	if err := repository.validateBranchRef(ctx, ref); err != nil {
		return Revision{}, err
	}
	return repository.ResolveRevision(ctx, ref)
}

func (repository *Repository) CreateCommit(ctx context.Context, spec CommitSpec) (Commit, error) {
	if !repository.validObjectID(spec.Tree) || !repository.validObjectID(spec.Parent) || spec.Message == "" || strings.ContainsRune(spec.Message, '\x00') || spec.Timestamp.IsZero() {
		return Commit{}, errors.New("invalid Git commit specification")
	}
	date := spec.Timestamp.UTC().Format(time.RFC3339)
	environment := append(os.Environ(),
		"GIT_AUTHOR_NAME=xgoal", "GIT_AUTHOR_EMAIL=xgoal@localhost", "GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_NAME=xgoal", "GIT_COMMITTER_EMAIL=xgoal@localhost", "GIT_COMMITTER_DATE="+date,
		"GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
	)
	command := exec.CommandContext(ctx, "git", "-C", repository.root, "commit-tree", spec.Tree, "-p", spec.Parent)
	command.Env = environment
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
	if err := repository.validateBranchRef(ctx, ref); err != nil {
		return err
	}
	if !repository.validObjectID(newCommit) || !repository.validObjectID(oldCommit) {
		return errors.New("invalid Git ref update object id")
	}
	output, exitCode, err := runWithExit(ctx, repository.root, "update-ref", ref, newCommit, oldCommit)
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

func (repository *Repository) validateBranchRef(ctx context.Context, ref string) error {
	if !strings.HasPrefix(ref, "refs/heads/") || !validArgument(ref) {
		return errors.New("integration ref must be a full branch ref")
	}
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if _, err := run(ctx, repository.root, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("invalid integration ref: %w", err)
	}
	return nil
}
