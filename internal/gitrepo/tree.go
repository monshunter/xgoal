package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxTreeListingBytes = 64 << 20

type TreeEntry struct {
	Path     string
	Mode     string
	ObjectID string
}

func (repository *Repository) ReadFileAtRevision(ctx context.Context, commit, filename string, maxBytes int64) (TreeEntry, []byte, error) {
	if filename == "" || strings.ContainsAny(filename, "\r\n\x00") {
		return TreeEntry{}, nil, errors.New("invalid Git tree path")
	}
	entries, err := repository.ListTree(ctx, commit)
	if err != nil {
		return TreeEntry{}, nil, err
	}
	for _, entry := range entries {
		if entry.Path != filename {
			continue
		}
		content, err := repository.ReadBlob(ctx, entry.ObjectID, maxBytes)
		return entry, content, err
	}
	return TreeEntry{}, nil, fmt.Errorf("path %q does not exist in Git revision %q", filename, commit)
}

func (repository *Repository) ObjectFormat() string { return repository.objectFormat }

func (repository *Repository) ListTree(ctx context.Context, commit string) ([]TreeEntry, error) {
	if !validArgument(commit) {
		return nil, errors.New("invalid tree commit")
	}
	output, err := runBytes(ctx, repository.root, maxTreeListingBytes, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return nil, fmt.Errorf("list Git tree %q: %w", commit, err)
	}
	records := bytes.Split(output, []byte{0})
	entries := make([]TreeEntry, 0, len(records)-1)
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		metadata, filename, found := bytes.Cut(record, []byte{'\t'})
		if !found || !utf8.Valid(filename) {
			return nil, errors.New("Git tree contains malformed or non-UTF-8 path data")
		}
		fields := strings.Fields(string(metadata))
		if len(fields) != 3 || fields[1] != "blob" || !validTreeMode(fields[0]) || !repository.validObjectID(fields[2]) {
			return nil, fmt.Errorf("unsupported Git tree entry %q", record)
		}
		entries = append(entries, TreeEntry{Path: string(filename), Mode: fields[0], ObjectID: fields[2]})
	}
	return entries, nil
}

func (repository *Repository) ReadBlob(ctx context.Context, objectID string, maxBytes int64) ([]byte, error) {
	if !repository.validObjectID(objectID) || maxBytes <= 0 {
		return nil, errors.New("invalid Git blob request")
	}
	sizeOutput, err := run(ctx, repository.root, "cat-file", "-s", objectID)
	if err != nil {
		return nil, fmt.Errorf("read Git blob size: %w", err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeOutput), 10, 64)
	if err != nil || size < 0 || size > maxBytes {
		return nil, fmt.Errorf("Git blob %s size %d exceeds limit %d", objectID, size, maxBytes)
	}
	content, err := runBytes(ctx, repository.root, maxBytes, "cat-file", "blob", objectID)
	if err != nil {
		return nil, fmt.Errorf("read Git blob %s: %w", objectID, err)
	}
	if int64(len(content)) != size {
		return nil, fmt.Errorf("Git blob %s changed size during read", objectID)
	}
	return content, nil
}

func (repository *Repository) EnsureCleanWorktree(ctx context.Context, worktreePath string) (WorktreeInfo, error) {
	info, err := repository.InspectWorktree(ctx, worktreePath)
	if err != nil {
		return WorktreeInfo{}, err
	}
	output, err := runBytes(ctx, info.Path, maxTreeListingBytes, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return WorktreeInfo{}, fmt.Errorf("inspect validation worktree status: %w", err)
	}
	if len(output) != 0 {
		return WorktreeInfo{}, errors.New("validation worktree is not clean")
	}
	return info, nil
}

func (repository *Repository) IndexAndWriteTree(ctx context.Context, worktreePath string) (string, error) {
	info, err := repository.InspectWorktree(ctx, worktreePath)
	if err != nil {
		return "", err
	}
	if _, err := run(ctx, info.Path, "-c", "core.hooksPath=/dev/null", "add", "--all", "--"); err != nil {
		return "", fmt.Errorf("index replayed tree: %w", err)
	}
	tree, err := run(ctx, info.Path, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write replayed tree: %w", err)
	}
	tree = strings.TrimSpace(tree)
	if !repository.validObjectID(tree) {
		return "", errors.New("Git returned an invalid candidate tree id")
	}
	return tree, nil
}

func (repository *Repository) validObjectID(value string) bool {
	want := 40
	if repository.objectFormat == "sha256" {
		want = 64
	}
	if len(value) != want {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validTreeMode(value string) bool {
	return value == "100644" || value == "100755" || value == "120000"
}

func runBytes(ctx context.Context, directory string, limit int64, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	stdout := boundedBuffer{limit: limit}
	var stderr limitedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start git: %w", err)
	}
	err := command.Wait()
	if err != nil {
		return nil, fmt.Errorf("git %s failed: %w: %s", arguments[0], err, strings.TrimSpace(stderr.String()))
	}
	if stdout.exceeded {
		return nil, fmt.Errorf("git %s output exceeds %d bytes", arguments[0], limit)
	}
	return stdout.data.Bytes(), nil
}

type boundedBuffer struct {
	data     bytes.Buffer
	limit    int64
	exceeded bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - int64(buffer.data.Len())
	if int64(len(value)) > remaining {
		buffer.exceeded = true
		if remaining > 0 {
			_, _ = buffer.data.Write(value[:remaining])
		}
		return len(value), nil
	}
	_, _ = buffer.data.Write(value)
	return len(value), nil
}
