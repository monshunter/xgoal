package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/control"
	"github.com/monshunter/xgoal/internal/daemon"
	"github.com/monshunter/xgoal/internal/finalize"
	"github.com/monshunter/xgoal/internal/orchestrator"
	"github.com/monshunter/xgoal/internal/recovery"
	finalreport "github.com/monshunter/xgoal/internal/report"
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
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return Paths{}, err
	}
	absoluteRoot = filepath.Clean(resolvedRoot)
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
	var executionEngine *orchestrator.Engine
	if configuration, loadErr := config.LoadFile(filepath.Join(paths.ProjectRoot, "xgoal.yaml")); loadErr == nil {
		executionEngine, err = orchestrator.New(ctx, store, paths.ProjectRoot, configuration)
		if err != nil {
			return err
		}
		service.SetLifecycle(executionEngine)
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return fmt.Errorf("load xgoal.yaml: %w", loadErr)
	}
	handler, err := api.NewHandler(service, store)
	if err != nil {
		return err
	}
	workerRecovery, err := recovery.New(store, recovery.OSInspector{GracePeriod: 2 * time.Second})
	if err != nil {
		return err
	}
	files, err := finalreport.NewFileManager(paths.StateDir)
	if err != nil {
		return err
	}
	reportRecovery, err := finalize.New(store, files)
	if err != nil {
		return err
	}
	recoveryManager := recoveryChain{workerRecovery, reportRecovery}
	if executionEngine != nil {
		recoveryManager = append(recoveryManager, engineRecovery{engine: executionEngine})
	}
	server, err := daemon.New(daemon.Config{RunDir: paths.RunDir, SocketPath: paths.SocketPath, ShutdownTimeout: 5 * time.Second}, handler, recoveryManager)
	if err != nil {
		return err
	}
	return server.Serve(ctx)
}

type engineRecovery struct{ engine *orchestrator.Engine }

func (recovery engineRecovery) Recover(ctx context.Context) error {
	if err := recovery.engine.Recover(ctx); err != nil {
		return err
	}
	go recovery.engine.Run(ctx)
	return nil
}

type recoveryChain []daemon.Recovery

func (chain recoveryChain) Recover(ctx context.Context) error {
	for _, recovery := range chain {
		if err := recovery.Recover(ctx); err != nil {
			return err
		}
	}
	return nil
}
