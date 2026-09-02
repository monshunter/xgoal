package workpacket

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/protocol"
)

const maxPacketBytes = 8 << 20

type Store struct {
	root string
}

type Artifact struct {
	Packet protocol.WorkPacket
	Hash   string
	Path   string
}

func NewStore(runtimeRoot string) (*Store, error) {
	if !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot {
		return nil, errors.New("work packet runtime root must be clean and absolute")
	}
	if err := ensureDirectory(runtimeRoot); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(runtimeRoot)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(resolved, "packets")
	if err := ensureDirectory(root); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (store *Store) Save(attemptID string, packet protocol.WorkPacket) (Artifact, bool, error) {
	if !validComponent(attemptID) {
		return Artifact{}, false, errors.New("invalid work packet attempt id")
	}
	hash, err := packet.Hash()
	if err != nil {
		return Artifact{}, false, err
	}
	content, err := canonical.Marshal(packet)
	if err != nil {
		return Artifact{}, false, err
	}
	finalDirectory := filepath.Join(store.root, attemptID)
	if _, err := os.Lstat(finalDirectory); err == nil {
		existing, readErr := store.Load(attemptID)
		if readErr == nil && existing.Hash == hash {
			return existing, false, nil
		}
		return Artifact{}, false, errors.New("work packet attempt already has a different or invalid artifact")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Artifact{}, false, err
	}
	temporaryDirectory, err := os.MkdirTemp(store.root, ".packet-")
	if err != nil {
		return Artifact{}, false, err
	}
	keep := true
	defer func() {
		if keep {
			_ = os.RemoveAll(temporaryDirectory)
		}
	}()
	if err := os.Chmod(temporaryDirectory, 0o700); err != nil {
		return Artifact{}, false, err
	}
	packetPath := filepath.Join(temporaryDirectory, "packet.json")
	file, err := os.OpenFile(packetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return Artifact{}, false, err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return Artifact{}, false, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return Artifact{}, false, err
	}
	if err := file.Close(); err != nil {
		return Artifact{}, false, err
	}
	if err := syncDirectory(temporaryDirectory); err != nil {
		return Artifact{}, false, err
	}
	if err := os.Rename(temporaryDirectory, finalDirectory); err != nil {
		return Artifact{}, false, fmt.Errorf("publish immutable work packet: %w", err)
	}
	keep = false
	if err := syncDirectory(store.root); err != nil {
		return Artifact{}, false, err
	}
	return Artifact{Packet: packet, Hash: hash, Path: filepath.Join(finalDirectory, "packet.json")}, true, nil
}

func (store *Store) Load(attemptID string) (Artifact, error) {
	if !validComponent(attemptID) {
		return Artifact{}, errors.New("invalid work packet attempt id")
	}
	directory := filepath.Join(store.root, attemptID)
	directoryInfo, err := os.Lstat(directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 || directoryInfo.Mode().Perm() != 0o700 {
		return Artifact{}, errors.New("work packet directory is missing or unsafe")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "packet.json" {
		return Artifact{}, errors.New("work packet directory contains an unexpected layout")
	}
	filename := filepath.Join(directory, "packet.json")
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o400 || info.Size() > maxPacketBytes {
		return Artifact{}, errors.New("work packet file is missing, linked, writable, or oversized")
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		return Artifact{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var packet protocol.WorkPacket
	if err := decoder.Decode(&packet); err != nil {
		return Artifact{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Artifact{}, errors.New("work packet contains trailing JSON")
	}
	canonicalContent, err := canonical.Marshal(packet)
	if err != nil || !bytes.Equal(content, canonicalContent) {
		return Artifact{}, errors.New("work packet is not canonical")
	}
	hash, err := packet.Hash()
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Packet: packet, Hash: hash, Path: filename}, nil
}

func ensureDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("work packet directory is missing or unsafe")
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

func validComponent(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 160 && utf8.ValidString(value) && !strings.ContainsAny(value, "/\\\r\n\x00")
}
