package patch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/protocol"
)

const maxManifestBytes = 8 << 20

type Store struct {
	root string
}

func NewStore(runtimeRoot string) (*Store, error) {
	if !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot || strings.ContainsAny(runtimeRoot, "\r\n\x00") {
		return nil, errors.New("runtime root must be a clean absolute path")
	}
	if err := ensurePrivateDirectory(runtimeRoot); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(runtimeRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve patch runtime root: %w", err)
	}
	root := filepath.Join(resolved, "patches")
	if err := ensurePrivateDirectory(root); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (store *Store) Save(captured Captured) (string, error) {
	if !validComponent(captured.Bundle.AttemptID) {
		return "", errors.New("invalid bundle attempt id")
	}
	if err := captured.Bundle.Validate(); err != nil {
		return "", err
	}
	if err := validateCapturedObjects(captured); err != nil {
		return "", err
	}
	finalPath := filepath.Join(store.root, captured.Bundle.AttemptID)
	if _, err := os.Lstat(finalPath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return "", fmt.Errorf("patch bundle %q already exists", captured.Bundle.AttemptID)
		}
		return "", fmt.Errorf("inspect patch bundle target: %w", err)
	}
	temporaryPath, err := os.MkdirTemp(store.root, ".bundle-"+captured.Bundle.AttemptID+"-")
	if err != nil {
		return "", fmt.Errorf("create temporary patch bundle: %w", err)
	}
	keepTemporary := true
	defer func() {
		if keepTemporary {
			_ = os.RemoveAll(temporaryPath)
		}
	}()
	if err := os.Chmod(temporaryPath, 0o700); err != nil {
		return "", err
	}
	objectDirectory := filepath.Join(temporaryPath, "objects", "sha256")
	if err := os.MkdirAll(objectDirectory, 0o700); err != nil {
		return "", fmt.Errorf("create patch object directory: %w", err)
	}
	for _, object := range captured.Bundle.Objects {
		if err := writeSyncedFile(filepath.Join(objectDirectory, object.Hash), captured.Objects[object.Hash]); err != nil {
			return "", err
		}
	}
	manifest, err := canonical.Marshal(captured.Bundle)
	if err != nil {
		return "", fmt.Errorf("encode patch manifest: %w", err)
	}
	if err := writeSyncedFile(filepath.Join(temporaryPath, "manifest.json"), manifest); err != nil {
		return "", err
	}
	for _, directory := range []string{objectDirectory, filepath.Dir(objectDirectory), temporaryPath} {
		if err := syncDirectory(directory); err != nil {
			return "", err
		}
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return "", fmt.Errorf("publish patch bundle: %w", err)
	}
	keepTemporary = false
	if err := syncDirectory(store.root); err != nil {
		return "", err
	}
	return finalPath, nil
}

func (store *Store) Load(attemptID string) (Captured, error) {
	if !validComponent(attemptID) {
		return Captured{}, errors.New("invalid bundle attempt id")
	}
	bundlePath := filepath.Join(store.root, attemptID)
	info, err := os.Lstat(bundlePath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Captured{}, errors.New("patch bundle directory is missing or unsafe")
	}
	manifestPath := filepath.Join(bundlePath, "manifest.json")
	manifest, err := readLimitedFile(manifestPath, maxManifestBytes)
	if err != nil {
		return Captured{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(manifest))
	decoder.DisallowUnknownFields()
	var bundle protocol.PatchBundle
	if err := decoder.Decode(&bundle); err != nil {
		return Captured{}, fmt.Errorf("decode patch manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Captured{}, errors.New("patch manifest has trailing data")
	}
	if bundle.AttemptID != attemptID {
		return Captured{}, errors.New("patch manifest attempt id mismatch")
	}
	if err := bundle.Validate(); err != nil {
		return Captured{}, err
	}
	if err := validateBundleLayout(bundlePath, bundle); err != nil {
		return Captured{}, err
	}
	objects := make(map[string][]byte, len(bundle.Objects))
	for _, object := range bundle.Objects {
		content, err := readLimitedFile(filepath.Join(bundlePath, filepath.FromSlash(object.Ref)), object.Length)
		if err != nil {
			return Captured{}, err
		}
		hash := sha256.Sum256(content)
		if int64(len(content)) != object.Length || hex.EncodeToString(hash[:]) != object.Hash {
			return Captured{}, fmt.Errorf("patch object %s length or hash mismatch", object.Hash)
		}
		objects[object.Hash] = content
	}
	return Captured{Bundle: bundle, Objects: objects}, nil
}

func validateCapturedObjects(captured Captured) error {
	if len(captured.Objects) != len(captured.Bundle.Objects) {
		return errors.New("captured object set size does not match bundle")
	}
	for _, object := range captured.Bundle.Objects {
		content, exists := captured.Objects[object.Hash]
		if !exists {
			return fmt.Errorf("captured object %s is missing", object.Hash)
		}
		hash := sha256.Sum256(content)
		if int64(len(content)) != object.Length || hex.EncodeToString(hash[:]) != object.Hash {
			return fmt.Errorf("captured object %s length or hash mismatch", object.Hash)
		}
	}
	return nil
}

func validateBundleLayout(bundlePath string, bundle protocol.PatchBundle) error {
	rootEntries, err := os.ReadDir(bundlePath)
	if err != nil {
		return err
	}
	for _, entry := range rootEntries {
		if entry.Name() != "manifest.json" && entry.Name() != "objects" && entry.Name() != "review.diff" {
			return fmt.Errorf("unexpected patch bundle entry %q", entry.Name())
		}
		if entry.Name() == "objects" && !entry.IsDir() {
			return errors.New("patch objects entry is not a directory")
		}
		if entry.Name() != "objects" && entry.Type()&os.ModeSymlink != 0 {
			return errors.New("patch bundle contains a linked projection")
		}
	}
	objectParents, err := os.ReadDir(filepath.Join(bundlePath, "objects"))
	if err != nil || len(objectParents) != 1 || objectParents[0].Name() != "sha256" || !objectParents[0].IsDir() {
		return errors.New("patch objects directory contains an unexpected layout")
	}
	objectDirectory := filepath.Join(bundlePath, "objects", "sha256")
	entries, err := os.ReadDir(objectDirectory)
	if err != nil {
		return fmt.Errorf("read patch object directory: %w", err)
	}
	want := make([]string, 0, len(bundle.Objects))
	for _, object := range bundle.Objects {
		want = append(want, object.Hash)
	}
	sort.Strings(want)
	if len(entries) != len(want) {
		return errors.New("patch object directory contains missing or extra entries")
	}
	for index, entry := range entries {
		info, err := entry.Info()
		if err != nil || entry.Name() != want[index] || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
			return errors.New("patch object directory contains an unsafe or unexpected entry")
		}
	}
	return nil
}

func readLimitedFile(path string, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, errors.New("negative file read limit")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || pathInfo.Mode().Perm() != 0o600 || pathInfo.Size() > limit {
		return nil, errors.New("immutable patch file is unsafe or exceeds its limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open immutable patch file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != pathInfo.Size() || info.Size() > limit {
		return nil, errors.New("immutable patch file is unsafe or exceeds its limit")
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(content)) > limit {
		return nil, errors.New("read immutable patch file exceeded its limit")
	}
	return content, nil
}

func writeSyncedFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create immutable patch file: %w", err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write immutable patch file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync immutable patch file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close immutable patch file: %w", err)
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("patch directory is missing or unsafe")
	}
	return os.Chmod(path, 0o700)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
