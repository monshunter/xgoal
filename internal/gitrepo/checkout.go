package gitrepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monshunter/xgoal/internal/scope"
	"golang.org/x/sys/unix"
)

const DefaultMaxFileBytes int64 = 64 << 20

var ErrCheckoutChanged = errors.New("current checkout identity or source tree changed")

type CheckoutIdentity struct {
	Root          string `json:"root"`
	CommonDir     string `json:"common_dir"`
	HeadCommit    string `json:"head_commit"`
	HeadTree      string `json:"head_tree"`
	SymbolicHEAD  string `json:"symbolic_head"`
	IndexHash     string `json:"index_hash"`
	IndexTree     string `json:"index_tree"`
	GitConfigHash string `json:"git_config_hash"`
	IndexPresent  bool   `json:"index_present"`
}

type SnapshotSpec struct {
	BaseTree     string
	ExcludePaths []string
	MaxFileBytes int64
}
type RawFile struct {
	Path    string
	Mode    string
	Content []byte
}
type TreeSnapshot struct {
	Tree     string
	Identity CheckoutIdentity
	Files    []RawFile
}

func (identity CheckoutIdentity) Validate() error {
	if !filepath.IsAbs(identity.Root) || filepath.Clean(identity.Root) != identity.Root || !filepath.IsAbs(identity.CommonDir) || filepath.Clean(identity.CommonDir) != identity.CommonDir {
		return errors.New("checkout identity requires canonical absolute paths")
	}
	for _, id := range []string{identity.HeadCommit, identity.HeadTree, identity.IndexTree} {
		if !hexHash(id, 40) && !hexHash(id, 64) {
			return errors.New("checkout identity has invalid Git object id")
		}
	}
	if !hexHash(identity.IndexHash, 64) || !hexHash(identity.GitConfigHash, 64) {
		return errors.New("checkout identity has invalid metadata hash")
	}
	if identity.SymbolicHEAD != "" && (!strings.HasPrefix(identity.SymbolicHEAD, "refs/heads/") || !validArgument(identity.SymbolicHEAD)) {
		return errors.New("checkout identity has invalid symbolic HEAD")
	}
	return nil
}

func (repository *Repository) InspectCheckout(ctx context.Context, directory string) (CheckoutIdentity, error) {
	canonical, err := canonicalDirectory(directory)
	if err != nil {
		return CheckoutIdentity{}, err
	}
	if canonical != repository.root {
		return CheckoutIdentity{}, errors.New("execution path is not the current repository root")
	}
	return repository.ReadCheckoutIdentity(ctx)
}

func (repository *Repository) ReadCheckoutIdentity(ctx context.Context) (CheckoutIdentity, error) {
	if err := repository.checkPrimaryCheckout(ctx); err != nil {
		return CheckoutIdentity{}, err
	}
	before, err := repository.readCheckoutMetadata(ctx)
	if err != nil {
		return CheckoutIdentity{}, err
	}
	indexData, present, err := readMetadataFile(filepath.Join(repository.commonDir, "index"), maxTreeListingBytes)
	if err != nil {
		return CheckoutIdentity{}, err
	}
	if present != before.IndexPresent || indexFingerprint(indexData, present) != before.IndexHash {
		return CheckoutIdentity{}, ErrCheckoutChanged
	}
	temporary, err := os.MkdirTemp("", "xgoal-index-")
	if err != nil {
		return CheckoutIdentity{}, err
	}
	defer os.RemoveAll(temporary)
	indexPath := filepath.Join(temporary, "index")
	if present {
		if err := os.WriteFile(indexPath, indexData, 0600); err != nil {
			return CheckoutIdentity{}, err
		}
	}
	tree, err := repository.runIndex(ctx, indexPath, nil, "write-tree")
	if err != nil {
		return CheckoutIdentity{}, fmt.Errorf("read user index tree without modifying it: %w", err)
	}
	before.IndexTree = strings.TrimSpace(string(tree))
	after, err := repository.readCheckoutMetadata(ctx)
	if err != nil {
		return CheckoutIdentity{}, err
	}
	after.IndexTree = before.IndexTree
	if before != after {
		return CheckoutIdentity{}, ErrCheckoutChanged
	}
	if err := before.Validate(); err != nil {
		return CheckoutIdentity{}, err
	}
	return before, nil
}

