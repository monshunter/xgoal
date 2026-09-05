package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

type backend struct {
	mu     sync.Mutex
	writes int
	events []domain.Event
}

func (b *backend) Execute(_ context.Context, operation api.Operation) (int, any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writes++
	return http.StatusCreated, map[string]any{"operation": operation.Name, "writes": b.writes}, nil
}

func (b *backend) Query(_ context.Context, operation api.Operation) (int, any, error) {
	return http.StatusOK, map[string]any{"operation": operation.Name}, nil
}

func (b *backend) Events(_ context.Context, _ string, after string, _ int) ([]domain.Event, error) {
	if after == "" {
		return append([]domain.Event(nil), b.events...), nil
	}
	for index, event := range b.events {
		if event.ID == after {
			return append([]domain.Event(nil), b.events[index+1:]...), nil
		}
	}
	return nil, nil
}

func TestAllSpecifiedEndpointsAreRouted(t *testing.T) {
	tests := []struct {
		method, path, operation string
		write                   bool
	}{
		{http.MethodPost, "/v1/projects/init", "project.init", true},
		{http.MethodPost, "/v1/goals", "goal.create", true},
		{http.MethodGet, "/v1/doctor", "doctor", false},
		{http.MethodPost, "/v1/doctor/active-probes", "doctor.active-probe", true},
		{http.MethodGet, "/v1/goals/g1", "goal.get", false},
		{http.MethodPost, "/v1/goals/g1/pause", "goal.pause", true},
		{http.MethodPost, "/v1/goals/g1/resume", "goal.resume", true},
		{http.MethodPost, "/v1/goals/g1/cancel", "goal.cancel", true},
		{http.MethodPost, "/v1/goals/g1/replan", "goal.replan", true},
		{http.MethodPost, "/v1/goals/g1/plan", "goal.plan", true},
		{http.MethodGet, "/v1/goals/g1/work-items", "goal.work-items", false},
		{http.MethodGet, "/v1/goals/g1/events", "goal.events", false},
		{http.MethodGet, "/v1/goals/g1/gates", "goal.gates", false},
		{http.MethodPost, "/v1/gates/gate1/decisions", "gate.decide", true},
		{http.MethodGet, "/v1/attempts/a1/logs", "attempt.logs", false},
		{http.MethodPost, "/v1/work-items/w1/retry", "work.retry", true},
		{http.MethodPost, "/v1/work-items/w1/cancel", "work.cancel", true},
		{http.MethodGet, "/v1/goals/g1/report", "goal.report", false},
		{http.MethodPost, "/v1/goals/g1/finalize", "goal.finalize", true},
		{http.MethodPost, "/v1/projects/p1/clean", "project.clean", true},
	}
	for _, test := range tests {
		t.Run(test.operation, func(t *testing.T) {
			state := basestore.NewMemory(clock.Real{})
			backend := &backend{}
			handler, err := api.NewHandler(backend, newMemoryIdempotency(state))
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(test.method, test.path, nil)
			if test.write {
				request.Header.Set("Idempotency-Key", "request-1")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code == http.StatusNotFound {
				t.Fatalf("endpoint not routed: %s %s", test.method, test.path)
			}
			if !containsOperation(response.Body.Bytes(), test.operation) {
				t.Fatalf("response %s does not contain operation %q", response.Body.String(), test.operation)
			}
		})
	}
}

func TestWriteRequiresIdempotencyAndReplaysResponse(t *testing.T) {
	backend := &backend{}
	idempotency := newMemoryIdempotency(nil)
	handler, _ := api.NewHandler(backend, idempotency)
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/v1/goals", nil))
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing key status = %d", missing.Code)
	}
	var firstBody string
	for index := 0; index < 2; index++ {
		request := httptest.NewRequest(http.MethodPost, "/v1/goals", nil)
		request.Header.Set("Idempotency-Key", "same")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
		if index == 0 {
			firstBody = response.Body.String()
		} else if response.Body.String() != firstBody {
			t.Fatalf("replay changed: %s != %s", response.Body.String(), firstBody)
		}
	}
	if backend.writes != 1 {
		t.Fatalf("backend writes = %d, want 1", backend.writes)
	}
}

func TestNDJSONEventStreamResumesAfterEventID(t *testing.T) {
	backend := &backend{events: []domain.Event{{ID: "event-1", AggregateType: "goal", AggregateID: "g1", Sequence: 1}, {ID: "event-2", AggregateType: "goal", AggregateID: "g1", Sequence: 2}}}
	handler, _ := api.NewHandler(backend, newMemoryIdempotency(nil))
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/goals/g1/events?watch=1&after_event_id=event-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	cancel()
	_ = response.Body.Close()
	if err != nil || !strings.Contains(line, `"ID":"event-2"`) || strings.Contains(line, `event-1`) {
		t.Fatalf("resumed line=%q err=%v", line, err)
	}
}

func containsOperation(body []byte, expected string) bool {
	var model map[string]any
	if json.Unmarshal(body, &model) != nil {
		return false
	}
	value, _ := model["operation"].(string)
	return value == expected
}

type memoryIdempotency struct {
	mu      sync.Mutex
	records map[string]domain.IdempotencyRecord
}

func newMemoryIdempotency(_ *basestore.Memory) *memoryIdempotency {
	return &memoryIdempotency{records: make(map[string]domain.IdempotencyRecord)}
}

func (m *memoryIdempotency) BeginIdempotentRequest(_ context.Context, scope, key string, request any) (domain.IdempotencyRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	identity := scope + "\x00" + key
	if record, exists := m.records[identity]; exists {
		return record, false, nil
	}
	encoded, _ := json.Marshal(request)
	record := domain.IdempotencyRecord{Scope: scope, Key: key, RequestJSON: encoded, RequestHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: domain.IdempotencyInProgress}
	m.records[identity] = record
	return record, true, nil
}

func (m *memoryIdempotency) CompleteIdempotentRequest(_ context.Context, scope, key, _ string, status int, response any) (domain.IdempotencyRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	identity := scope + "\x00" + key
	record := m.records[identity]
	record.State, record.ResponseStatus = domain.IdempotencyCompleted, status
	record.ResponseJSON, _ = json.Marshal(response)
	record.ResponseHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	m.records[identity] = record
	return record, nil
}
