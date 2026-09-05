package environment

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/scope"
	"github.com/monshunter/xgoal/internal/supervisor"
)

const (
	providerName         = "local-process"
	isolationLevel       = "L0"
	maxVersionBytes      = 64 << 10
	maxLockfileBytes     = 16 << 20
	maxSnapshotFileBytes = int64(64 << 20)
)

var ErrCheckoutDrift = errors.New("current checkout changed during the execution phase")

var defaultToolProbes = []ToolProbe{
	{Name: "claude", Argv: []string{"claude", "--version"}},
	{Name: "codex", Argv: []string{"codex", "--version"}},
	{Name: "docker", Argv: []string{"docker", "--version"}},
	{Name: "go", Argv: []string{"go", "version"}},
	{Name: "node", Argv: []string{"node", "--version"}},
	{Name: "python3", Argv: []string{"python3", "--version"}},
}

type Local struct {
	root       string
	repository *gitrepo.Repository
	clock      clock.Clock

	mu      sync.Mutex
	handles map[string]*managedEnvironment
}

type managedEnvironment struct {
	spec        Spec
	handle      Handle
	environment map[string]string
	services    *supervisor.Group
}

func NewLocal(runtimeRoot string, repository *gitrepo.Repository, currentClock clock.Clock) (*Local, error) {
	if repository == nil || currentClock == nil || !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot || strings.ContainsAny(runtimeRoot, "\r\n\x00") {
		return nil, errors.New("local provider requires repository, clock, and clean absolute runtime root")
	}
	if err := ensurePrivateDirectory(runtimeRoot); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(runtimeRoot)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(resolved, "environments")
	if err := ensurePrivateDirectory(root); err != nil {
		return nil, err
	}
	return &Local{root: root, repository: repository, clock: currentClock, handles: make(map[string]*managedEnvironment)}, nil
}

func (local *Local) Probe(ctx context.Context, project Project) (Capabilities, error) {
	if project.Root == "" {
		return Capabilities{}, errors.New("project root is required")
	}
	canonical, err := filepath.EvalSymlinks(project.Root)
	if err != nil || canonical != local.repository.Root() {
		return Capabilities{}, errors.New("project root does not match trusted repository")
	}
	if _, err := local.runVersion(ctx, canonical, []string{"git", "--version"}, minimalEnvironment(os.TempDir())); err != nil {
		return Capabilities{}, fmt.Errorf("probe Git: %w", err)
	}
	return Capabilities{
		Provider: providerName, IsolationLevel: isolationLevel,
		FilesystemIsolation: false, NetworkIsolation: false,
		CredentialIsolation: isolationLevel, TrustedRepositoryOnly: true,
	}, nil
}

func (local *Local) Prepare(ctx context.Context, spec Spec) (Handle, error) {
	if err := validateSpec(spec); err != nil {
		return Handle{}, err
	}
	if spec.WorktreePath != local.repository.Root() || spec.Identity.Root != spec.WorktreePath || spec.BaseCommit != spec.Identity.HeadCommit {
		return Handle{}, errors.New("environment must bind the current project root and user HEAD identity")
	}
	spec.ExcludePaths = append(append([]string(nil), spec.ExcludePaths...), filepath.Dir(local.root))
	if err := local.repository.CheckSnapshot(ctx, snapshotSpec(spec), spec.Identity, spec.BaseTree); err != nil {
		return Handle{}, fmt.Errorf("%w: %w", ErrCheckoutDrift, err)
	}
	local.mu.Lock()
	defer local.mu.Unlock()
	if _, exists := local.handles[spec.ID]; exists {
		return Handle{}, fmt.Errorf("environment %q already prepared", spec.ID)
	}
	environmentRoot := filepath.Join(local.root, spec.ID)
	if _, err := os.Lstat(environmentRoot); !errors.Is(err, os.ErrNotExist) {
		return Handle{}, fmt.Errorf("environment directory %q already exists", environmentRoot)
	}
	if err := os.Mkdir(environmentRoot, 0o700); err != nil {
		return Handle{}, err
	}
	prepared := false
	defer func() {
		if !prepared {
			_ = os.Remove(environmentRoot)
		}
	}()
	for _, child := range []string{"logs", "tmp"} {
		if err := os.Mkdir(filepath.Join(environmentRoot, child), 0o700); err != nil {
			return Handle{}, err
		}
	}
	environment, err := buildEnvironment(filepath.Join(environmentRoot, "tmp"), spec.EnvironmentAllowlist)
	if err != nil {
		return Handle{}, err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return Handle{}, fmt.Errorf("create environment handle token: %w", err)
	}
	handle := Handle{ID: spec.ID, Worktree: spec.WorktreePath, Root: environmentRoot, token: hex.EncodeToString(tokenBytes)}
	local.handles[spec.ID] = &managedEnvironment{spec: spec, handle: handle, environment: environment, services: supervisor.NewGroup()}
	prepared = true
	return handle, nil
}

