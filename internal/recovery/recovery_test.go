package recovery_test

import (
	"context"
	"errors"
	"testing"

	"github.com/monshunter/xgoal/internal/recovery"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

type workerStore struct {
	workers  []sqlite.WorkerProcess
	resolved map[string]sqlite.WorkerState
}

func (store *workerStore) RecoverableWorkers(context.Context) ([]sqlite.WorkerProcess, error) {
	return store.workers, nil
}

func (store *workerStore) ResolveWorkerRecovery(_ context.Context, attemptID string, _ int64, state sqlite.WorkerState, _ string, _ sqlite.EventInput) (sqlite.WorkerProcess, error) {
	store.resolved[attemptID] = state
	return sqlite.WorkerProcess{AttemptID: attemptID, State: state}, nil
}

type inspector struct {
	identities map[int]struct {
		identity string
		pgid     int
	}
	terminated []int
}

func (inspector *inspector) Identity(pid int) (string, int, error) {
	identity, ok := inspector.identities[pid]
	if !ok {
		return "", 0, errors.New("gone")
	}
	return identity.identity, identity.pgid, nil
}

func (inspector *inspector) TerminateGroup(_ context.Context, pgid int) error {
	inspector.terminated = append(inspector.terminated, pgid)
	return nil
}

func TestRecoveryTerminatesOnlyMatchingProcessIdentity(t *testing.T) {
	store := &workerStore{workers: []sqlite.WorkerProcess{
		{AttemptID: "match", PID: 10, PGID: 10, StartIdentity: "start-a", State: sqlite.WorkerRunning, Version: 1},
		{AttemptID: "reused", PID: 11, PGID: 11, StartIdentity: "old", State: sqlite.WorkerRunning, Version: 1},
		{AttemptID: "gone", PID: 12, PGID: 12, StartIdentity: "gone", State: sqlite.WorkerRunning, Version: 1},
	}, resolved: make(map[string]sqlite.WorkerState)}
	processes := &inspector{identities: map[int]struct {
		identity string
		pgid     int
	}{10: {"start-a", 10}, 11: {"new", 11}}}
	manager, err := recovery.New(store, processes)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(processes.terminated) != 1 || processes.terminated[0] != 10 {
		t.Fatalf("terminated = %v", processes.terminated)
	}
	if store.resolved["match"] != sqlite.WorkerTerminated || store.resolved["reused"] != sqlite.WorkerLost || store.resolved["gone"] != sqlite.WorkerLost {
		t.Fatalf("resolved = %+v", store.resolved)
	}
}