func (repository *Repository) CheckCheckoutIdentity(ctx context.Context, expected CheckoutIdentity) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	current, err := repository.ReadCheckoutIdentity(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCheckoutChanged, err)
	}
	if current != expected {
		return fmt.Errorf("%w: HEAD, index, repository or trusted Git configuration differs", ErrCheckoutChanged)
	}
	return nil
}

func (repository *Repository) checkPrimaryCheckout(ctx context.Context) error {
	current, err := Open(ctx, repository.root)
	if err != nil {
		return err
	}
	if current.root != repository.root || current.commonDir != repository.commonDir || current.objectFormat != repository.objectFormat {
		return ErrCheckoutChanged
	}
	gitdir, err := run(ctx, repository.root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	canonical, err := canonicalDirectory(strings.TrimSpace(gitdir))
	if err != nil || canonical != repository.commonDir {
		return errors.New("linked Git worktree execution is unsupported; use the primary working directory")
	}
	marker, err := os.Lstat(filepath.Join(repository.root, ".git"))
	if err != nil {
		return err
	}
	if marker.Mode()&os.ModeSymlink != 0 || (!marker.IsDir() && !marker.Mode().IsRegular()) {
		return errors.New("unsafe root Git metadata")
	}
	return nil
}

func (repository *Repository) readCheckoutMetadata(ctx context.Context) (CheckoutIdentity, error) {
	revision, err := repository.ResolveRevision(ctx, "HEAD")
	if err != nil {
		return CheckoutIdentity{}, err
	}
	symbolic, code, err := runWithExit(ctx, repository.root, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return CheckoutIdentity{}, fmt.Errorf("read symbolic HEAD: %w", err)
	}
	if code > 1 {
		return CheckoutIdentity{}, fmt.Errorf("read symbolic HEAD exited %d", code)
	}
	index, present, err := readMetadataFile(filepath.Join(repository.commonDir, "index"), maxTreeListingBytes)
	if err != nil {
		return CheckoutIdentity{}, err
	}
	configuration, err := runBytes(ctx, repository.root, maxTreeListingBytes, "config", "--null", "--list", "--show-origin")
	if err != nil {
		return CheckoutIdentity{}, err
	}
	hash := sha256.New()
	_, _ = hash.Write(configuration)
	for _, relative := range []string{"config", "config.worktree", "info/exclude", "info/attributes", "info/sparse-checkout"} {
		content, exists, err := readMetadataFile(filepath.Join(repository.commonDir, filepath.FromSlash(relative)), maxTreeListingBytes)
		if err != nil {
			return CheckoutIdentity{}, err
		}
		encoded, _ := json.Marshal(struct {
			Name    string
			Present bool
			Content []byte
		}{relative, exists, content})
		_, _ = hash.Write(encoded)
	}
	excludes, exit, err := runWithExit(ctx, repository.root, "config", "--null", "--path", "--get", "core.excludesFile")
	if err != nil {
		return CheckoutIdentity{}, err
	}
	var excludesPath string
	if exit == 0 {
		if !strings.HasSuffix(excludes, "\x00") {
			return CheckoutIdentity{}, errors.New("invalid Git excludes configuration")
		}
		// An explicitly empty value disables the default file. Preserve path
		// whitespace; Git's NUL delimiter is the only byte to remove.
		excludesPath = strings.TrimSuffix(excludes, "\x00")
	} else if exit == 1 {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			excludesPath = filepath.Join(xdg, "git", "ignore")
		} else if configHome := os.Getenv("HOME"); configHome != "" {
			excludesPath = filepath.Join(configHome, ".config", "git", "ignore")
		}
	} else {
		return CheckoutIdentity{}, errors.New("cannot inspect Git excludes configuration")
	}
	if excludesPath != "" {
		if !filepath.IsAbs(excludesPath) {
			excludesPath = filepath.Join(repository.root, excludesPath)
		}
		content, exists, err := readMetadataFile(excludesPath, maxTreeListingBytes)
		if err != nil {
			return CheckoutIdentity{}, err
		}
		encoded, _ := json.Marshal(struct {
			Name    string
			Present bool
			Content []byte
		}{excludesPath, exists, content})
		_, _ = hash.Write(encoded)
	}
	return CheckoutIdentity{Root: repository.root, CommonDir: repository.commonDir, HeadCommit: revision.Commit, HeadTree: revision.Tree, SymbolicHEAD: strings.TrimSpace(symbolic), IndexHash: indexFingerprint(index, present), IndexPresent: present, GitConfigHash: hex.EncodeToString(hash.Sum(nil))}, nil
}

