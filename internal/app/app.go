package app

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/control"
	"github.com/monshunter/xgoal/internal/daemon"
	"github.com/monshunter/xgoal/internal/recovery"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

type Paths struct {
	ProjectRoot string
	StateDir    string
	RunDir      string
	SocketPath  string
}

func ResolvePaths(projectRoot, stateDir, socketPath string) (Paths, error) {
	if projectRoot == "" {
		return Paths{}, errors.New("project root is required")
	}
	absoluteRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return Paths{}, err
	}
	absoluteRoot = filepath.Clean(absoluteRoot)
	if stateDir == "" {
		stateDir = filepath.Join(absoluteRoot, ".xgoal")
	} else if !filepath.IsAbs(stateDir) {
		stateDir = filepath.Join(absoluteRoot, stateDir)
	}
	stateDir = filepath.Clean(stateDir)
	runDir := filepath.Join(stateDir, "run")
	if socketPath == "" {
		socketPath = filepath.Join(runDir, "xgoal.sock")
	} else if !filepath.IsAbs(socketPath) {
		socketPath = filepath.Join(absoluteRoot, socketPath)
	}
	return Paths{ProjectRoot: absoluteRoot, StateDir: stateDir, RunDir: runDir, SocketPath: filepath.Clean(socketPath)}, nil
}

func Serve(ctx context.Context, paths Paths) (returnErr error) {
	store, err := sqlite.Open(ctx, paths.StateDir, clock.Real{})
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, store.Close()) }()
	service, err := control.New(store, paths.ProjectRoot)
	if err != nil {
		return err
	}
	handler, err := api.NewHandler(service, store)
	if err != nil {
		return err
	}
	recoveryManager, err := recovery.New(store, recovery.OSInspector{GracePeriod: 2 * time.Second})
	if err != nil {
		return err
	}
	server, err := daemon.New(daemon.Config{RunDir: paths.RunDir, SocketPath: paths.SocketPath, ShutdownTimeout: 5 * time.Second}, handler, recoveryManager)
	if err != nil {
		return err
	}
	return server.Serve(ctx)
}
