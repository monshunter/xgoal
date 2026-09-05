package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/daemon"
	"github.com/monshunter/xgoal/internal/project"
	"golang.org/x/sys/unix"
)

type DaemonStatus struct {
	State         string              `json:"state"`
	ProjectID     string              `json:"project_id"`
	ProjectRoot   string              `json:"project_root"`
	StateDir      string              `json:"state_dir"`
	SocketPath    string              `json:"socket_path"`
	OwnershipHeld bool                `json:"ownership_held"`
	Identity      *api.DaemonIdentity `json:"daemon,omitempty"`
	Detail        string              `json:"detail,omitempty"`
}

func Status(ctx context.Context, paths Paths) (DaemonStatus, error) {
	status := DaemonStatus{ProjectID: paths.ProjectID, ProjectRoot: paths.ProjectRoot, StateDir: paths.StateDir, SocketPath: paths.SocketPath}
	held, err := project.OwnershipHeld(paths)
	if err != nil {
		status.State = "UNREACHABLE"
		status.Detail = err.Error()
		return status, err
	}
	status.OwnershipHeld = held
	client, err := api.NewProjectClient(paths.SocketPath, time.Second, ExpectedIdentity(paths))
	if err != nil {
		return status, err
	}
	defer client.Close()
	probeContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	identity, err := client.Handshake(probeContext)
	if err == nil {
		status.State = identity.State
		status.Identity = &identity
		return status, nil
	}
	status.Detail = err.Error()
	if errors.Is(err, api.ErrIdentityMismatch) {
		status.State = "IDENTITY_MISMATCH"
		return status, err
	}
	if errors.Is(err, api.ErrDaemonAccessDenied) {
		status.State = "UNREACHABLE"
		return status, err
	}
	if !held && (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)) {
		status.State = "STOPPED"
		status.Detail = ""
		return status, nil
	}
	status.State = "UNREACHABLE"
	return status, nil
}

type StartResult struct {
	DaemonStatus
	AlreadyRunning bool   `json:"already_running"`
	LogPath        string `json:"log_path,omitempty"`
}

// Start launches the same foreground server in a detached session and waits for a real handshake.
func Start(ctx context.Context, paths Paths, executable string) (result StartResult, returnErr error) {
	status, err := Status(ctx, paths)
	result.DaemonStatus = status
	if err != nil {
		return result, err
	}
	if status.State == "READY" {
		result.AlreadyRunning = true
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return result, err
		}
	}
	if err := daemon.EnsureRuntimeDirectory(paths.RunDir); err != nil {
		return result, err
	}
	result.LogPath = filepath.Join(paths.RunDir, "daemon.log")
	log, err := openDaemonLog(result.LogPath)
	if err != nil {
		return result, err
	}
	defer log.Close()
	command := exec.Command(executable, "daemon", "serve", "--project", paths.ProjectRoot, "--state-dir", paths.StateDir, "--socket", paths.SocketPath)
	command.Stdin = nil
	command.Stdout = log
	command.Stderr = log
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return result, fmt.Errorf("start daemon: %w", err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- command.Wait() }()
	childExited := false
	var childErr error
	defer func() {
		if returnErr != nil && !childExited {
			// This is the exact child handle created by this invocation, never a PID file.
			_ = command.Process.Signal(syscall.SIGTERM)
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			select {
			case <-childDone:
			case <-timer.C:
				_ = command.Process.Kill()
				<-childDone
			}
		}
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, statusErr := Status(ctx, paths)
		result.DaemonStatus = status
		if statusErr != nil {
			return result, statusErr
		}
		if status.State == "READY" {
			result.AlreadyRunning = status.Identity.PID != command.Process.Pid
			return result, nil
		}
		if childExited && !status.OwnershipHeld {
			return result, fmt.Errorf("daemon child exited before readiness (%v); inspect %s", childErr, result.LogPath)
		}
		select {
		case childErr = <-childDone:
			childExited = true
		case <-ctx.Done():
			return result, fmt.Errorf("daemon did not become ready: %w; inspect %s", ctx.Err(), result.LogPath)
		case <-ticker.C:
		}
	}
}

// Stop addresses the observed instance and waits until it no longer owns the project.
func Stop(ctx context.Context, paths Paths) (DaemonStatus, error) {
	status, err := Status(ctx, paths)
	if err != nil {
		return status, err
	}
	if status.State == "STOPPED" {
		return status, nil
	}
	if status.Identity == nil {
		return status, errors.New("daemon owns the project or is unreachable; no verified instance can be stopped")
	}
	instance := status.Identity.InstanceID
	client, err := api.NewProjectClient(paths.SocketPath, time.Second, ExpectedIdentity(paths))
	if err != nil {
		return status, err
	}
	defer client.Close()
	verified, err := client.Handshake(ctx)
	if err != nil {
		return status, err
	}
	if verified.InstanceID != instance {
		return status, errors.New("daemon instance changed before stop; inspect its status")
	}
	code, _, err := client.Do(ctx, http.MethodPost, "/v1/daemon/stop", "", nil)
	if err != nil {
		return status, err
	}
	if code != http.StatusAccepted {
		return status, fmt.Errorf("daemon stop rejected: HTTP %d", code)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		observed, probeErr := Status(ctx, paths)
		if probeErr != nil && !errors.Is(probeErr, api.ErrIdentityMismatch) {
			return observed, probeErr
		}
		if !observed.OwnershipHeld && observed.Identity == nil {
			observed.State = "STOPPED"
			observed.Detail = ""
			return observed, nil
		}
		if observed.Identity != nil && observed.Identity.InstanceID != instance {
			return observed, nil
		}
		select {
		case <-ctx.Done():
			return observed, fmt.Errorf("waiting for daemon ownership release: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func openDaemonLog(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || int(stat.Uid) != os.Getuid() || stat.Nlink != 1 {
		file.Close()
		return nil, errors.New("daemon log must be a regular file owned by this user")
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