func (repository *Repository) SnapshotTree(ctx context.Context, spec SnapshotSpec) (TreeSnapshot, error) {
	if spec.MaxFileBytes == 0 {
		spec.MaxFileBytes = DefaultMaxFileBytes
	}
	if !repository.validObjectID(spec.BaseTree) || spec.MaxFileBytes < 0 {
		return TreeSnapshot{}, errors.New("snapshot requires a base tree and nonnegative file limit")
	}
	excluded, err := repository.exclusions(spec.ExcludePaths)
	if err != nil {
		return TreeSnapshot{}, err
	}
	identity, err := repository.ReadCheckoutIdentity(ctx)
	if err != nil {
		return TreeSnapshot{}, err
	}
	base, err := repository.ListTree(ctx, spec.BaseTree)
	if err != nil {
		return TreeSnapshot{}, err
	}
	selected := map[string]bool{}
	for _, entry := range base {
		if excluded(entry.Path) {
			return TreeSnapshot{}, fmt.Errorf("base tree tracks excluded Git or xgoal metadata: %s", entry.Path)
		}
		selected[entry.Path] = true
	}
	untracked, err := repository.sourceInventory(ctx, excluded)
	if err != nil {
		return TreeSnapshot{}, err
	}
	for _, name := range untracked {
		if _, exists := selected[name]; !exists {
			selected[name] = false
		}
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	if _, err := scope.CanonicalizePaths(names); err != nil {
		return TreeSnapshot{}, err
	}
	rootFD, err := unix.Open(repository.root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return TreeSnapshot{}, err
	}
	defer unix.Close(rootFD)
	files := make([]RawFile, 0, len(names))
	entries := make([]TreeEntry, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return TreeSnapshot{}, err
		}
		file, exists, err := readRawFile(rootFD, name, spec.MaxFileBytes)
		if err != nil {
			return TreeSnapshot{}, err
		}
		if !exists {
			continue
		}
		if file.Mode == "120000" {
			if err := scope.ValidateSymlinkTarget(name, string(file.Content)); err != nil {
				return TreeSnapshot{}, err
			}
		}
		object, err := repository.WriteBlob(ctx, file.Content)
		if err != nil {
			return TreeSnapshot{}, err
		}
		files = append(files, file)
		entries = append(entries, TreeEntry{Path: name, Mode: file.Mode, ObjectID: object})
	}
	tree, err := repository.BuildTree(ctx, entries)
	if err != nil {
		return TreeSnapshot{}, err
	}
	if err := repository.CheckCheckoutIdentity(ctx, identity); err != nil {
		return TreeSnapshot{}, err
	}
	return TreeSnapshot{Tree: tree, Identity: identity, Files: files}, nil
}

// Git's untracked listing omits special files and may normalize path spelling.
// Read directory entries without following links, then use Git only for ignore
// decisions. The base tree independently owns every tracked path.
func (repository *Repository) sourceInventory(ctx context.Context, excluded func(string) bool) ([]string, error) {
	ignoredListing, err := runBytes(ctx, repository.root, maxTreeListingBytes, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return nil, err
	}
	ignoredDirectories := map[string]bool{}
	for _, value := range bytes.Split(ignoredListing, []byte{0}) {
		if strings.HasSuffix(string(value), "/") {
			ignoredDirectories[strings.TrimSuffix(string(value), "/")] = true
		}
	}
	var names []string
	var input bytes.Buffer
	err = filepath.WalkDir(repository.root, func(absolute string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if absolute == repository.root {
			return nil
		}
		relative, err := filepath.Rel(repository.root, absolute)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if excluded(name) || (entry.IsDir() && ignoredDirectories[name]) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(entry.Name(), ".git") {
			return fmt.Errorf("nested Git repository is unsupported: %s", name)
		}
		if entry.IsDir() {
			return nil
		}
		names = append(names, name)
		input.WriteString(name)
		input.WriteByte(0)
		if input.Len() > maxTreeListingBytes {
			return errors.New("source path inventory exceeds limit")
		}
		return nil
	})
	if err != nil || len(names) == 0 {
		return names, err
	}
	command := gitCommand(ctx, repository.root, "check-ignore", "--no-index", "-z", "--stdin")
	command.Stdin = &input
	output := boundedBuffer{limit: maxTreeListingBytes}
	var stderr limitedBuffer
	command.Stdout, command.Stderr = &output, &stderr
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, fmt.Errorf("inspect ignored source paths: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
	}
	if output.exceeded {
		return nil, errors.New("ignored source path inventory exceeds limit")
	}
	ignored := map[string]bool{}
	for _, value := range bytes.Split(output.data.Bytes(), []byte{0}) {
		ignored[string(value)] = true
	}
	filtered := names[:0]
	for _, name := range names {
		if !ignored[name] {
			filtered = append(filtered, name)
		}
	}
	return filtered, nil
}

