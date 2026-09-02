package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestConcurrentIdempotencyBeginsHaveOneCreatorAndOneIdentity(t *testing.T) {
	t.Parallel()

	const contenders = 32
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), clock.Real{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	type result struct {
		record  domain.IdempotencyRecord
		created bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, contenders)
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			record, created, err := store.BeginIdempotentRequest(ctx, "POST /v1/goals", "same-request", map[string]any{"goal": "same"})
			results <- result{record: record, created: created, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	creators := 0
	requestHash := ""
	for result := range results {
		if result.err != nil {
			t.Fatalf("BeginIdempotentRequest() error = %v", result.err)
		}
		if result.created {
			creators++
		}
		if requestHash == "" {
			requestHash = result.record.RequestHash
		}
		if result.record.RequestHash != requestHash || result.record.State != domain.IdempotencyInProgress {
			t.Fatalf("idempotency result = %+v", result.record)
		}
	}
	if creators != 1 {
		t.Fatalf("idempotency creators = %d, want 1", creators)
	}
}

func TestIdempotencyRecordReplaysSameRequestAndRejectsMismatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	store, err := Open(ctx, projectDir, clock.NewFake(time.Date(2026, 9, 2, 11, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	record, created, err := store.BeginIdempotentRequest(ctx, "POST /v1/goals", "request_1", map[string]any{"b": 2, "a": 1})
	if err != nil {
		t.Fatalf("BeginIdempotentRequest() error = %v", err)
	}
	if !created || record.State != domain.IdempotencyInProgress || string(record.RequestJSON) != `{"a":1,"b":2}` || record.RequestHash == "" {
		t.Fatalf("created idempotency record = %+v, created=%t", record, created)
	}
	replayed, created, err := store.BeginIdempotentRequest(ctx, "POST /v1/goals", "request_1", map[string]any{"a": 1, "b": 2})
	if err != nil {
		t.Fatalf("replay BeginIdempotentRequest() error = %v", err)
	}
	if created || replayed.RequestHash != record.RequestHash {
		t.Fatalf("replayed record = %+v, created=%t", replayed, created)
	}
	if _, _, err := store.BeginIdempotentRequest(ctx, "POST /v1/goals", "request_1", map[string]any{"a": 9}); !errors.Is(err, basestore.ErrIdempotencyConflict) {
		t.Fatalf("mismatched replay error = %v, want ErrIdempotencyConflict", err)
	}

	completed, err := store.CompleteIdempotentRequest(ctx, record.Scope, record.Key, record.RequestHash, 201, map[string]any{"goal_id": "goal_1"})
	if err != nil {
		t.Fatalf("CompleteIdempotentRequest() error = %v", err)
	}
	if completed.State != domain.IdempotencyCompleted || completed.ResponseStatus != 201 || string(completed.ResponseJSON) != `{"goal_id":"goal_1"}` || completed.ResponseHash == "" {
		t.Fatalf("completed record = %+v", completed)
	}
	completedReplay, err := store.CompleteIdempotentRequest(ctx, record.Scope, record.Key, record.RequestHash, 201, map[string]any{"goal_id": "goal_1"})
	if err != nil {
		t.Fatalf("completed replay error = %v", err)
	}
	if completedReplay.ResponseHash != completed.ResponseHash {
		t.Fatalf("completed replay = %+v", completedReplay)
	}
	if _, err := store.CompleteIdempotentRequest(ctx, record.Scope, record.Key, record.RequestHash, 200, map[string]any{"goal_id": "other"}); !errors.Is(err, basestore.ErrIdempotencyConflict) {
		t.Fatalf("different response replay error = %v, want ErrIdempotencyConflict", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, projectDir, clock.Real{})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	persisted, created, err := reopened.BeginIdempotentRequest(ctx, record.Scope, record.Key, map[string]any{"a": 1, "b": 2})
	if err != nil {
		t.Fatalf("reopen replay error = %v", err)
	}
	if created || persisted.State != domain.IdempotencyCompleted || persisted.ResponseHash != completed.ResponseHash {
		t.Fatalf("persisted replay = %+v, created=%t", persisted, created)
	}
}

func TestEffectJournalDeduplicatesRequestAndRequiresObservation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), clock.NewFake(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	request := EffectRequest{
		ID:      "effect_1",
		Key:     "project_1/goal_1/work_1/attempt_1/workspace-create/1",
		Type:    "WORKSPACE_CREATE",
		Request: map[string]any{"path": "/tmp/work_1", "base_tree": "abc123"},
	}
	effect, created, err := store.RequestEffect(ctx, request, EventInput{Type: "WorkspaceCreateRequested", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("RequestEffect() error = %v", err)
	}
	if !created || effect.State != domain.EffectRequested || effect.Version != 1 || string(effect.RequestJSON) != `{"base_tree":"abc123","path":"/tmp/work_1"}` {
		t.Fatalf("requested effect = %+v, created=%t", effect, created)
	}
	replayed, created, err := store.RequestEffect(ctx, EffectRequest{
		ID:      "effect_different_id",
		Key:     request.Key,
		Type:    request.Type,
		Request: map[string]any{"base_tree": "abc123", "path": "/tmp/work_1"},
	}, EventInput{Type: "ShouldNotBeWritten", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("effect replay error = %v", err)
	}
	if created || replayed.ID != effect.ID || replayed.RequestHash != effect.RequestHash {
		t.Fatalf("replayed effect = %+v, created=%t", replayed, created)
	}
	if _, _, err := store.RequestEffect(ctx, EffectRequest{
		ID: "effect_2", Key: request.Key, Type: request.Type, Request: map[string]any{"path": "/tmp/other"},
	}, EventInput{Type: "Mismatch", ActorType: "kernel", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrIdempotencyConflict) {
		t.Fatalf("effect request mismatch error = %v, want ErrIdempotencyConflict", err)
	}

	effect, err = store.UpdateEffect(ctx, effect.ID, 1, domain.EffectExecuting, nil, EventInput{Type: "WorkspaceCreateExecuting", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("move effect to executing: %v", err)
	}
	effect, err = store.UpdateEffect(ctx, effect.ID, 2, domain.EffectObserving, nil, EventInput{Type: "WorkspaceCreateObserving", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("move effect to observing: %v", err)
	}
	if _, err := store.UpdateEffect(ctx, effect.ID, 3, domain.EffectSucceeded, nil, EventInput{Type: "InvalidSuccess", ActorType: "kernel", Payload: map[string]any{}}); err == nil {
		t.Fatal("effect succeeded without observation")
	}
	effect, err = store.UpdateEffect(ctx, effect.ID, 3, domain.EffectSucceeded, map[string]any{
		"path": "/tmp/work_1", "tree": "abc123", "read_back": true,
	}, EventInput{Type: "WorkspaceCreated", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("complete observed effect: %v", err)
	}
	if effect.State != domain.EffectSucceeded || effect.Version != 4 || effect.ObservationHash == "" || string(effect.ObservationJSON) != `{"path":"/tmp/work_1","read_back":true,"tree":"abc123"}` {
		t.Fatalf("completed effect = %+v", effect)
	}
	events, err := store.Events(ctx, "effect", effect.ID)
	if err != nil {
		t.Fatalf("effect Events() error = %v", err)
	}
	if len(events) != 4 || events[3].Sequence != 4 {
		t.Fatalf("effect Events() = %+v", events)
	}
}

func TestEffectRequestRollsBackWhenEventFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project"), clock.Real{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER reject_effect_event
BEFORE INSERT ON events
WHEN NEW.event_type = 'RejectEffectForTest'
BEGIN
    SELECT RAISE(ABORT, 'rejected for effect atomicity test');
END;`); err != nil {
		t.Fatalf("create rejection trigger: %v", err)
	}

	request := EffectRequest{ID: "effect_1", Key: "project/goal/work/attempt/type/1", Type: "TEST", Request: map[string]any{"x": 1}}
	if _, _, err := store.RequestEffect(ctx, request, EventInput{Type: "RejectEffectForTest", ActorType: "kernel", Payload: map[string]any{}}); err == nil {
		t.Fatal("RequestEffect() unexpectedly succeeded")
	}
	if _, err := store.Effect(ctx, request.ID); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("Effect() after event failure error = %v, want ErrNotFound", err)
	}
}
