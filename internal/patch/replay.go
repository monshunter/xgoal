package patch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/scope"
)

var (
	ErrStaleOrConflict = errors.New("patch is stale or conflicts with integration tree")
	ErrUnsafeReplay    = errors.New("unsafe patch replay")
)

type ReplaySpec struct {
	WorktreePath      string
	IntegrationCommit string
	IntegrationTree   string
	Captured          Captured
	Policy            scope.Policy
	MaxFileBytes      int64
}

type ReplayResult struct {
	IntegrationCommit string
	IntegrationTree   string
	CandidateTree     string
}

type replayOperation struct {
	entry                 protocol.PatchEntry
	path                  string
	pathBefore            string
	filesystemPath        string
	beforePath            string
	content               []byte
	removeTargetDirectory bool
}

func Replay(ctx context.Context, repository *gitrepo.Repository, spec ReplaySpec) (ReplayResult, error) {
	if repository == nil || spec.IntegrationCommit == "" || spec.IntegrationTree == "" || spec.MaxFileBytes <= 0 {
		return ReplayResult{}, errors.New("invalid patch replay spec")
	}
	if err := spec.Captured.Bundle.Validate(); err != nil {
		return ReplayResult{}, fmt.Errorf("validate patch bundle: %w", err)
	}
	if err := validateCapturedObjects(spec.Captured); err != nil {
		return ReplayResult{}, err
	}
	worktree, err := repository.EnsureCleanWorktree(ctx, spec.WorktreePath)
	if err != nil {
		return ReplayResult{}, err
	}
	if worktree.HeadCommit != spec.IntegrationCommit || worktree.HeadTree != spec.IntegrationTree {
		return ReplayResult{}, fmt.Errorf("%w: validation worktree is at %s/%s, want %s/%s", ErrStaleOrConflict,
			worktree.HeadCommit, worktree.HeadTree, spec.IntegrationCommit, spec.IntegrationTree)
	}
	resolved, err := repository.ResolveRevision(ctx, spec.IntegrationCommit)
	if err != nil {
		return ReplayResult{}, err
	}
	if resolved.Commit != spec.IntegrationCommit || resolved.Tree != spec.IntegrationTree {
		return ReplayResult{}, fmt.Errorf("%w: integration commit/tree binding changed", ErrStaleOrConflict)
	}
	operations, err := preflightReplay(ctx, repository, worktree.Path, resolved.Commit, spec)
	if err != nil {
		return ReplayResult{}, err
	}
	if err := applyReplay(worktree.Path, operations); err != nil {
		return ReplayResult{}, err
	}
	candidateTree, err := repository.IndexAndWriteTree(ctx, worktree.Path)
	if err != nil {
		return ReplayResult{}, err
	}
	if candidateTree == spec.IntegrationTree {
		return ReplayResult{}, errors.New("replayed patch did not change the integration tree")
	}
	return ReplayResult{
		IntegrationCommit: spec.IntegrationCommit,
		IntegrationTree:   spec.IntegrationTree,
		CandidateTree:     candidateTree,
	}, nil
}