func (repository *Repository) CheckSnapshot(ctx context.Context, spec SnapshotSpec, identity CheckoutIdentity, expectedTree string) error {
	if !repository.validObjectID(expectedTree) {
		return errors.New("invalid expected source tree")
	}
	if err := repository.CheckCheckoutIdentity(ctx, identity); err != nil {
		return err
	}
	snapshot, err := repository.SnapshotTree(ctx, spec)
	if err != nil {
		return err
	}
	if snapshot.Identity != identity || snapshot.Tree != expectedTree {
		return fmt.Errorf("%w: observed tree %s, expected %s", ErrCheckoutChanged, snapshot.Tree, expectedTree)
	}
	return nil
}

func (repository *Repository) exclusions(paths []string) (func(string) bool, error) {
	excluded := []string{".git", ".xgoal"}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, errors.New("snapshot exclusions must be clean absolute paths")
		}
		if isWithin(path, repository.root) {
			return nil, errors.New("snapshot exclusion cannot contain the repository root")
		}
		if isWithin(repository.root, path) {
			relative, _ := filepath.Rel(repository.root, path)
			excluded = append(excluded, filepath.ToSlash(relative))
		}
	}
	return func(path string) bool {
		for _, prefix := range excluded {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return true
			}
		}
		return false
	}, nil
}

func readRawFile(rootFD int, path string, limit int64) (RawFile, bool, error) {
	parts := strings.Split(path, "/")
	parent := rootFD
	var owned []int
	defer func() {
		for _, fd := range owned {
			_ = unix.Close(fd)
		}
	}()
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(parent, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) {
			return RawFile{}, false, nil
		}
		if err != nil {
			return RawFile{}, false, err
		}
		owned = append(owned, next)
		parent = next
	}
	name := parts[len(parts)-1]
	var stat unix.Stat_t
	if err := unix.Fstatat(parent, name, &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return RawFile{}, false, nil
	} else if err != nil {
		return RawFile{}, false, err
	}
	kind := stat.Mode & unix.S_IFMT
	if kind == unix.S_IFDIR {
		return RawFile{}, false, nil
	}
	if kind == unix.S_IFLNK {
		buffer := make([]byte, 4096)
		n, err := unix.Readlinkat(parent, name, buffer)
		if err != nil {
			return RawFile{}, false, err
		}
		if n == len(buffer) || int64(n) > limit {
			return RawFile{}, false, fmt.Errorf("symlink exceeds snapshot limit: %s", path)
		}
		return RawFile{Path: path, Mode: "120000", Content: buffer[:n]}, true, nil
	}
	if kind != unix.S_IFREG {
		return RawFile{}, false, fmt.Errorf("unsupported source file type: %s", path)
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return RawFile{}, false, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return RawFile{}, false, err
	}
	if !before.Mode().IsRegular() || before.Size() > limit {
		return RawFile{}, false, fmt.Errorf("source file exceeds snapshot limit or changed type: %s", path)
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return RawFile{}, false, err
	}
	after, err := file.Stat()
	if err != nil {
		return RawFile{}, false, err
	}
	if int64(len(content)) != before.Size() || before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		return RawFile{}, false, fmt.Errorf("%w: source file changed while reading: %s", ErrCheckoutChanged, path)
	}
	mode := "100644"
	if before.Mode().Perm()&0111 != 0 {
		mode = "100755"
	}
	return RawFile{Path: path, Mode: mode, Content: content}, true, nil
}

func readMetadataFile(path string, limit int64) ([]byte, bool, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, false, fmt.Errorf("unsafe or oversized Git metadata: %s", path)
	}
	value, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(value)) != info.Size() {
		return nil, false, ErrCheckoutChanged
	}
	return value, true, nil
}
func indexFingerprint(value []byte, present bool) string {
	hash := sha256.New()
	if present {
		_, _ = hash.Write([]byte{1})
	} else {
		_, _ = hash.Write([]byte{0})
	}
	_, _ = hash.Write(value)
	return hex.EncodeToString(hash.Sum(nil))
}
func hexHash(value string, size int) bool {
	if len(value) != size {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}
