package validator

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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/scope"
	"github.com/monshunter/xgoal/internal/supervisor"
)

type CommandEnvironment interface {
	RunCommand(context.Context, environment.Handle, environment.CommandSpec) (supervisor.Execution, error)
}

type CommandRunner struct {
	root        string
	registry    *Registry
	environment CommandEnvironment
	handle      environment.Handle
}

type CommandRequest struct {
	RunID            string
	ValidatorID      string
	GoalRevisionHash string
	ConfigHash       string
	TreeHash         string
	EnvironmentHash  string
	MaxOutputBytes   int64
}

const maxValidatorOutputBytes = 64 << 20

func NewCommandRunner(runtimeRoot string, registry *Registry, commandEnvironment CommandEnvironment, handle environment.Handle) (*CommandRunner, error) {
	if registry == nil || commandEnvironment == nil || !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot {
		return nil, errors.New("command runner requires runtime root, registry, environment, and handle")
	}
	root := filepath.Join(runtimeRoot, "validator")
	logs := filepath.Join(root, "logs")
	receipts := filepath.Join(root, "receipts")
	for _, directory := range []string{runtimeRoot, root, logs, receipts} {
		if err := ensureValidatorDirectory(directory); err != nil {
			return nil, err
		}
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	return &CommandRunner{root: resolved, registry: registry, environment: commandEnvironment, handle: handle}, nil
}

func (runner *CommandRunner) Run(ctx context.Context, request CommandRequest) (protocol.CommandReceipt, error) {
	if !validRunID(request.RunID) || request.ValidatorID == "" || request.ConfigHash != runner.registry.ConfigHash() ||
		!validReceiptHash(request.GoalRevisionHash) || !validReceiptHash(request.ConfigHash) || !validTreeID(request.TreeHash) ||
		!validReceiptHash(request.EnvironmentHash) || request.MaxOutputBytes <= 0 || request.MaxOutputBytes > maxValidatorOutputBytes {
		return protocol.CommandReceipt{}, errors.New("invalid command validator request")
	}
	definition, exists := runner.registry.Definition(request.ValidatorID)
	if !exists {
		return protocol.CommandReceipt{}, errors.New("unknown validator")
	}
	stdoutRef := filepath.ToSlash(filepath.Join("logs", request.RunID+".stdout.log"))
	stderrRef := filepath.ToSlash(filepath.Join("logs", request.RunID+".stderr.log"))
	stdout, err := openValidatorLog(filepath.Join(runner.root, filepath.FromSlash(stdoutRef)))
	if err != nil {
		return protocol.CommandReceipt{}, err
	}
	stderr, err := openValidatorLog(filepath.Join(runner.root, filepath.FromSlash(stderrRef)))
	if err != nil {
		_ = stdout.Close()
		return protocol.CommandReceipt{}, err
	}
	startedAt := time.Now().UTC()
	result := protocol.CommandUnavailable
	exitCode := -1
	var execution supervisor.Execution
	var runErr error
	limiter := &outputLimiter{remaining: request.MaxOutputBytes}
	runContext, cancel := context.WithTimeout(ctx, definition.Timeout)
	limiter.cancel = cancel
	if err := verifyTrustedExecutable(runner.handle.Worktree, definition); err != nil {
		runErr = err
	} else {
		execution, runErr = runner.environment.RunCommand(runContext, runner.handle, environment.CommandSpec{
			Argv: definition.Argv, CWD: definition.CWD, EnvironmentAllowlist: definition.EnvironmentAllowlist,
			Stdout: &limitedLog{file: stdout, limiter: limiter}, Stderr: &limitedLog{file: stderr, limiter: limiter},
			GracePeriod: time.Second,
		})
		exitCode = execution.ExitCode
		result = classifyCommand(runContext, execution, runErr, limiter.exceeded, definition.ExpectedExitCodes)
	}
	cancel()
	closeErr := errors.Join(syncAndClose(stdout), syncAndClose(stderr))
	finishedAt := time.Now().UTC()
	if !execution.StartedAt.IsZero() {
		startedAt = execution.StartedAt
	}
	if !execution.FinishedAt.IsZero() {
		finishedAt = execution.FinishedAt
	}
	outputHash, hashErr := hashOutputs(filepath.Join(runner.root, filepath.FromSlash(stdoutRef)), filepath.Join(runner.root, filepath.FromSlash(stderrRef)))
	if closeErr != nil || hashErr != nil {
		return protocol.CommandReceipt{}, errors.Join(closeErr, hashErr)
	}
	receipt := protocol.CommandReceipt{
		ProtocolVersion: protocol.CommandReceiptVersion,
		ID:              request.RunID, ValidatorID: definition.ID, DefinitionHash: definition.Hash,
		GoalRevisionHash: request.GoalRevisionHash, ConfigHash: request.ConfigHash, TreeHash: request.TreeHash,
		Argv: append([]string(nil), definition.Argv...), CWD: definition.CWD, EnvironmentHash: request.EnvironmentHash,
		StartedAt: startedAt, FinishedAt: finishedAt, ExitCode: exitCode,
		StdoutRef: stdoutRef, StderrRef: stderrRef, OutputHash: outputHash, Result: result,
	}
	if err := receipt.Validate(); err != nil {
		return protocol.CommandReceipt{}, err
	}
	if err := writeReceipt(runner.root, receipt); err != nil {
		return protocol.CommandReceipt{}, err
	}
	if ctx.Err() != nil && !errors.Is(runErr, context.DeadlineExceeded) {
		return receipt, ctx.Err()
	}
	return receipt, nil
}

// ReadReceipt validates the immutable receipt file and the two logs it binds.
func ReadReceipt(runtimeRoot, runID string) (protocol.CommandReceipt, error) {
	if !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot || !validRunID(runID) {
		return protocol.CommandReceipt{}, errors.New("invalid validator receipt request")
	}
	canonicalRuntime, err := filepath.EvalSymlinks(runtimeRoot)
	if err != nil {
		return protocol.CommandReceipt{}, err
	}
	root := filepath.Join(canonicalRuntime, "validator")
	for _, directory := range []string{root, filepath.Join(root, "receipts"), filepath.Join(root, "logs")} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return protocol.CommandReceipt{}, errors.New("validator artifact directory is missing, linked, or has unsafe permissions")
		}
	}
	filename := filepath.Join(root, "receipts", runID+".json")
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() > 1<<20 {
		return protocol.CommandReceipt{}, errors.New("validator receipt file is missing or unsafe")
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		return protocol.CommandReceipt{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var receipt protocol.CommandReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return protocol.CommandReceipt{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return protocol.CommandReceipt{}, errors.New("validator receipt contains trailing data")
	}
	if receipt.ID != runID {
		return protocol.CommandReceipt{}, errors.New("validator receipt id mismatch")
	}
	if receipt.StdoutRef != filepath.ToSlash(filepath.Join("logs", runID+".stdout.log")) ||
		receipt.StderrRef != filepath.ToSlash(filepath.Join("logs", runID+".stderr.log")) {
		return protocol.CommandReceipt{}, errors.New("validator receipt log references do not match its run id")
	}
	canonicalReceipt, err := canonical.Marshal(receipt)
	if err != nil || !bytes.Equal(content, canonicalReceipt) {
		return protocol.CommandReceipt{}, errors.New("validator receipt is not canonical")
	}
	if err := receipt.Validate(); err != nil {
		return protocol.CommandReceipt{}, err
	}
	outputHash, err := hashOutputs(filepath.Join(root, filepath.FromSlash(receipt.StdoutRef)), filepath.Join(root, filepath.FromSlash(receipt.StderrRef)))
	if err != nil || outputHash != receipt.OutputHash {
		return protocol.CommandReceipt{}, errors.New("validator receipt log hash mismatch")
	}
	return receipt, nil
}

func writeReceipt(root string, receipt protocol.CommandReceipt) error {
	content, err := canonical.Marshal(receipt)
	if err != nil {
		return err
	}
	directory := filepath.Join(root, "receipts")
	finalPath := filepath.Join(directory, receipt.ID+".json")
	temporary, err := os.CreateTemp(directory, ".receipt-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
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
	if err := os.Link(temporaryPath, finalPath); err != nil {
		return fmt.Errorf("publish validator receipt without overwrite: %w", err)
	}
	return syncValidatorDirectory(directory)
}

func syncValidatorDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func classifyCommand(ctx context.Context, execution supervisor.Execution, runErr error, outputExceeded bool, expected []int) protocol.CommandResult {
	if outputExceeded {
		return protocol.CommandFailed
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(runErr, context.DeadlineExceeded) {
		return protocol.CommandTimedOut
	}
	if execution.StartedAt.IsZero() {
		return protocol.CommandUnavailable
	}
	for _, code := range expected {
		if execution.ExitCode == code {
			return protocol.CommandPassed
		}
	}
	var exitError *exec.ExitError
	if runErr == nil || errors.As(runErr, &exitError) {
		return protocol.CommandFailed
	}
	return protocol.CommandUnavailable
}

func verifyTrustedExecutable(worktree string, definition Definition) error {
	if definition.TrustedExecutablePath == "" {
		return nil
	}
	canonical, err := scope.NormalizeRepositoryPath(definition.TrustedExecutablePath)
	if err != nil || canonical != definition.TrustedExecutablePath {
		return errors.New("trusted executable path is invalid")
	}
	filename := filepath.Join(worktree, filepath.FromSlash(canonical))
	if err := rejectExecutableSymlinkParents(worktree, filename); err != nil {
		return err
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 || info.Size() > maxScriptBytes {
		return errors.New("trusted executable is missing, linked, non-executable, or too large")
	}
	content, err := os.ReadFile(filename)
	if err != nil || int64(len(content)) != info.Size() {
		return errors.New("trusted executable changed while being read")
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != definition.TrustedExecutableHash {
		return errors.New("trusted executable differs from frozen base definition")
	}
	return nil
}

func rejectExecutableSymlinkParents(root, filename string) error {
	relative, err := filepath.Rel(root, filename)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("trusted executable escapes validation worktree")
	}
	current := root
	for _, segment := range strings.Split(filepath.Dir(relative), string(filepath.Separator)) {
		if segment == "." {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("trusted executable has a missing or linked parent")
		}
	}
	return nil
}

func hashOutputs(stdoutPath, stderrPath string) (string, error) {
	hashes := make(map[string]string, 2)
	for name, filename := range map[string]string{"stdout": stdoutPath, "stderr": stderrPath} {
		pathInfo, err := os.Lstat(filename)
		if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || pathInfo.Mode().Perm() != 0o600 || pathInfo.Size() > maxValidatorOutputBytes {
			return "", errors.New("validator log is missing, linked, unsafe, or oversized")
		}
		file, err := os.Open(filename)
		if err != nil {
			return "", err
		}
		fileInfo, statErr := file.Stat()
		if statErr != nil || !os.SameFile(pathInfo, fileInfo) || !fileInfo.Mode().IsRegular() || fileInfo.Size() != pathInfo.Size() {
			_ = file.Close()
			return "", errors.New("validator log changed while being opened")
		}
		hash := sha256.New()
		written, copyErr := io.Copy(hash, io.LimitReader(file, maxValidatorOutputBytes+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || written != pathInfo.Size() || written > maxValidatorOutputBytes {
			return "", errors.Join(errors.New("validator log changed or exceeded its limit while being read"), copyErr, closeErr)
		}
		hashes[name] = hex.EncodeToString(hash.Sum(nil))
	}
	return canonical.Hash("validator-output", protocol.CommandReceiptVersion, hashes)
}

func openValidatorLog(filename string) (*os.File, error) {
	return os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func syncAndClose(file *os.File) error {
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func ensureValidatorDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(directory)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("validator directory is missing or unsafe")
	}
	return os.Chmod(directory, 0o700)
}

func validRunID(value string) bool {
	return value != "" && value != "." && value != ".." && utf8.ValidString(value) && len(value) <= 128 && !strings.ContainsAny(value, "/\\\r\n\x00")
}

func validReceiptHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validTreeID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

type outputLimiter struct {
	mu        sync.Mutex
	remaining int64
	exceeded  bool
	cancel    context.CancelFunc
}

type limitedLog struct {
	file    *os.File
	limiter *outputLimiter
}

func (log *limitedLog) Write(value []byte) (int, error) {
	log.limiter.mu.Lock()
	defer log.limiter.mu.Unlock()
	allowed := int64(len(value))
	if allowed > log.limiter.remaining {
		allowed = log.limiter.remaining
		log.limiter.exceeded = true
		log.limiter.cancel()
	}
	if allowed > 0 {
		if _, err := log.file.Write(value[:allowed]); err != nil {
			return 0, err
		}
		log.limiter.remaining -= allowed
	}
	return len(value), nil
}
