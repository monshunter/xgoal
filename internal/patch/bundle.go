package patch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/scope"
)

type CaptureSpec struct {
	AttemptID     string
	ExecutionPath string
	Identity      gitrepo.CheckoutIdentity
	ExcludePaths  []string
	BaseCommit    string
	BaseTree      string
	MaxFileBytes  int64
}

type Captured struct {
	Bundle  protocol.PatchBundle
	Objects map[string][]byte
}

type fileState struct {
	path    string
	mode    string
	content []byte
	hash    string
}

func Capture(ctx context.Context, repository *gitrepo.Repository, spec CaptureSpec) (Captured, error) {
	if repository == nil || !validComponent(spec.AttemptID) || spec.MaxFileBytes <= 0 || spec.BaseCommit == "" || spec.BaseTree == "" {
		return Captured{}, errors.New("invalid patch capture spec")
	}
	identity, err := repository.InspectCheckout(ctx, spec.ExecutionPath)
	if err != nil {
		return Captured{}, err
	}
	if err := spec.Identity.Validate(); err != nil {
		return Captured{}, err
	}
	if identity != spec.Identity {
		return Captured{}, gitrepo.ErrCheckoutChanged
	}
	base, err := repository.ResolveRevision(ctx, spec.BaseCommit)
	if err != nil {
		return Captured{}, err
	}
	if base.Commit != spec.BaseCommit || base.Tree != spec.BaseTree {
		return Captured{}, errors.New("patch capture base commit/tree mismatch")
	}
	baseFiles, err := readBaseFiles(ctx, repository, base.Commit, spec.MaxFileBytes)
	if err != nil {
		return Captured{}, err
	}
	snapshot, err := repository.SnapshotTree(ctx, gitrepo.SnapshotSpec{BaseTree: spec.BaseTree, ExcludePaths: spec.ExcludePaths, MaxFileBytes: spec.MaxFileBytes})
	if err != nil {
		return Captured{}, err
	}
	if snapshot.Identity != spec.Identity {
		return Captured{}, gitrepo.ErrCheckoutChanged
	}
	currentFiles := make(map[string]fileState, len(snapshot.Files))
	for _, file := range snapshot.Files {
		canonicalPath, err := scope.NormalizeRepositoryPath(file.Path)
		if err != nil {
			return Captured{}, err
		}
		currentFiles[canonicalPath] = newFileState(canonicalPath, file.Mode, file.Content)
	}
	entries, objects, err := compareFiles(baseFiles, currentFiles)
	if err != nil {
		return Captured{}, err
	}
	if len(entries) == 0 {
		return Captured{}, errors.New("workspace contains no changes")
	}
	patchObjects := make([]protocol.PatchObject, 0, len(objects))
	for hash, content := range objects {
		patchObjects = append(patchObjects, protocol.PatchObject{Ref: "objects/sha256/" + hash, Hash: hash, Length: int64(len(content))})
	}
	sort.Slice(patchObjects, func(i, j int) bool { return patchObjects[i].Ref < patchObjects[j].Ref })
	bundle, err := (protocol.PatchBundle{
		ProtocolVersion: protocol.PatchBundleVersion,
		AttemptID:       spec.AttemptID,
		BaseCommit:      base.Commit,
		BaseTree:        base.Tree,
		Entries:         entries,
		Objects:         patchObjects,
	}).Seal()
	if err != nil {
		return Captured{}, err
	}
	return Captured{Bundle: bundle, Objects: objects}, nil
}

func readBaseFiles(ctx context.Context, repository *gitrepo.Repository, commit string, maxFileBytes int64) (map[string]fileState, error) {
	entries, err := repository.ListTree(ctx, commit)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	canonicalPaths, err := scope.CanonicalizePaths(paths)
	if err != nil {
		return nil, fmt.Errorf("base tree paths: %w", err)
	}
	rawToCanonical := reversePaths(canonicalPaths)
	files := make(map[string]fileState, len(entries))
	for _, entry := range entries {
		canonicalPath := rawToCanonical[entry.Path]
		content, err := repository.ReadBlob(ctx, entry.ObjectID, maxFileBytes)
		if err != nil {
			return nil, err
		}
		if entry.Mode == "120000" {
			if err := scope.ValidateSymlinkTarget(canonicalPath, string(content)); err != nil {
				return nil, fmt.Errorf("base symlink %q: %w", entry.Path, err)
			}
		}
		files[canonicalPath] = newFileState(canonicalPath, entry.Mode, content)
	}
	return files, nil
}

