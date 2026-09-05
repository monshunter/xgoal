package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/domain"
)

type atomicBackend struct {
	backend
	accepts          int
	scope, key, body string
	model            any
}

func (b *atomicBackend) AcceptGoal(_ context.Context, operation api.Operation, scope, key string, model any) (domain.IdempotencyRecord, bool, error) {
	b.accepts++
	b.scope, b.key, b.body, b.model = scope, key, string(operation.Body), model
	return domain.IdempotencyRecord{State: domain.IdempotencyCompleted, ResponseStatus: 201, ResponseJSON: []byte(`{"goal_id":"g1","state":"DRAFT","planning_state":"QUEUED"}`)}, b.accepts == 1, nil
}

type countIdempotency struct {
	api.Idempotency
	begins, completes    int
	completionContextErr error
	completionBounded    bool
}

func (i *countIdempotency) BeginIdempotentRequest(ctx context.Context, scope, key string, model any) (domain.IdempotencyRecord, bool, error) {
	i.begins++
	return i.Idempotency.BeginIdempotentRequest(ctx, scope, key, model)
}
func (i *countIdempotency) CompleteIdempotentRequest(ctx context.Context, scope, key, hash string, status int, model any) (domain.IdempotencyRecord, error) {
	i.completes++
	i.completionContextErr = ctx.Err()
	deadline, ok := ctx.Deadline()
	i.completionBounded = ok && time.Until(deadline) > 0 && time.Until(deadline) <= 10*time.Second
	if ctx.Err() != nil {
		return domain.IdempotencyRecord{}, ctx.Err()
	}
	return i.Idempotency.CompleteIdempotentRequest(ctx, scope, key, hash, status, model)
}
func TestGoalAcceptanceBypassesSplitIdempotencyAndPreservesRawRequest(t *testing.T) {
	b := &atomicBackend{}
	journal := &countIdempotency{Idempotency: newMemoryIdempotency(nil)}
	handler, err := api.NewHandler(b, journal)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"goal_id":"g1","raw_goal":"create file"}`
	for n := 0; n < 2; n++ {
		request := httptest.NewRequest(http.MethodPost, "/v1/goals", strings.NewReader(body))
		request.Header.Set("Idempotency-Key", "same")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 201 || !strings.Contains(response.Body.String(), `"planning_state":"QUEUED"`) {
			t.Fatalf("acceptance=%d %s", response.Code, response.Body.String())
		}
		if n == 1 && response.Header().Get("Idempotent-Replay") != "true" {
			t.Fatal("missing replay header")
		}
	}
	if b.accepts != 2 || b.writes != 0 || journal.begins != 0 || journal.completes != 0 {
		t.Fatalf("accepts=%d writes=%d begin=%d complete=%d", b.accepts, b.writes, journal.begins, journal.completes)
	}
	encoded, _ := json.Marshal(b.model)
	if b.scope != "POST /v1/goals" || b.key != "same" || b.body != body || string(encoded) != body {
		t.Fatalf("request changed: %s %s %s %s", b.scope, b.key, b.body, encoded)
	}
}

type cancelBackend struct {
	backend
	cancel context.CancelFunc
}

func (b *cancelBackend) Execute(context.Context, api.Operation) (int, any, error) {
	b.cancel()
	return 200, map[string]any{"cancelled": true}, nil
}
func TestCancelledRequestPersistsCompletedResponseWithBoundedCleanupContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &cancelBackend{cancel: cancel}
	journal := &countIdempotency{Idempotency: newMemoryIdempotency(nil)}
	handler, err := api.NewHandler(b, journal)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/doctor/active-probes", strings.NewReader(`{}`)).WithContext(ctx)
	request.Header.Set("Idempotency-Key", "probe-cancel")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || journal.completes != 1 || journal.completionContextErr != nil || !journal.completionBounded {
		t.Fatalf("completion=%d count=%d context=%v body=%s", response.Code, journal.completes, journal.completionContextErr, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/doctor/active-probes", strings.NewReader(`{}`))
	request.Header.Set("Idempotency-Key", "probe-cancel")
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, request)
	if replay.Code != 200 || replay.Header().Get("Idempotent-Replay") != "true" {
		t.Fatalf("cancelled response not replayable: %d %s", replay.Code, replay.Body.String())
	}
}
