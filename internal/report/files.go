package report

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrArtifactMismatch = errors.New("final report artifact hash mismatch")
	ErrUnsafeReportPath = errors.New("unsafe final report path")
)

type PreparedFiles struct {
	GoalID           string `json:"goal_id"`
	ProtocolVersion  string `json:"protocol_version"`
	ReportHash       string `json:"report_hash"`
	JSONPath         string `json:"json_path"`
	JSONTempPath     string `json:"json_temp_path"`
	JSONHash         string `json:"json_hash"`
	JSON             []byte `json:"json"`
	MarkdownPath     string `json:"markdown_path"`
	MarkdownTempPath string `json:"markdown_temp_path"`
	MarkdownHash     string `json:"markdown_hash"`
	Markdown         []byte `json:"markdown"`
}

type FileManager struct {
	root        string
	afterRename func(index int) error
}

func NewFileManager(stateRoot string) (*FileManager, error) {
	if !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot {
		return nil, fmt.Errorf("%w: state root must be a clean absolute path", ErrUnsafeReportPath)
	}
	if err := ensurePrivateDir(stateRoot); err != nil {
		return nil, err
	}
	return &FileManager{root: stateRoot}, nil
}

func (manager *FileManager) Prepare(goalID string, artifact Artifact) (PreparedFiles, error) {
	if !safeName(goalID) || !validHash(artifact.ReportHash, 64) || bytesHash(artifact.JSON) != artifact.JSONHash || bytesHash(artifact.Markdown) != artifact.MarkdownHash {
		return PreparedFiles{}, fmt.Errorf("%w: invalid goal or artifact identity", ErrUnsafeReportPath)
	}
	reportDir := filepath.Join(manager.root, "reports", goalID)
	if err := ensurePrivateDir(filepath.Join(manager.root, "reports")); err != nil {
		return PreparedFiles{}, err
	}
	if err := ensurePrivateDir(reportDir); err != nil {
		return PreparedFiles{}, err
	}
	jsonFinal := filepath.Join("reports", goalID, artifact.ReportHash+".json")
	markdownFinal := filepath.Join("reports", goalID, artifact.ReportHash+".md")
	jsonTemp, err := manager.writeTemp(reportDir, artifact.ReportHash+".json", artifact.JSON)
	if err != nil {
		return PreparedFiles{}, err
	}
	markdownTemp, err := manager.writeTemp(reportDir, artifact.ReportHash+".md", artifact.Markdown)
	if err != nil {
		_ = os.Remove(jsonTemp)
		return PreparedFiles{}, err
	}
	if err := syncDir(reportDir); err != nil {
		return PreparedFiles{}, err
	}
	return PreparedFiles{
		GoalID: goalID, ProtocolVersion: ProtocolVersion, ReportHash: artifact.ReportHash,
		JSONPath: jsonFinal, JSONTempPath: manager.relative(jsonTemp), JSONHash: artifact.JSONHash, JSON: append([]byte(nil), artifact.JSON...),
		MarkdownPath: markdownFinal, MarkdownTempPath: manager.relative(markdownTemp), MarkdownHash: artifact.MarkdownHash, Markdown: append([]byte(nil), artifact.Markdown...),
	}, nil
}

func (manager *FileManager) Commit(prepared PreparedFiles) error {
	if err := manager.validatePrepared(prepared); err != nil {
		return err
	}
	files := []preparedFile{
		{final: prepared.JSONPath, temp: prepared.JSONTempPath, hash: prepared.JSONHash, payload: prepared.JSON},
		{final: prepared.MarkdownPath, temp: prepared.MarkdownTempPath, hash: prepared.MarkdownHash, payload: prepared.Markdown},
	}
	for index, file := range files {
		if err := manager.publish(file, false); err != nil {
			return err
		}
		if manager.afterRename != nil {
			if err := manager.afterRename(index); err != nil {
				return err
			}
		}
	}
	_, _, err := manager.Read(prepared)
	return err
}

func (manager *FileManager) Recover(prepared PreparedFiles) error {
	if err := manager.validatePrepared(prepared); err != nil {
		return err
	}
	for _, file := range []preparedFile{
		{final: prepared.JSONPath, temp: prepared.JSONTempPath, hash: prepared.JSONHash, payload: prepared.JSON},
		{final: prepared.MarkdownPath, temp: prepared.MarkdownTempPath, hash: prepared.MarkdownHash, payload: prepared.Markdown},
	} {
		if err := manager.publish(file, true); err != nil {
			return err
		}
	}
	_, _, err := manager.Read(prepared)
	return err
}

