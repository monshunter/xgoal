// Package scenario seals configured scenario outputs after deterministic
// assertions. It has no execution, orchestration or completion authority.
package scenario

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/scope"
)

const Version = "xgoal.scenario-artifact/v1"
const maxFileBytes = int64(32 << 20)
const maxTotalBytes = int64(128 << 20)

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	ProtocolVersion  string            `json:"protocol_version"`
	EvidenceID       string            `json:"evidence_id"`
	Scenario         config.Scenario   `json:"scenario"`
	GoalRevisionHash string            `json:"goal_revision_hash"`
	ConfigHash       string            `json:"config_hash"`
	TreeHash         string            `json:"tree_hash"`
	EnvironmentID    string            `json:"environment_id"`
	EnvironmentHash  string            `json:"environment_hash"`
	ReceiptHashes    map[string]string `json:"receipt_hashes"`
	Files            []File            `json:"files"`
	Hash             string            `json:"hash,omitempty"`
}

func DefinitionHash(spec config.Scenario) (string, error) {
	return canonical.Hash("scenario-definition", Version, spec)
}

func (m Manifest) Validate() error {
	if m.ProtocolVersion != Version || !safeID(m.EvidenceID) || !safeID(m.EnvironmentID) || !safeID(m.Scenario.ID) || len(m.Scenario.Validators) == 0 || len(m.Files) > 64 {
		return errors.New("invalid scenario identity")
	}
	for _, h := range []string{m.GoalRevisionHash, m.ConfigHash, m.EnvironmentHash, m.Hash} {
		if !digest(h, 64) {
			return errors.New("invalid scenario hash binding")
		}
	}
	if !digest(m.TreeHash, 40) && !digest(m.TreeHash, 64) {
		return errors.New("invalid scenario Tree binding")
	}
	if len(m.ReceiptHashes) != len(m.Scenario.Validators) || len(m.Files) != len(m.Scenario.ArtifactPaths) {
		return errors.New("scenario manifest has incomplete assertions or artifacts")
	}
	for _, id := range m.Scenario.Validators {
		if !digest(m.ReceiptHashes[id], 64) {
			return fmt.Errorf("scenario assertion %q has no bound receipt", id)
		}
	}
	var total int64
	for i, f := range m.Files {
		if !validPath(f.Path) || !slices.Contains(m.Scenario.ArtifactPaths, f.Path) || !digest(f.SHA256, 64) || f.Size < 0 || f.Size > maxFileBytes || (i > 0 && m.Files[i-1].Path >= f.Path) {
			return errors.New("invalid scenario file binding")
		}
		total += f.Size
	}
	if total > maxTotalBytes {
		return errors.New("scenario files exceed total limit")
	}
	hash, err := manifestHash(m)
	if err != nil || hash != m.Hash {
		return errors.New("scenario manifest hash mismatch")
	}
	return nil
}

// Seal writes immutable private copies and publishes the manifest last. Failed
// preparation leaves diagnostics, but no valid manifest or completed evidence.
func Seal(ctx context.Context, runtimeRoot, sourceDir string, m Manifest) (Manifest, error) {
	if !safeID(m.EvidenceID) || m.Hash != "" || len(m.Files) != 0 || len(m.Scenario.ArtifactPaths) > 64 {
		return Manifest{}, errors.New("invalid scenario seal input")
	}
	resolved, err := filepath.EvalSymlinks(sourceDir)
	if err != nil || resolved != sourceDir {
		return Manifest{}, errors.New("scenario source root is unavailable or linked")
	}
	source, err := os.OpenRoot(sourceDir)
	if err != nil {
		return Manifest{}, err
	}
	defer source.Close()
	directory, err := artifactDirectory(runtimeRoot, m.EvidenceID, true)
	if err != nil {
		return Manifest{}, err
	}
	paths := slices.Clone(m.Scenario.ArtifactPaths)
	sort.Strings(paths)
	var total int64
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		data, err := readRegular(source, path, min(maxFileBytes, maxTotalBytes-total), false)
		if err != nil {
			return Manifest{}, fmt.Errorf("scenario artifact %q: %w", path, err)
		}
		total += int64(len(data))
		hash := sha256.Sum256(data)
		m.Files = append(m.Files, File{Path: path, SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data))})
		destination := filepath.Join(directory, "files", filepath.FromSlash(path))
		if err := makePrivateParents(directory, filepath.Dir(destination)); err != nil {
			return Manifest{}, err
		}
		if err := writeImmutable(destination, data); err != nil {
			return Manifest{}, err
		}
	}
	m.ProtocolVersion = Version
	m.Hash, err = manifestHash(m)
	if err != nil {
		return Manifest{}, err
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	data, err := canonical.Marshal(m)
	if err != nil {
		return Manifest{}, err
	}
	if err := writeImmutable(filepath.Join(directory, "manifest.json"), data); err != nil {
		return Manifest{}, err
	}
	return Load(ctx, runtimeRoot, m.EvidenceID)
}