func (local *Local) Snapshot(ctx context.Context, handle Handle) (protocol.EnvironmentSnapshot, error) {
	managed, err := local.readHandle(handle)
	if err != nil {
		return protocol.EnvironmentSnapshot{}, err
	}
	if err := local.VerifyTree(ctx, handle, managed.spec.BaseTree); err != nil {
		return protocol.EnvironmentSnapshot{}, err
	}
	environment := environmentSlice(managed.environment)
	kernel, err := local.runVersion(ctx, managed.handle.Worktree, []string{"uname", "-srv"}, environment)
	if err != nil {
		return protocol.EnvironmentSnapshot{}, fmt.Errorf("capture kernel version: %w", err)
	}
	gitVersion, err := local.runVersion(ctx, managed.handle.Worktree, []string{"git", "--version"}, environment)
	if err != nil {
		return protocol.EnvironmentSnapshot{}, fmt.Errorf("capture Git version: %w", err)
	}
	probes := managed.spec.ToolProbes
	if len(probes) == 0 {
		probes = defaultToolProbes
	}
	toolVersions := make(map[string]string, len(probes))
	for _, probe := range probes {
		probeContext, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		version, err := local.runVersion(probeContext, managed.handle.Worktree, probe.Argv, environment)
		cancel()
		if err != nil {
			if probe.Required {
				return protocol.EnvironmentSnapshot{}, fmt.Errorf("probe required tool %q: %w", probe.Name, err)
			}
			version = "unavailable"
		}
		toolVersions[probe.Name] = version
	}
	lockfileHashes := make(map[string]string, len(managed.spec.Lockfiles))
	for _, filename := range managed.spec.Lockfiles {
		hash, err := hashWorkspaceFile(managed.handle.Worktree, filename)
		if err != nil {
			return protocol.EnvironmentSnapshot{}, err
		}
		lockfileHashes[filename] = hash
	}
	environmentNames := make([]string, 0, len(managed.environment))
	for name := range managed.environment {
		environmentNames = append(environmentNames, name)
	}
	sort.Strings(environmentNames)
	snapshot := protocol.EnvironmentSnapshot{
		ProtocolVersion:  protocol.EnvironmentSnapshotVersion,
		ID:               "snapshot_" + managed.spec.ID,
		OS:               runtime.GOOS,
		Kernel:           kernel,
		Arch:             runtime.GOARCH,
		IsolationLevel:   isolationLevel,
		GitVersion:       gitVersion,
		BaseCommit:       managed.spec.BaseCommit,
		BaseTree:         managed.spec.BaseTree,
		ToolVersions:     toolVersions,
		LockfileHashes:   lockfileHashes,
		ConfigHash:       managed.spec.ConfigHash,
		GoalRevisionHash: managed.spec.GoalRevisionHash,
		EnvironmentNames: environmentNames,
		BootstrapHash:    managed.spec.BootstrapHash,
		CapturedAt:       local.clock.Now().UTC(),
	}
	if err := snapshot.Validate(); err != nil {
		return protocol.EnvironmentSnapshot{}, err
	}
	if err := local.VerifyTree(ctx, handle, managed.spec.BaseTree); err != nil {
		return protocol.EnvironmentSnapshot{}, err
	}
	return snapshot, nil
}

// VerifyTree binds command/review evidence to the same raw current-directory
// contents and user Git identity before and after a phase.
func (local *Local) VerifyTree(ctx context.Context, handle Handle, expectedTree string) error {
	managed, err := local.readHandle(handle)
	if err != nil {
		return err
	}
	if err := local.repository.CheckSnapshot(ctx, snapshotSpec(managed.spec), managed.spec.Identity, expectedTree); err != nil {
		return fmt.Errorf("%w: %w", ErrCheckoutDrift, err)
	}
	return nil
}

func snapshotSpec(spec Spec) gitrepo.SnapshotSpec {
	return gitrepo.SnapshotSpec{BaseTree: spec.BaseTree, ExcludePaths: spec.ExcludePaths, MaxFileBytes: maxSnapshotFileBytes}
}