func preflightReplay(ctx context.Context, repository *gitrepo.Repository, worktreeRoot, integrationCommit string, spec ReplaySpec) ([]replayOperation, error) {
	changedPaths := make([]string, 0, len(spec.Captured.Bundle.Entries)*2)
	for _, entry := range spec.Captured.Bundle.Entries {
		changedPaths = append(changedPaths, entry.Path)
		if entry.PathBefore != "" {
			changedPaths = append(changedPaths, entry.PathBefore)
		}
	}
	if _, err := scope.CanonicalizePaths(changedPaths); err != nil {
		return nil, fmt.Errorf("patch paths: %w", err)
	}
	if err := spec.Policy.CheckWrite(changedPaths); err != nil {
		return nil, err
	}
	treeEntries, err := repository.ListTree(ctx, integrationCommit)
	if err != nil {
		return nil, err
	}
	rawPaths := make([]string, 0, len(treeEntries))
	for _, entry := range treeEntries {
		rawPaths = append(rawPaths, entry.Path)
	}
	canonicalPaths, err := scope.CanonicalizePaths(rawPaths)
	if err != nil {
		return nil, fmt.Errorf("integration tree paths: %w", err)
	}
	treeByPath := make(map[string]gitrepo.TreeEntry, len(treeEntries))
	for _, entry := range treeEntries {
		canonicalPath, err := scope.NormalizeRepositoryPath(entry.Path)
		if err != nil {
			return nil, err
		}
		treeByPath[canonicalPath] = entry
		if entry.Mode == "120000" {
			content, err := repository.ReadBlob(ctx, entry.ObjectID, spec.MaxFileBytes)
			if err != nil {
				return nil, err
			}
			if err := scope.ValidateSymlinkTarget(canonicalPath, string(content)); err != nil {
				return nil, fmt.Errorf("integration symlink %q: %w", entry.Path, err)
			}
		}
	}
	_ = canonicalPaths
	plannedRemovals := make(map[string]struct{}, len(spec.Captured.Bundle.Entries))
	for _, entry := range spec.Captured.Bundle.Entries {
		switch entry.Kind {
		case protocol.PatchDeleted:
			plannedRemovals[entry.Path] = struct{}{}
		case protocol.PatchRenamed:
			plannedRemovals[entry.PathBefore] = struct{}{}
		}
	}

	hashCache := make(map[string]string)
	operations := make([]replayOperation, 0, len(spec.Captured.Bundle.Entries))
	for _, entry := range spec.Captured.Bundle.Entries {
		operation := replayOperation{entry: entry, path: entry.Path, pathBefore: entry.PathBefore}
		current, targetExists := treeByPath[entry.Path]
		switch entry.Kind {
		case protocol.PatchAdded:
			if targetExists {
				return nil, conflict(entry.Path, "added path already exists")
			}
		case protocol.PatchModified, protocol.PatchDeleted:
			if !targetExists {
				return nil, conflict(entry.Path, "before path is missing")
			}
			if err := verifyBefore(ctx, repository, current, entry.ModeBefore, entry.ContentHashBefore, spec.MaxFileBytes, hashCache); err != nil {
				return nil, conflict(entry.Path, err.Error())
			}
			operation.path = current.Path
		case protocol.PatchRenamed:
			if targetExists {
				return nil, conflict(entry.Path, "rename target already exists")
			}
			before, exists := treeByPath[entry.PathBefore]
			if !exists {
				return nil, conflict(entry.PathBefore, "rename source is missing")
			}
			if err := verifyBefore(ctx, repository, before, entry.ModeBefore, entry.ContentHashBefore, spec.MaxFileBytes, hashCache); err != nil {
				return nil, conflict(entry.PathBefore, err.Error())
			}
			operation.pathBefore = before.Path
		}
		if entry.Kind != protocol.PatchDeleted {
			operation.content = append([]byte(nil), spec.Captured.Objects[entry.ContentHashAfter]...)
			if entry.ModeAfter == "120000" {
				if err := scope.ValidateSymlinkTarget(entry.Path, string(operation.content)); err != nil {
					return nil, fmt.Errorf("%w: symlink %q: %v", ErrUnsafeReplay, entry.Path, err)
				}
			}
		}
		operation.filesystemPath = filepath.Join(worktreeRoot, filepath.FromSlash(operation.path))
		if operation.pathBefore != "" {
			operation.beforePath = filepath.Join(worktreeRoot, filepath.FromSlash(operation.pathBefore))
		}
		if err := preflightFilesystemOperation(worktreeRoot, &operation, treeByPath, plannedRemovals); err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func verifyBefore(ctx context.Context, repository *gitrepo.Repository, current gitrepo.TreeEntry, mode, contentHash string, maxFileBytes int64, cache map[string]string) error {
	if current.Mode != mode {
		return fmt.Errorf("mode is %s, want %s", current.Mode, mode)
	}
	hash, exists := cache[current.ObjectID]
	if !exists {
		content, err := repository.ReadBlob(ctx, current.ObjectID, maxFileBytes)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		hash = hex.EncodeToString(digest[:])
		cache[current.ObjectID] = hash
	}
	if hash != contentHash {
		return fmt.Errorf("content hash is %s, want %s", hash, contentHash)
	}
	return nil
}

func preflightFilesystemOperation(root string, operation *replayOperation, treeByPath map[string]gitrepo.TreeEntry, plannedRemovals map[string]struct{}) error {
	for _, candidate := range []string{operation.filesystemPath, operation.beforePath} {
		if candidate == "" {
			continue
		}
		if err := ensureSafeParentsForPreflight(root, candidate, plannedRemovals); err != nil {
			return fmt.Errorf("%w: %v", ErrUnsafeReplay, err)
		}
	}
	if operation.entry.Kind == protocol.PatchAdded || operation.entry.Kind == protocol.PatchRenamed {
		info, err := os.Lstat(operation.filesystemPath)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil && hasPlannedParentRemoval(root, operation.filesystemPath, plannedRemovals):
		case err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && directoryFullyRemoved(operation.entry.Path, treeByPath, plannedRemovals):
			operation.removeTargetDirectory = true
		default:
			return conflict(operation.entry.Path, "target exists in validation filesystem")
		}
	}
	for _, candidate := range []string{operation.filesystemPath, operation.beforePath} {
		if candidate == "" || (candidate == operation.filesystemPath && (operation.entry.Kind == protocol.PatchAdded || operation.entry.Kind == protocol.PatchRenamed)) {
			continue
		}
		info, err := os.Lstat(candidate)
		if err != nil || info.IsDir() {
			return conflict(operation.entry.Path, "before file is missing or changed in validation filesystem")
		}
	}
	return nil
}

func hasPlannedParentRemoval(root, filename string, plannedRemovals map[string]struct{}) bool {
	relative, err := filepath.Rel(root, filename)
	if err != nil {
		return false
	}
	directory := filepath.Dir(relative)
	for directory != "." && directory != string(filepath.Separator) {
		if _, planned := plannedRemovals[filepath.ToSlash(directory)]; planned {
			return true
		}
		directory = filepath.Dir(directory)
	}
	return false
}

func ensureSafeParentsForPreflight(root, filename string, plannedRemovals map[string]struct{}) error {
	relative, err := filepath.Rel(root, filename)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("replay path escapes validation worktree")
	}
	directory := filepath.Dir(relative)
	if directory == "." {
		return nil
	}
	current := root
	for _, segment := range strings.Split(directory, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		currentRelative, relativeErr := filepath.Rel(root, current)
		_, planned := plannedRemovals[filepath.ToSlash(currentRelative)]
		if relativeErr == nil && planned {
			return nil
		}
		return fmt.Errorf("replay parent %q is missing, linked, or not a directory", current)
	}
	return nil
}