func Load(ctx context.Context, runtimeRoot, evidenceID string) (Manifest, error) {
	directory, err := artifactDirectory(runtimeRoot, evidenceID, false)
	if err != nil {
		return Manifest{}, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return Manifest{}, err
	}
	defer root.Close()
	data, err := readRegular(root, "manifest.json", 2<<20, true)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return Manifest{}, err
	}
	canonicalData, err := canonical.Marshal(m)
	if err != nil || !bytes.Equal(data, canonicalData) || m.EvidenceID != evidenceID {
		return Manifest{}, errors.New("scenario manifest is not canonical or has another identity")
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	for _, f := range m.Files {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		data, err := readRegular(root, "files/"+f.Path, f.Size, true)
		if err != nil {
			return Manifest{}, err
		}
		hash := sha256.Sum256(data)
		if int64(len(data)) != f.Size || hex.EncodeToString(hash[:]) != f.SHA256 {
			return Manifest{}, errors.New("sealed scenario file changed")
		}
	}
	return m, nil
}

func manifestHash(m Manifest) (string, error) {
	m.Hash = ""
	return canonical.Hash("scenario-manifest", Version, m)
}

func safeID(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\r\n\x00")
}

func validPath(value string) bool {
	p, err := scope.NormalizeRepositoryPath(value)
	return err == nil && p == value && value != "." && !strings.ContainsAny(value, "*?[]")
}

func digest(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func readRegular(root *os.Root, name string, limit int64, immutable bool) ([]byte, error) {
	if !validPath(name) || limit < 0 {
		return nil, errors.New("invalid scenario artifact path or limit")
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) {
			return nil, errors.New("scenario artifact path contains a linked or non-directory component")
		}
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > limit || (immutable && before.Mode().Perm() != 0400) {
		return nil, errors.New("scenario artifact is non-regular, mutable or oversized")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || int64(len(data)) > limit || before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Size() != int64(len(data)) {
		return nil, errors.New("scenario artifact changed during capture")
	}
	return data, nil
}

func artifactDirectory(runtimeRoot, id string, create bool) (string, error) {
	if !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot || !safeID(id) {
		return "", errors.New("invalid scenario runtime directory")
	}
	resolved, err := filepath.EvalSymlinks(runtimeRoot)
	if err != nil || resolved != runtimeRoot {
		return "", errors.New("scenario runtime root is unavailable or linked")
	}
	directory := runtimeRoot
	for _, component := range []string{"scenarios", id} {
		directory = filepath.Join(directory, component)
		if create {
			if err := mkdirDurable(directory); err != nil {
				return "", err
			}
		}
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("unsafe scenario artifact directory")
		}
	}
	return directory, nil
}

func makePrivateParents(root, directory string) error {
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return errors.New("scenario destination escapes artifact directory")
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		root = filepath.Join(root, part)
		if err := mkdirDurable(root); err != nil {
			return err
		}
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return errors.New("unsafe scenario destination")
		}
	}
	return nil
}

func mkdirDurable(path string) error {
	if err := os.Mkdir(path, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return err
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(parent.Sync(), parent.Close())
}

func writeImmutable(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0400)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