func (local *Local) StartServices(ctx context.Context, handle Handle, services []ServiceSpec) error {
	managed, err := local.readHandle(handle)
	if err != nil {
		return err
	}
	started := make([]string, 0, len(services))
	rollback := func(cause error) error {
		result := cause
		for index := len(started) - 1; index >= 0; index-- {
			result = errors.Join(result, managed.services.Stop(started[index]))
		}
		return result
	}
	for _, service := range services {
		if !validComponent(service.ID) {
			return rollback(errors.New("service id is invalid"))
		}
		cwd, err := resolveCWD(managed.handle.Worktree, service.CWD)
		if err != nil {
			return rollback(err)
		}
		environment, err := selectEnvironment(managed.environment, service.EnvironmentAllowlist)
		if err != nil {
			return rollback(err)
		}
		stdout, err := openPrivateLog(filepath.Join(managed.handle.Root, "logs", service.ID+".stdout.log"))
		if err != nil {
			return rollback(err)
		}
		stderr, err := openPrivateLog(filepath.Join(managed.handle.Root, "logs", service.ID+".stderr.log"))
		if err != nil {
			_ = stdout.Close()
			return rollback(err)
		}
		_, startErr := managed.services.Start(ctx, supervisor.ServiceSpec{
			ID: service.ID,
			Command: supervisor.Command{
				Argv: append([]string(nil), service.Argv...), Dir: cwd, Env: environment,
				Stdout: stdout, Stderr: stderr, GracePeriod: service.GracePeriod,
			},
			Probe: service.Probe, ProbeTimeout: service.ProbeTimeout, ProbeInterval: service.ProbeInterval,
		})
		closeErr := errors.Join(stdout.Close(), stderr.Close())
		if startErr != nil || closeErr != nil {
			return rollback(errors.Join(startErr, closeErr))
		}
		started = append(started, service.ID)
	}
	return nil
}

func (local *Local) StopServices(_ context.Context, handle Handle) error {
	managed, err := local.readHandle(handle)
	if err != nil {
		return err
	}
	return managed.services.StopAll()
}

func (local *Local) RunCommand(ctx context.Context, handle Handle, spec CommandSpec) (supervisor.Execution, error) {
	managed, err := local.readHandle(handle)
	if err != nil {
		return supervisor.Execution{}, err
	}
	cwd, err := resolveCWD(managed.handle.Worktree, spec.CWD)
	if err != nil {
		return supervisor.Execution{}, err
	}
	environment, err := selectEnvironment(managed.environment, spec.EnvironmentAllowlist)
	if err != nil {
		return supervisor.Execution{}, err
	}
	return supervisor.Run(ctx, supervisor.Command{
		Argv: append([]string(nil), spec.Argv...), Dir: cwd, Env: environment,
		Stdin: spec.Stdin, Stdout: spec.Stdout, Stderr: spec.Stderr, GracePeriod: spec.GracePeriod,
	})
}

func (local *Local) Cleanup(ctx context.Context, handle Handle) error {
	managed, err := local.readHandle(handle)
	if err != nil {
		return err
	}
	if err := local.StopServices(ctx, handle); err != nil {
		return err
	}
	local.mu.Lock()
	defer local.mu.Unlock()
	current, exists := local.handles[handle.ID]
	if !exists || current != managed || current.handle.token != handle.token || filepath.Dir(current.handle.Root) != local.root {
		return errors.New("environment handle changed before cleanup")
	}
	// The directory contains attributable service logs and temporary failure
	// evidence. Explicit clean owns deletion; lifecycle cleanup only releases
	// processes and this in-memory capability.
	delete(local.handles, handle.ID)
	return nil
}

func (local *Local) readHandle(handle Handle) (*managedEnvironment, error) {
	local.mu.Lock()
	defer local.mu.Unlock()
	managed, exists := local.handles[handle.ID]
	if !exists || handle.token == "" || managed.handle.token != handle.token || managed.handle.Worktree != handle.Worktree || managed.handle.Root != handle.Root {
		return nil, errors.New("unknown or forged environment handle")
	}
	return managed, nil
}

func (local *Local) runVersion(ctx context.Context, directory string, argv, environment []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output := &boundedBuffer{limit: maxVersionBytes}
	execution, err := supervisor.Run(ctx, supervisor.Command{
		Argv: argv, Dir: directory, Env: environment, Stdout: output, Stderr: output, GracePeriod: time.Second,
	})
	if err != nil {
		return "", err
	}
	if execution.ExitCode != 0 {
		return "", fmt.Errorf("%q exited %d", argv[0], execution.ExitCode)
	}
	if output.exceeded {
		return "", fmt.Errorf("%q version output exceeds %d bytes", argv[0], maxVersionBytes)
	}
	version := strings.TrimSpace(output.buffer.String())
	if version == "" || !utf8.ValidString(version) || strings.ContainsRune(version, '\x00') {
		return "", fmt.Errorf("%q returned an invalid version", argv[0])
	}
	return version, nil
}