func reversePaths(canonical map[string]string) map[string]string {
	reversed := make(map[string]string, len(canonical))
	for canonicalPath, rawPath := range canonical {
		reversed[rawPath] = canonicalPath
	}
	return reversed
}

func compareFiles(base, current map[string]fileState) ([]protocol.PatchEntry, map[string][]byte, error) {
	var entries []protocol.PatchEntry
	objects := make(map[string][]byte)
	deleted := make(map[string]protocol.PatchEntry)
	added := make(map[string]protocol.PatchEntry)
	for filename, before := range base {
		after, exists := current[filename]
		if !exists {
			deleted[filename] = protocol.PatchEntry{Path: filename, Kind: protocol.PatchDeleted, ModeBefore: before.mode, ContentHashBefore: before.hash}
			continue
		}
		if before.mode == after.mode && before.hash == after.hash {
			continue
		}
		objectRef := "objects/sha256/" + after.hash
		entries = append(entries, protocol.PatchEntry{
			Path: filename, Kind: protocol.PatchModified,
			ModeBefore: before.mode, ModeAfter: after.mode,
			ContentHashBefore: before.hash, ContentHashAfter: after.hash, ObjectRef: objectRef,
		})
		objects[after.hash] = append([]byte(nil), after.content...)
	}
	for filename, after := range current {
		if _, exists := base[filename]; exists {
			continue
		}
		added[filename] = protocol.PatchEntry{
			Path: filename, Kind: protocol.PatchAdded, ModeAfter: after.mode,
			ContentHashAfter: after.hash, ObjectRef: "objects/sha256/" + after.hash,
		}
	}
	type identity struct{ mode, hash string }
	deletesByIdentity := make(map[identity][]string)
	addsByIdentity := make(map[identity][]string)
	for filename, entry := range deleted {
		key := identity{entry.ModeBefore, entry.ContentHashBefore}
		deletesByIdentity[key] = append(deletesByIdentity[key], filename)
	}
	for filename, entry := range added {
		key := identity{entry.ModeAfter, entry.ContentHashAfter}
		addsByIdentity[key] = append(addsByIdentity[key], filename)
	}
	for key, deletedPaths := range deletesByIdentity {
		addedPaths := addsByIdentity[key]
		if len(deletedPaths) != 1 || len(addedPaths) != 1 {
			continue
		}
		oldPath, newPath := deletedPaths[0], addedPaths[0]
		before, after := base[oldPath], current[newPath]
		entries = append(entries, protocol.PatchEntry{
			Path: newPath, PathBefore: oldPath, Kind: protocol.PatchRenamed,
			ModeBefore: before.mode, ModeAfter: after.mode,
			ContentHashBefore: before.hash, ContentHashAfter: after.hash,
			ObjectRef: "objects/sha256/" + after.hash,
		})
		objects[after.hash] = append([]byte(nil), after.content...)
		delete(deleted, oldPath)
		delete(added, newPath)
	}
	for _, entry := range deleted {
		entries = append(entries, entry)
	}
	for filename, entry := range added {
		entries = append(entries, entry)
		after := current[filename]
		objects[after.hash] = append([]byte(nil), after.content...)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, objects, nil
}

func newFileState(filename, mode string, content []byte) fileState {
	hash := sha256.Sum256(content)
	return fileState{path: filename, mode: mode, content: append([]byte(nil), content...), hash: hex.EncodeToString(hash[:])}
}

func validComponent(value string) bool {
	return value != "" && value != "." && value != ".." && utf8.ValidString(value) && !strings.ContainsAny(value, "/\\\r\n\x00")
}
