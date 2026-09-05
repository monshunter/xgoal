package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

type Ownership struct {
	paths  Paths
	files  []*os.File
	mu     sync.Mutex
	closed bool
}

// Acquire takes the repository lock before even creating the state directory.
// The state lock retains the legacy daemon lock path for rolling upgrades.
func Acquire(ctx context.Context, paths Paths) (*Ownership, error) {
	if err := validatePaths(ctx, paths); err != nil {
		return nil, err
	}
	for _, directory := range []string{paths.CommonDir, paths.StateDir} {
		if err := ownedDirectory(directory, true); err != nil {
			return nil, err
		}
	}
	owner := &Ownership{paths: paths}
	for _, path := range []string{filepath.Join(paths.CommonDir, "xgoal", "owner.lock"), filepath.Join(paths.StateDir, "run", "daemon.lock")} {
		if err := privateDirectory(filepath.Dir(path)); err != nil {
			_ = owner.Close()
			return nil, err
		}
		fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
		if err != nil {
			_ = owner.Close()
			return nil, fmt.Errorf("open ownership lock: %w", err)
		}
		file := os.NewFile(uintptr(fd), path)
		if err := safeLock(fd); err != nil {
			_ = file.Close()
			_ = owner.Close()
			return nil, err
		}
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			_ = file.Close()
			_ = owner.Close()
			if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
				return nil, ErrAlreadyRunning
			}
			return nil, err
		}
		owner.files = append(owner.files, file)
		if err := file.Chmod(0600); err != nil {
			_ = owner.Close()
			return nil, err
		}
	}
	// Another initializer may have installed binding while resolution ran.
	if err := validatePaths(ctx, paths); err != nil {
		_ = owner.Close()
		return nil, err
	}
	if err := privateDirectory(paths.StateDir); err != nil {
		_ = owner.Close()
		return nil, err
	}
	return owner, nil
}

// Bind publishes one atomic shared locator before a database can be created.
// Caller must first complete the read-only SQLite project-binding preflight.
func (owner *Ownership) Bind(ctx context.Context) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return errors.New("project ownership is closed")
	}
	if err := validatePaths(ctx, owner.paths); err != nil {
		return err
	}
	configFD, err := unix.Open(filepath.Join(owner.paths.CommonDir, "config"), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open shared Git config: %w", err)
	}
	configErr := safeLock(configFD)
	_ = unix.Close(configFD)
	if configErr != nil {
		return fmt.Errorf("unsafe shared Git config: %w", configErr)
	}
	value := binding{ProjectID: owner.paths.ProjectID, ProjectRoot: owner.paths.ProjectRoot, StateDir: owner.paths.StateDir}
	current, err := readBinding(ctx, owner.paths.CommonDir)
	if err != nil {
		return err
	}
	if current != nil && *current != value {
		return fmt.Errorf("%w: shared project locator changed", ErrBindingMismatch)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	for _, setting := range [][2]string{{"xgoal.stateBinding", string(raw)}, {"xgoal.projectID", value.ProjectID}} {
		cmd := exec.CommandContext(ctx, "git", "config", "--file", filepath.Join(owner.paths.CommonDir, "config"), setting[0], setting[1])
		cmd.Env = GitEnvironment()
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("persist project locator: %s: %w", output, err)
		}
	}
	return nil
}

func (owner *Ownership) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil
	}
	owner.closed = true
	var result error
	for i := len(owner.files) - 1; i >= 0; i-- {
		result = errors.Join(result, unix.Flock(int(owner.files[i].Fd()), unix.LOCK_UN), owner.files[i].Close())
	}
	return result
}

// OwnershipHeld probes existing locks without creating or changing files.
func OwnershipHeld(paths Paths) (bool, error) {
	if err := validatePaths(context.Background(), paths); err != nil {
		return false, err
	}
	for _, path := range []string{filepath.Join(paths.CommonDir, "xgoal", "owner.lock"), filepath.Join(paths.StateDir, "run", "daemon.lock")} {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if err := safeLock(fd); err != nil {
			_ = unix.Close(fd)
			return false, err
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			_ = unix.Flock(fd, unix.LOCK_UN)
		}
		_ = unix.Close(fd)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
	}
	return false, nil
}

func privateDirectory(path string) error {
	// Do not follow a linked state root or lock directory while opening a lock.
	for current := path; current != filepath.Dir(current); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe ownership directory: %s", current)
		}
	}
	if err := ownedDirectory(path, true); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	if err := ownedDirectory(path, false); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}

func validatePaths(ctx context.Context, paths Paths) error {
	for _, path := range []string{paths.ProjectRoot, paths.WorktreeRoot, paths.CommonDir, paths.StateDir, paths.RunDir, paths.SocketPath} {
		if !validStoredPath(path) {
			return fmt.Errorf("%w: invalid or missing path", ErrBindingMismatch)
		}
	}
	current, err := Resolve(ctx, paths.WorktreeRoot, paths.StateDir, paths.SocketPath)
	if err != nil {
		return err
	}
	if current != paths {
		return fmt.Errorf("%w: project changed or paths were not resolved for this repository", ErrBindingMismatch)
	}
	return nil
}

func ownedDirectory(path string, allowMissing bool) error {
	var info unix.Stat_t
	err := unix.Lstat(path, &info)
	if allowMissing && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("ownership directory must be a real directory owned by the current user: %s", path)
	}
	return nil
}

func safeLock(fd int) error {
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Uid != uint32(os.Geteuid()) || info.Nlink != 1 {
		return errors.New("ownership lock must be a regular file owned exclusively by the current user")
	}
	return nil
}
