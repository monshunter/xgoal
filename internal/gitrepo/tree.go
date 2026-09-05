package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/scope"
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
	command := gitCommand(ctx, directory, arguments...)
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

// WriteBlob writes raw content without invoking attributes, filters, hooks or the user index.
func (repository *Repository) WriteBlob(ctx context.Context, content []byte) (string, error) {
	output, err := runInput(ctx, repository.root, nil, content, maxGitOutput, "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(output))
	if !repository.validObjectID(id) {
		return "", errors.New("Git returned an invalid blob id")
	}
	return id, nil
}

// BuildTree builds an object-only candidate in a private index. Entries retain their raw Git path spelling.
func (repository *Repository) BuildTree(ctx context.Context, entries []TreeEntry) (string, error) {
	paths := make([]string, 0, len(entries))
	byPath := map[string]bool{}
	for _, entry := range entries {
		if !validTreeMode(entry.Mode) || !repository.validObjectID(entry.ObjectID) {
			return "", errors.New("invalid tree entry mode or object")
		}
		paths = append(paths, entry.Path)
		byPath[entry.Path] = true
	}
	if _, err := scope.CanonicalizePaths(paths); err != nil {
		return "", err
	}
	for _, entry := range entries {
		for parent := path.Dir(entry.Path); parent != "."; parent = path.Dir(parent) {
			if byPath[parent] {
				return "", fmt.Errorf("tree contains file/directory conflict: %s", entry.Path)
			}
		}
	}
	unique := map[string]bool{}
	var objects strings.Builder
	for _, entry := range entries {
		if !unique[entry.ObjectID] {
			unique[entry.ObjectID] = true
			objects.WriteString(entry.ObjectID + "\n")
		}
	}
	if len(unique) > 0 {
		output, err := runInput(ctx, repository.root, nil, []byte(objects.String()), maxTreeListingBytes, "cat-file", "--batch-check=%(objectname) %(objecttype)")
		if err != nil {
			return "", err
		}
		records := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
		if len(records) != len(unique) {
			return "", errors.New("Git did not verify every tree object")
		}
		for _, record := range records {
			fields := strings.Fields(record)
			if len(fields) != 2 || fields[1] != "blob" || !unique[fields[0]] {
				return "", errors.New("tree entry does not reference a valid blob")
			}
		}
	}
	temporary, err := os.MkdirTemp("", "xgoal-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	index := filepath.Join(temporary, "index")
	if _, err := repository.runIndex(ctx, index, nil, "read-tree", "--empty"); err != nil {
		return "", err
	}
	sorted := append([]TreeEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	var input bytes.Buffer
	for _, entry := range sorted {
		fmt.Fprintf(&input, "%s %s\t%s%c", entry.Mode, entry.ObjectID, entry.Path, 0)
	}
	if len(sorted) > 0 {
		if _, err := repository.runIndex(ctx, index, input.Bytes(), "update-index", "-z", "--index-info"); err != nil {
			return "", err
		}
	}
	output, err := repository.runIndex(ctx, index, nil, "write-tree")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(output))
	if !repository.validObjectID(tree) {
		return "", errors.New("invalid generated tree id")
	}
	return tree, nil
}

func (repository *Repository) runIndex(ctx context.Context, index string, input []byte, args ...string) ([]byte, error) {
	return runInput(ctx, repository.root, []string{"GIT_INDEX_FILE=" + index}, input, maxTreeListingBytes, args...)
}

func runInput(ctx context.Context, directory string, environment []string, input []byte, limit int64, args ...string) ([]byte, error) {
	command := gitCommand(ctx, directory, args...)
	command.Env = append(command.Env, environment...)
	command.Stdin = bytes.NewReader(input)
	output := boundedBuffer{limit: limit}
	var stderr limitedBuffer
	command.Stdout = &output
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, stderr.String())
	}
	if output.exceeded {
		return nil, fmt.Errorf("git %s output exceeds limit", args[0])
	}
	return output.data.Bytes(), nil
}
