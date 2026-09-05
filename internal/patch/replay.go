package patch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/scope"
)

var (
	ErrStaleOrConflict = errors.New("patch is stale or conflicts with integration tree")
	ErrUnsafeReplay    = errors.New("unsafe patch replay")
)

type ReplaySpec struct {
	ExecutionPath     string
	Identity          gitrepo.CheckoutIdentity
	ExcludePaths      []string
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

// Replay validates and reconstructs the Bundle exclusively in Git objects.
// Current source files are only read; they must already match the candidate.
func Replay(ctx context.Context, repository *gitrepo.Repository, spec ReplaySpec) (ReplayResult, error) {
	if repository == nil || spec.IntegrationCommit == "" || spec.IntegrationTree == "" || spec.MaxFileBytes <= 0 {
		return ReplayResult{}, errors.New("invalid patch replay spec")
	}
	if err := spec.Identity.Validate(); err != nil {
		return ReplayResult{}, err
	}
	if err := spec.Captured.Bundle.Validate(); err != nil {
		return ReplayResult{}, fmt.Errorf("validate patch bundle: %w", err)
	}
	if err := validateCapturedObjects(spec.Captured); err != nil {
		return ReplayResult{}, err
	}
	identity, err := repository.InspectCheckout(ctx, spec.ExecutionPath)
	if err != nil {
		return ReplayResult{}, err
	}
	if identity != spec.Identity {
		return ReplayResult{}, gitrepo.ErrCheckoutChanged
	}
	integration, err := repository.ResolveRevision(ctx, spec.IntegrationCommit)
	if err != nil {
		return ReplayResult{}, err
	}
	if integration.Commit != spec.IntegrationCommit || integration.Tree != spec.IntegrationTree {
		return ReplayResult{}, fmt.Errorf("%w: integration commit/tree differs", ErrStaleOrConflict)
	}
	snapshotSpec := gitrepo.SnapshotSpec{BaseTree: spec.IntegrationTree, ExcludePaths: spec.ExcludePaths, MaxFileBytes: spec.MaxFileBytes}
	current, err := repository.SnapshotTree(ctx, snapshotSpec)
	if err != nil {
		return ReplayResult{}, err
	}
	if current.Identity != spec.Identity {
		return ReplayResult{}, gitrepo.ErrCheckoutChanged
	}
	actualPaths := map[string]string{}
	for _, file := range current.Files {
		normalized, err := scope.NormalizeRepositoryPath(file.Path)
		if err != nil {
			return ReplayResult{}, err
		}
		actualPaths[normalized] = file.Path
	}
	entries, err := reconstructEntries(ctx, repository, spec, actualPaths)
	if err != nil {
		return ReplayResult{}, err
	}
	candidate, err := repository.BuildTree(ctx, entries)
	if err != nil {
		return ReplayResult{}, fmt.Errorf("%w: %w", ErrUnsafeReplay, err)
	}
	if candidate == spec.IntegrationTree {
		return ReplayResult{}, errors.New("replayed patch did not change the integration tree")
	}
	if candidate != current.Tree {
		return ReplayResult{}, fmt.Errorf("%w: candidate %s differs from current source %s", gitrepo.ErrCheckoutChanged, candidate, current.Tree)
	}
	if err := repository.CheckSnapshot(ctx, snapshotSpec, spec.Identity, candidate); err != nil {
		return ReplayResult{}, err
	}
	return ReplayResult{IntegrationCommit: spec.IntegrationCommit, IntegrationTree: spec.IntegrationTree, CandidateTree: candidate}, nil
}

func reconstructEntries(ctx context.Context, repository *gitrepo.Repository, spec ReplaySpec, actualPaths map[string]string) ([]gitrepo.TreeEntry, error) {
	changed := []string{}
	for _, entry := range spec.Captured.Bundle.Entries {
		changed = append(changed, entry.Path)
		if entry.PathBefore != "" {
			changed = append(changed, entry.PathBefore)
		}
	}
	if _, err := scope.CanonicalizePaths(changed); err != nil {
		return nil, err
	}
	if err := spec.Policy.CheckWrite(changed); err != nil {
		return nil, err
	}
	baseEntries, err := repository.ListTree(ctx, spec.IntegrationTree)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(baseEntries))
	for _, entry := range baseEntries {
		paths = append(paths, entry.Path)
	}
	if _, err := scope.CanonicalizePaths(paths); err != nil {
		return nil, err
	}
	before := map[string]gitrepo.TreeEntry{}
	after := map[string]gitrepo.TreeEntry{}
	for _, entry := range baseEntries {
		key, err := scope.NormalizeRepositoryPath(entry.Path)
		if err != nil {
			return nil, err
		}
		before[key] = entry
		after[key] = entry
		if entry.Mode == "120000" {
			content, err := repository.ReadBlob(ctx, entry.ObjectID, spec.MaxFileBytes)
			if err != nil {
				return nil, err
			}
			if err := scope.ValidateSymlinkTarget(key, string(content)); err != nil {
				return nil, fmt.Errorf("%w: %w", ErrUnsafeReplay, err)
			}
		}
	}
	hashCache := map[string]string{}
	for _, entry := range spec.Captured.Bundle.Entries {
		target, exists := before[entry.Path]
		targetPath := entry.Path
		if raw, ok := actualPaths[entry.Path]; ok {
			targetPath = raw
		}
		switch entry.Kind {
		case protocol.PatchAdded:
			if exists {
				return nil, conflict(entry.Path, "added path already exists")
			}
		case protocol.PatchModified, protocol.PatchDeleted:
			if !exists {
				return nil, conflict(entry.Path, "before path missing")
			}
			if err := verifyBefore(ctx, repository, target, entry.ModeBefore, entry.ContentHashBefore, spec.MaxFileBytes, hashCache); err != nil {
				return nil, conflict(entry.Path, err.Error())
			}
			targetPath = target.Path
			if entry.Kind == protocol.PatchDeleted {
				delete(after, entry.Path)
				continue
			}
		case protocol.PatchRenamed:
			if exists {
				return nil, conflict(entry.Path, "rename target already exists")
			}
			source, ok := before[entry.PathBefore]
			if !ok {
				return nil, conflict(entry.PathBefore, "rename source missing")
			}
			if err := verifyBefore(ctx, repository, source, entry.ModeBefore, entry.ContentHashBefore, spec.MaxFileBytes, hashCache); err != nil {
				return nil, conflict(entry.PathBefore, err.Error())
			}
			delete(after, entry.PathBefore)
		}
		content := spec.Captured.Objects[entry.ContentHashAfter]
		if int64(len(content)) > spec.MaxFileBytes {
			return nil, errors.New("patch object exceeds replay file limit")
		}
		if entry.ModeAfter == "120000" {
			if err := scope.ValidateSymlinkTarget(entry.Path, string(content)); err != nil {
				return nil, fmt.Errorf("%w: %w", ErrUnsafeReplay, err)
			}
		}
		object, err := repository.WriteBlob(ctx, content)
		if err != nil {
			return nil, err
		}
		after[entry.Path] = gitrepo.TreeEntry{Path: targetPath, Mode: entry.ModeAfter, ObjectID: object}
	}
	entries := make([]gitrepo.TreeEntry, 0, len(after))
	for _, entry := range after {
		entries = append(entries, entry)
	}
	return entries, nil
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

func conflict(repositoryPath, reason string) error {
	return fmt.Errorf("%w: %q: %s", ErrStaleOrConflict, repositoryPath, reason)
}