func directoryFullyRemoved(repositoryPath string, treeByPath map[string]gitrepo.TreeEntry, plannedRemovals map[string]struct{}) bool {
	prefix := repositoryPath + "/"
	found := false
	for treePath := range treeByPath {
		if !strings.HasPrefix(treePath, prefix) {
			continue
		}
		found = true
		if _, removed := plannedRemovals[treePath]; !removed {
			return false
		}
	}
	return found
}

func applyReplay(root string, operations []replayOperation) error {
	deletions := make([]string, 0, len(operations))
	writes := make([]replayOperation, 0, len(operations))
	for _, operation := range operations {
		switch operation.entry.Kind {
		case protocol.PatchDeleted:
			deletions = append(deletions, operation.filesystemPath)
		case protocol.PatchRenamed:
			deletions = append(deletions, operation.beforePath)
			writes = append(writes, operation)
		default:
			writes = append(writes, operation)
		}
	}
	sort.Strings(deletions)
	for _, filename := range deletions {
		if err := os.Remove(filename); err != nil {
			return fmt.Errorf("apply replay deletion %q: %w", filename, err)
		}
		if err := syncDirectory(filepath.Dir(filename)); err != nil {
			return err
		}
	}
	removals := make([]string, 0)
	for _, operation := range writes {
		if operation.removeTargetDirectory {
			removals = append(removals, operation.filesystemPath)
		}
	}
	sort.Slice(removals, func(i, j int) bool { return pathDepth(removals[i]) > pathDepth(removals[j]) })
	for _, directory := range removals {
		if err := removeEmptyDirectoryTree(root, directory); err != nil {
			return fmt.Errorf("apply replay directory replacement %q: %w", directory, err)
		}
	}
	sort.Slice(writes, func(i, j int) bool { return writes[i].entry.Path < writes[j].entry.Path })
	for _, operation := range writes {
		if err := ensureSafeParents(root, operation.filesystemPath, true); err != nil {
			return fmt.Errorf("%w: %v", ErrUnsafeReplay, err)
		}
		if err := replacePath(operation.filesystemPath, operation.entry.ModeAfter, operation.content); err != nil {
			return fmt.Errorf("apply replay write %q: %w", operation.entry.Path, err)
		}
	}
	return nil
}

