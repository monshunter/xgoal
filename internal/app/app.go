package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/control"
	"github.com/monshunter/xgoal/internal/daemon"
	"github.com/monshunter/xgoal/internal/finalize"
	"github.com/monshunter/xgoal/internal/orchestrator"
	"github.com/monshunter/xgoal/internal/project"
	"github.com/monshunter/xgoal/internal/recovery"
	finalreport "github.com/monshunter/xgoal/internal/report"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

type Paths = project.Paths

func ResolvePaths(projectRoot, stateDir, socketPath string) (Paths, error) {
	return project.Resolve(context.Background(), projectRoot, stateDir, socketPath)
}

func ExpectedIdentity(paths Paths) api.ExpectedIdentity {
	return api.ExpectedIdentity{ProjectID: paths.ProjectID, RepositoryIdentity: paths.RepositoryIdentity, ProjectRoot: paths.ProjectRoot, StateDir: paths.StateDir}
}

func Serve(ctx context.Context, paths Paths) (returnErr error) {
	ownership, err := project.Acquire(ctx, paths)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, ownership.Close()) }()
	binding := sqlite.ProjectBinding{ProjectID: paths.ProjectID, CommonDir: paths.CommonDir, ProjectRoot: paths.ProjectRoot}
	if err := sqlite.CheckProjectBinding(ctx, paths.StateDir, binding, paths.LegacyState); err != nil {
		return err
	}
	if err := ownership.Bind(ctx); err != nil {
		return err
	}
	store, err := sqlite.OpenProject(ctx, paths.StateDir, clock.Real{}, binding, paths.LegacyState)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, store.Close()) }()
	runtimeContext, cancelRuntime := context.WithCancel(ctx)
	defer cancelRuntime()
	ctx = runtimeContext
	service, err := control.New(store, paths.ProjectRoot)
	if err != nil {
		return err
	}
	var executionEngine *orchestrator.Engine
	if configuration, executable := service.ExecutionConfiguration(); executable {
		executionEngine, err = orchestrator.New(ctx, store, paths.ProjectRoot, configuration)
		if err != nil {
			return err
		}
		service.SetLifecycle(executionEngine)
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
	recoveryManager := recoveryChain{recovery.ProcessManager{Store: store, GracePeriod: 2 * time.Second}, workerRecovery, storeRecovery{store: store}, reportRecovery}
	if executionEngine != nil {
		recoveryManager = append(recoveryManager, engineRecovery{engine: executionEngine})
	} else {
		recoveryManager = append(recoveryManager, planningRecovery{store: store})
	}
	instanceBytes := make([]byte, 16)
	if _, err := rand.Read(instanceBytes); err != nil {
		return err
	}
	identity := api.DaemonIdentity{ProtocolVersion: api.ProtocolVersion, SoftwareVersion: api.SoftwareVersion, ProjectID: paths.ProjectID, RepositoryIdentity: paths.RepositoryIdentity, ProjectRoot: paths.ProjectRoot, StateDir: paths.StateDir, InstanceID: hex.EncodeToString(instanceBytes), PID: os.Getpid(), StartedAt: time.Now().UTC().Format(time.RFC3339Nano), State: "STARTING"}
	server, err := daemon.New(daemon.Config{RunDir: paths.RunDir, SocketPath: paths.SocketPath, ShutdownTimeout: 5 * time.Second, Identity: identity, RequestStop: cancelRuntime}, handler, recoveryManager)
	if err != nil {
		return err
	}
	if err := server.Prepare(ctx); err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, server.Close()) }()
	engineDone := make(chan struct{})
	if executionEngine != nil {
		go func() { defer close(engineDone); executionEngine.Run(ctx) }()
	} else {
		close(engineDone)
	}
	defer func() { cancelRuntime(); <-engineDone }()
	return server.Serve(ctx)
}

type engineRecovery struct{ engine *orchestrator.Engine }

type storeRecovery struct{ store *sqlite.Store }

type planningRecovery struct{ store *sqlite.Store }

func (recovery planningRecovery) Recover(ctx context.Context) error {
	return recovery.store.RecoverPlanning(ctx, sqlite.PlanningRecoveryOptions{})
}

func (recovery storeRecovery) Recover(ctx context.Context) error {
	if err := recovery.store.ReconcileLegacyExecution(ctx); err != nil {
		return err
	}
	return recovery.store.ReconcileProcessAttempts(ctx)
}

func (recovery engineRecovery) Recover(ctx context.Context) error {
	if err := recovery.engine.Recover(ctx); err != nil {
		return err
	}
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