func (manager *FileManager) Read(prepared PreparedFiles) ([]byte, []byte, error) {
	if err := manager.validatePrepared(prepared); err != nil {
		return nil, nil, err
	}
	jsonBytes, err := manager.readVerified(prepared.JSONPath, prepared.JSONHash)
	if err != nil {
		return nil, nil, err
	}
	markdown, err := manager.readVerified(prepared.MarkdownPath, prepared.MarkdownHash)
	if err != nil {
		return nil, nil, err
	}
	return jsonBytes, markdown, nil
}

// Discard removes only verified private temporary files that were never committed to SQLite.
func (manager *FileManager) Discard(prepared PreparedFiles) error {
	if err := manager.validatePrepared(prepared); err != nil {
		return err
	}
	var result error
	for _, item := range []struct {
		path string
		hash string
	}{{prepared.JSONTempPath, prepared.JSONHash}, {prepared.MarkdownTempPath, prepared.MarkdownHash}} {
		path, err := manager.resolve(item.path)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if err := verifyRegularFile(path, item.hash); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if err := os.Remove(path); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

type preparedFile struct {
	final   string
	temp    string
	hash    string
	payload []byte
}

func (manager *FileManager) publish(file preparedFile, rebuild bool) error {
	finalPath, err := manager.resolve(file.final)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(finalPath); err == nil {
		if _, verifyErr := manager.readVerified(file.final, file.hash); verifyErr != nil {
			return verifyErr
		}
		if tempPath, resolveErr := manager.resolve(file.temp); resolveErr == nil {
			if tempInfo, statErr := os.Lstat(tempPath); statErr == nil && tempInfo.Mode().IsRegular() && tempInfo.Mode()&os.ModeSymlink == 0 {
				_ = os.Remove(tempPath)
			}
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect final report artifact: %w", err)
	}
	tempPath, err := manager.resolve(file.temp)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(tempPath); errors.Is(err, os.ErrNotExist) {
		if !rebuild {
			return fmt.Errorf("%w: prepared temporary artifact is missing", ErrArtifactMismatch)
		}
		tempPath, err = manager.writeTemp(filepath.Dir(finalPath), filepath.Base(finalPath), file.payload)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := verifyRegularFile(tempPath, file.hash); err != nil {
		return err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return fmt.Errorf("publish final report artifact: %w", err)
	}
	if err := os.Chmod(finalPath, 0o600); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(finalPath)); err != nil {
		return err
	}
	return verifyRegularFile(finalPath, file.hash)
}

func (manager *FileManager) readVerified(relative, hash string) ([]byte, error) {
	path, err := manager.resolve(relative)
	if err != nil {
		return nil, err
	}
	if err := verifyRegularFile(path, hash); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (manager *FileManager) writeTemp(directory, base string, payload []byte) (string, error) {
	for range 32 {
		random := make([]byte, 12)
		if _, err := rand.Read(random); err != nil {
			return "", err
		}
		path := filepath.Join(directory, "."+base+".tmp-"+hex.EncodeToString(random))
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create final report temporary file: %w", err)
		}
		writeErr := func() error {
			if _, err := file.Write(payload); err != nil {
				return err
			}
			if err := file.Sync(); err != nil {
				return err
			}
			return file.Close()
		}()
		if writeErr != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return "", writeErr
		}
		return path, nil
	}
	return "", errors.New("cannot allocate final report temporary file")
}

func (manager *FileManager) validatePrepared(value PreparedFiles) error {
	if !safeName(value.GoalID) || value.ProtocolVersion != ProtocolVersion || !validHash(value.ReportHash, 64) ||
		bytesHash(value.JSON) != value.JSONHash || bytesHash(value.Markdown) != value.MarkdownHash {
		return fmt.Errorf("%w: invalid persisted report record", ErrArtifactMismatch)
	}
	for _, relative := range []string{value.JSONPath, value.JSONTempPath, value.MarkdownPath, value.MarkdownTempPath} {
		if _, err := manager.resolve(relative); err != nil {
			return err
		}
	}
	return nil
}

func (manager *FileManager) resolve(relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: invalid relative artifact path", ErrUnsafeReportPath)
	}
	absolute := filepath.Join(manager.root, relative)
	if filepath.Clean(absolute) != absolute || absolute == manager.root || !strings.HasPrefix(absolute, manager.root+string(filepath.Separator)) {
		return "", ErrUnsafeReportPath
	}
	return absolute, nil
}

func (manager *FileManager) relative(path string) string {
	value, _ := filepath.Rel(manager.root, path)
	return value
}

func ensurePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil {
			return fmt.Errorf("create final report directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: report directory is not a real directory", ErrUnsafeReportPath)
	}
	return os.Chmod(path, 0o700)
}

func verifyRegularFile(path, expectedHash string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: artifact is not a regular file", ErrUnsafeReportPath)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if bytesHash(contents) != expectedHash {
		return ErrArtifactMismatch
	}
	return nil
}

func syncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func safeName(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\r\n\x00")
}