func removeEmptyDirectoryTree(root, directory string) error {
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("directory replacement escapes validation worktree")
	}
	directories := make([]string, 0)
	err = filepath.WalkDir(directory, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("directory replacement contains an undeclared file or link")
		}
		directories = append(directories, current)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool { return pathDepth(directories[i]) > pathDepth(directories[j]) })
	for _, current := range directories {
		if err := os.Remove(current); err != nil {
			return err
		}
	}
	return syncDirectory(filepath.Dir(directory))
}

func pathDepth(value string) int {
	return strings.Count(filepath.Clean(value), string(filepath.Separator))
}

func ensureSafeParents(root, filename string, create bool) error {
	relative, err := filepath.Rel(root, filename)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("replay path escapes validation worktree")
	}
	current := root
	segments := strings.Split(filepath.Dir(relative), string(filepath.Separator))
	if filepath.Dir(relative) == "." {
		return nil
	}
	for _, segment := range segments {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := os.Mkdir(current, 0o700); err != nil {
				return fmt.Errorf("create replay directory: %w", err)
			}
			continue
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("replay parent %q is missing, linked, or not a directory", current)
		}
	}
	return nil
}

func replacePath(filename, mode string, content []byte) error {
	directory := filepath.Dir(filename)
	temporary, err := os.CreateTemp(directory, ".xgoal-replay-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		_ = temporary.Close()
		if keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if mode == "120000" {
		if err := temporary.Close(); err != nil {
			return err
		}
		if err := os.Remove(temporaryPath); err != nil {
			return err
		}
		if err := os.Symlink(string(content), temporaryPath); err != nil {
			return err
		}
	} else {
		permissions := os.FileMode(0o644)
		if mode == "100755" {
			permissions = 0o755
		}
		if _, err := temporary.Write(content); err != nil {
			return err
		}
		if err := temporary.Chmod(permissions); err != nil {
			return err
		}
		if err := temporary.Sync(); err != nil {
			return err
		}
		if err := temporary.Close(); err != nil {
			return err
		}
	}
	if err := os.Rename(temporaryPath, filename); err != nil {
		return err
	}
	keepTemporary = false
	return syncDirectory(directory)
}

func conflict(repositoryPath, reason string) error {
	return fmt.Errorf("%w: %q: %s", ErrStaleOrConflict, repositoryPath, reason)
}
