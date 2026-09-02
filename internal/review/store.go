package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/protocol"
)

type Store struct {
	root string
}

type PacketArtifact struct {
	Packet protocol.ReviewPacket
	Hash   string
	Path   string
}

type ResultArtifact struct {
	Result protocol.ReviewResult
	Hash   string
	Path   string
}

func NewStore(runtimeRoot string) (*Store, error) {
	if !cleanAbsolute(runtimeRoot) {
		return nil, errors.New("review runtime root must be clean and absolute")
	}
	if err := ensureDirectory(runtimeRoot, 0o700); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(runtimeRoot)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(resolved, "reviews")
	if err := ensureDirectory(root, 0o700); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (store *Store) SavePacket(packet protocol.ReviewPacket) (PacketArtifact, error) {
	hash, err := packet.Hash()
	if err != nil {
		return PacketArtifact{}, err
	}
	directory := filepath.Join(store.root, packet.ID)
	if err := ensureDirectory(directory, 0o700); err != nil {
		return PacketArtifact{}, err
	}
	content, err := canonical.Marshal(packet)
	if err != nil {
		return PacketArtifact{}, err
	}
	path := filepath.Join(directory, "packet.json")
	if err := writeImmutable(path, content, 0o400); err != nil {
		if existing, existingHash, readErr := ReadPacket(path); readErr == nil && existingHash == hash {
			return PacketArtifact{Packet: existing, Hash: existingHash, Path: path}, nil
		}
		return PacketArtifact{}, err
	}
	return PacketArtifact{Packet: packet, Hash: hash, Path: path}, nil
}

func (store *Store) SaveResult(reviewID string, result protocol.ReviewResult) (ResultArtifact, error) {
	if !component(reviewID) {
		return ResultArtifact{}, errors.New("invalid review result id")
	}
	hash, err := result.Hash()
	if err != nil {
		return ResultArtifact{}, err
	}
	content, err := canonical.Marshal(result)
	if err != nil {
		return ResultArtifact{}, err
	}
	path := filepath.Join(store.root, reviewID, "result.json")
	if err := writeImmutable(path, content, 0o600); err != nil {
		if existing, existingHash, readErr := ReadResult(path); readErr == nil && existingHash == hash {
			return ResultArtifact{Result: existing, Hash: existingHash, Path: path}, nil
		}
		return ResultArtifact{}, err
	}
	return ResultArtifact{Result: result, Hash: hash, Path: path}, nil
}

func ReadPacket(path string) (protocol.ReviewPacket, string, error) {
	var packet protocol.ReviewPacket
	if err := readCanonical(path, 0o400, &packet); err != nil {
		return protocol.ReviewPacket{}, "", err
	}
	hash, err := packet.Hash()
	return packet, hash, err
}

func ReadResult(path string) (protocol.ReviewResult, string, error) {
	var result protocol.ReviewResult
	if err := readCanonical(path, 0o600, &result); err != nil {
		return protocol.ReviewResult{}, "", err
	}
	hash, err := result.Hash()
	return result, hash, err
}

func readCanonical(path string, mode os.FileMode, target any) error {
	if !cleanAbsolute(path) {
		return errors.New("review artifact path must be clean and absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode || info.Size() > 8<<20 {
		return errors.New("review artifact is missing, linked, unsafe, or oversized")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("review artifact contains trailing JSON")
	}
	canonicalContent, err := canonical.Marshal(target)
	if err != nil || !bytes.Equal(content, canonicalContent) {
		return errors.New("review artifact is not canonical")
	}
	return nil
}

func writeImmutable(path string, content []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".review-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func ensureDirectory(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, mode); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("review artifact directory is missing or unsafe")
	}
	return os.Chmod(path, mode)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