func validateSpec(spec Spec) error {
	if !validComponent(spec.ID) || spec.WorktreePath == "" || spec.BaseCommit == "" || spec.BaseTree == "" || !validHash(spec.ConfigHash) || !validHash(spec.GoalRevisionHash) || (spec.BootstrapHash != "" && !validHash(spec.BootstrapHash)) {
		return errors.New("invalid local environment spec")
	}
	if _, err := scope.CanonicalizePaths(spec.Lockfiles); err != nil && len(spec.Lockfiles) > 0 {
		return fmt.Errorf("invalid lockfiles: %w", err)
	}
	seenTools := make(map[string]struct{}, len(spec.ToolProbes))
	for _, probe := range spec.ToolProbes {
		if !validComponent(probe.Name) || len(probe.Argv) == 0 {
			return errors.New("invalid tool probe")
		}
		if _, exists := seenTools[probe.Name]; exists {
			return fmt.Errorf("duplicate tool probe %q", probe.Name)
		}
		seenTools[probe.Name] = struct{}{}
	}
	_, err := buildEnvironment(os.TempDir(), spec.EnvironmentAllowlist)
	return err
}

func buildEnvironment(tempDirectory string, allowlist []string) (map[string]string, error) {
	result := map[string]string{"LANG": "C", "LC_ALL": "C", "TMPDIR": tempDirectory}
	if value, exists := os.LookupEnv("PATH"); exists {
		result["PATH"] = value
	}
	seen := make(map[string]struct{}, len(allowlist))
	for _, name := range allowlist {
		if !validEnvironmentName(name) {
			return nil, fmt.Errorf("invalid environment name %q", name)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate environment name %q", name)
		}
		seen[name] = struct{}{}
		if value, exists := os.LookupEnv(name); exists {
			result[name] = value
		}
	}
	return result, nil
}

func selectEnvironment(available map[string]string, names []string) ([]string, error) {
	if len(names) == 0 {
		return environmentSlice(available), nil
	}
	selected := make(map[string]string, len(names)+4)
	for _, name := range []string{"LANG", "LC_ALL", "PATH", "TMPDIR"} {
		if value, exists := available[name]; exists {
			selected[name] = value
		}
	}
	for _, name := range names {
		value, exists := available[name]
		if !exists {
			return nil, fmt.Errorf("environment name %q was not prepared", name)
		}
		selected[name] = value
	}
	return environmentSlice(selected), nil
}

func environmentSlice(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

func resolveCWD(root, relative string) (string, error) {
	if relative == "" || relative == "." {
		return root, nil
	}
	canonical, err := scope.NormalizeRepositoryPath(relative)
	if err != nil || canonical != relative {
		return "", errors.New("service cwd is not a canonical repository-relative path")
	}
	candidate := filepath.Join(root, filepath.FromSlash(canonical))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || !within(root, resolved) {
		return "", errors.New("service cwd is missing or escapes through a symlink")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("service cwd is not a directory")
	}
	return resolved, nil
}

func hashWorkspaceFile(root, relative string) (string, error) {
	canonical, err := scope.NormalizeRepositoryPath(relative)
	if err != nil || canonical != relative {
		return "", errors.New("lockfile path is not canonical")
	}
	filename := filepath.Join(root, filepath.FromSlash(canonical))
	if err := rejectLinkedParents(root, filename); err != nil {
		return "", err
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxLockfileBytes {
		return "", fmt.Errorf("lockfile %q is missing, unsafe, or too large", relative)
	}
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxLockfileBytes+1))
	if err != nil || written != info.Size() || written > maxLockfileBytes {
		return "", fmt.Errorf("lockfile %q changed or exceeded its limit", relative)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func rejectLinkedParents(root, filename string) error {
	relative, err := filepath.Rel(root, filename)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("path escapes environment worktree")
	}
	current := root
	for _, segment := range strings.Split(filepath.Dir(relative), string(filepath.Separator)) {
		if segment == "." {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("path has a missing or linked parent")
		}
	}
	return nil
}

func openPrivateLog(filename string) (*os.File, error) {
	return os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func minimalEnvironment(tempDirectory string) []string {
	values, _ := buildEnvironment(tempDirectory, nil)
	return environmentSlice(values)
}

func ensurePrivateDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(directory)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("environment directory is missing or unsafe")
	}
	return os.Chmod(directory, 0o700)
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validComponent(value string) bool {
	return value != "" && value != "." && value != ".." && utf8.ValidString(value) && !strings.ContainsAny(value, "/\\\r\n\x00")
}

func validEnvironmentName(value string) bool {
	if value == "" || !((value[0] >= 'A' && value[0] <= 'Z') || value[0] == '_') {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if !((character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_') {
			return false
		}
	}
	return true
}

func validHash(value string) bool {
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

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - buffer.buffer.Len()
	if len(value) > remaining {
		buffer.exceeded = true
		if remaining > 0 {
			_, _ = buffer.buffer.Write(value[:remaining])
		}
		return len(value), nil
	}
	_, _ = buffer.buffer.Write(value)
	return len(value), nil
}
