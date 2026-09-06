package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

const maxRequestBytes = 1 << 20

type Operation struct {
	Name       string
	ResourceID string
	Body       json.RawMessage
	Query      map[string]string
}

type Backend interface {
	Execute(context.Context, Operation) (status int, response any, err error)
	Query(context.Context, Operation) (status int, response any, err error)
	Events(context.Context, string, string, int) ([]domain.Event, error)
}

type ResourceResolver interface {
	ResolveResource(context.Context, Operation) (Operation, error)
}

// AtomicGoalBackend commits the Goal, planning intent and acceptance response
// together. Other write operations retain the existing idempotency contract.
type AtomicGoalBackend interface {
	AcceptGoal(context.Context, Operation, string, string, any) (domain.IdempotencyRecord, bool, error)
}

type Idempotency interface {
	BeginIdempotentRequest(context.Context, string, string, any) (domain.IdempotencyRecord, bool, error)
	CompleteIdempotentRequest(context.Context, string, string, string, int, any) (domain.IdempotencyRecord, error)
}

type IdempotencyLookup interface {
	LookupIdempotentRequest(context.Context, string, string, any) (domain.IdempotencyRecord, error)
}

type Handler struct {
	backend     Backend
	idempotency Idempotency
	poll        time.Duration
}

func NewHandler(backend Backend, idempotency Idempotency) (*Handler, error) {
	if backend == nil || idempotency == nil {
		return nil, errors.New("API backend and idempotency store are required")
	}
	return &Handler{backend: backend, idempotency: idempotency, poll: 100 * time.Millisecond}, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	operation, write, ok := route(request)
	if !ok {
		writeError(writer, http.StatusNotFound, "NOT_FOUND", "endpoint not found")
		return
	}
	if write {
		handler.serveWrite(writer, request, operation)
		return
	}
	if operation.Name == "invocation.logs" && request.URL.Query().Get("watch") == "1" {
		handler.serveInvocationStream(writer, request, operation)
		return
	}
	if operation.Name == "goal.events" && request.URL.Query().Get("watch") == "1" {
		if resolver, ok := handler.backend.(ResourceResolver); ok {
			resolved, err := resolver.ResolveResource(request.Context(), operation)
			if err != nil {
				writeBackendError(writer, err)
				return
			}
			operation = resolved
		}
		handler.serveEventStream(writer, request, operation)
		return
	}
	status, response, err := handler.backend.Query(request.Context(), operation)
	if err != nil {
		writeBackendError(writer, err)
		return
	}
	writeJSON(writer, status, response)
}

func (handler *Handler) serveWrite(writer http.ResponseWriter, request *http.Request, operation Operation) {
	key := request.Header.Get("Idempotency-Key")
	if !validHeaderLabel(key) {
		writeError(writer, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "write requests require a non-empty single-line Idempotency-Key")
		return
	}
	body, model, err := readJSONBody(request.Body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	operation.Body = body
	scope := request.Method + " " + request.URL.Path
	if backend, ok := handler.backend.(AtomicGoalBackend); ok && operation.Name == "goal.create" {
		record, created, err := backend.AcceptGoal(request.Context(), operation, scope, key, model)
		if err != nil {
			writeBackendError(writer, err)
			return
		}
		if record.State != domain.IdempotencyCompleted || record.ResponseStatus < 100 || record.ResponseStatus > 599 || !json.Valid(record.ResponseJSON) {
			writeError(writer, http.StatusInternalServerError, "INVALID_ACCEPTANCE", "atomic Goal acceptance did not return a committed response")
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if !created {
			writer.Header().Set("Idempotent-Replay", "true")
		}
		writer.WriteHeader(record.ResponseStatus)
		_, _ = writer.Write(record.ResponseJSON)
		return
	}
	if resolver, ok := handler.backend.(ResourceResolver); ok {
		resolved, resolveErr := resolver.ResolveResource(request.Context(), operation)
		if resolveErr != nil {
			// A new ambiguous request must not even reserve idempotency state.
			// A completed request still replays its original result if the prefix
			// became ambiguous after that request committed.
			if lookup, ok := handler.idempotency.(IdempotencyLookup); ok {
				prior, lookupErr := lookup.LookupIdempotentRequest(request.Context(), scope, key, model)
				if lookupErr == nil && prior.State == domain.IdempotencyCompleted {
					writer.Header().Set("Content-Type", "application/json")
					writer.Header().Set("Idempotent-Replay", "true")
					writer.WriteHeader(prior.ResponseStatus)
					_, _ = writer.Write(prior.ResponseJSON)
					return
				}
				if lookupErr != nil && !errors.Is(lookupErr, basestore.ErrNotFound) {
					writeBackendError(writer, lookupErr)
					return
				}
			}
			writeBackendError(writer, resolveErr)
			return
		}
		operation = resolved
	}
	record, created, err := handler.idempotency.BeginIdempotentRequest(request.Context(), scope, key, model)
	if err != nil {
		writeBackendError(writer, err)
		return
	}
	if !created {
		if record.State == domain.IdempotencyCompleted {
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("Idempotent-Replay", "true")
			writer.WriteHeader(record.ResponseStatus)
			_, _ = writer.Write(record.ResponseJSON)
			return
		}
		writeError(writer, http.StatusConflict, "REQUEST_IN_PROGRESS", "matching request is still in progress")
		return
	}
	status, response, backendErr := handler.backend.Execute(request.Context(), operation)
	if backendErr != nil {
		status, response = ErrorResponse(backendErr)
	}
	completionContext, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 10*time.Second)
	defer cancel()
	completed, err := handler.idempotency.CompleteIdempotentRequest(completionContext, scope, key, record.RequestHash, status, response)
	if err != nil {
		writeBackendError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(completed.ResponseStatus)
	_, _ = writer.Write(completed.ResponseJSON)
}

func (handler *Handler) serveEventStream(writer http.ResponseWriter, request *http.Request, operation Operation) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, "STREAM_UNAVAILABLE", "response writer cannot stream")
		return
	}
	after := request.URL.Query().Get("after_event_id")
	writer.Header().Set("Content-Type", "application/x-ndjson")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	ticker := time.NewTicker(handler.poll)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	encoder := json.NewEncoder(writer)
	for {
		events, err := handler.backend.Events(request.Context(), operation.ResourceID, after, 100)
		if err != nil {
			_ = encoder.Encode(errorEnvelope("EVENT_STREAM_FAILED", err.Error()))
			flusher.Flush()
			return
		}
		for _, event := range events {
			if err := encoder.Encode(event); err != nil {
				return
			}
			after = event.ID
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		select {
		case <-request.Context().Done():
			return
		case <-ticker.C:
		case <-heartbeat.C:
			if err := encoder.Encode(map[string]any{"type": "heartbeat", "after_event_id": after}); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func route(request *http.Request) (Operation, bool, bool) {
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	query := make(map[string]string)
	for key, values := range request.URL.Query() {
		if len(values) > 0 {
			query[key] = values[0]
		}
	}
	operation := Operation{Query: query}
	if len(parts) == 3 && parts[0] == "v1" && parts[1] == "projects" && parts[2] == "init" && request.Method == http.MethodPost {
		operation.Name = "project.init"
		return operation, true, true
	}
	if len(parts) == 2 && parts[0] == "v1" && parts[1] == "goals" && request.Method == http.MethodPost {
		operation.Name = "goal.create"
		return operation, true, true
	}
	if len(parts) == 2 && parts[0] == "v1" && parts[1] == "goals" && request.Method == http.MethodGet {
		operation.Name = "goal.list"
		return operation, false, true
	}
	if len(parts) == 2 && parts[0] == "v1" && parts[1] == "identifiers" && request.Method == http.MethodGet {
		operation.Name = "identifiers"
		return operation, false, true
	}
	if len(parts) == 3 && parts[0] == "v1" && (parts[1] == "work-items" || parts[1] == "gates") && request.Method == http.MethodGet {
		operation.Name = "work.get"
		if parts[1] == "gates" {
			operation.Name = "gate.get"
		}
		operation.ResourceID = parts[2]
		return operation, false, true
	}
	if len(parts) == 2 && parts[0] == "v1" && parts[1] == "doctor" && request.Method == http.MethodGet {
		operation.Name = "doctor"
		return operation, false, true
	}
	if len(parts) == 3 && parts[0] == "v1" && parts[1] == "doctor" && parts[2] == "active-probes" && request.Method == http.MethodPost {
		operation.Name = "doctor.active-probe"
		return operation, true, true
	}
	if len(parts) == 3 && parts[0] == "v1" && parts[1] == "goals" && request.Method == http.MethodGet {
		operation.Name, operation.ResourceID = "goal.get", parts[2]
		return operation, false, true
	}
	if len(parts) == 4 && parts[0] == "v1" && parts[1] == "goals" {
		operation.ResourceID = parts[2]
		switch parts[3] {
		case "pause", "resume", "cancel", "plan", "replan", "finalize", "exports":
			if request.Method == http.MethodPost {
				operation.Name = "goal." + parts[3]
				return operation, true, true
			}
		case "work-items", "events", "gates", "report", "invocations":
			if request.Method == http.MethodGet {
				operation.Name = "goal." + parts[3]
				return operation, false, true
			}
		}
	}
	if len(parts) == 4 && parts[0] == "v1" && parts[1] == "gates" && parts[3] == "decisions" && request.Method == http.MethodPost {
		operation.Name, operation.ResourceID = "gate.decide", parts[2]
		return operation, true, true
	}
	if len(parts) == 4 && parts[0] == "v1" && parts[1] == "gates" && parts[3] == "resume" && request.Method == http.MethodPost {
		operation.Name, operation.ResourceID = "gate.resume", parts[2]
		return operation, true, true
	}
	if len(parts) == 4 && parts[0] == "v1" && parts[1] == "invocations" && request.Method == http.MethodGet && (parts[3] == "context" || parts[3] == "logs") {
		operation.Name, operation.ResourceID = "invocation."+parts[3], parts[2]
		return operation, false, true
	}
	if len(parts) == 4 && parts[0] == "v1" && parts[1] == "attempts" && parts[3] == "logs" && request.Method == http.MethodGet {
		operation.Name, operation.ResourceID = "attempt.logs", parts[2]
		return operation, false, true
	}
	if len(parts) == 4 && parts[0] == "v1" && parts[1] == "work-items" && request.Method == http.MethodPost {
		switch parts[3] {
		case "retry", "cancel":
			operation.Name, operation.ResourceID = "work."+parts[3], parts[2]
			return operation, true, true
		}
	}
	if len(parts) == 4 && parts[0] == "v1" && parts[1] == "projects" && parts[3] == "clean" && request.Method == http.MethodPost {
		operation.Name, operation.ResourceID = "project.clean", parts[2]
		return operation, true, true
	}
	return Operation{}, false, false
}

func readJSONBody(reader io.ReadCloser) (json.RawMessage, any, error) {
	defer reader.Close()
	limited := io.LimitReader(reader, maxRequestBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > maxRequestBytes {
		return nil, nil, errors.New("request body exceeds 1 MiB")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var model any
	if err := decoder.Decode(&model); err != nil {
		return nil, nil, fmt.Errorf("decode JSON: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, nil, err
	}
	return json.RawMessage(raw), model, nil
}

func DecodeStrict(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	return ensureEOF(decoder)
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return nil
}

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (err *APIError) Error() string { return err.Message }

type ErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func errorEnvelope(code, message string) ErrorEnvelope {
	var result ErrorEnvelope
	result.Error.Code, result.Error.Message = code, message
	return result
}

func ErrorResponse(err error) (int, any) {
	var apiError *APIError
	if errors.As(err, &apiError) {
		return apiError.Status, errorEnvelope(apiError.Code, apiError.Message)
	}
	switch {
	case errors.Is(err, basestore.ErrIdempotencyConflict), errors.Is(err, basestore.ErrConflict), errors.Is(err, basestore.ErrAlreadyExists):
		return http.StatusConflict, errorEnvelope("CONFLICT", err.Error())
	case errors.Is(err, basestore.ErrNotFound):
		return http.StatusNotFound, errorEnvelope("NOT_FOUND", err.Error())
	case errors.Is(err, basestore.ErrAuthorizationDenied):
		return http.StatusForbidden, errorEnvelope("POLICY_DENIED", err.Error())
	case errors.Is(err, basestore.ErrExpired):
		return http.StatusGone, errorEnvelope("EXPIRED", err.Error())
	}
	return http.StatusInternalServerError, errorEnvelope("INTERNAL_ERROR", err.Error())
}

func writeBackendError(writer http.ResponseWriter, err error) {
	status, response := ErrorResponse(err)
	writeJSON(writer, status, response)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, errorEnvelope(code, message))
}

func writeJSON(writer http.ResponseWriter, status int, response any) {
	encoded, err := json.Marshal(response)
	if err != nil {
		status = http.StatusInternalServerError
		encoded, _ = json.Marshal(errorEnvelope("ENCODE_FAILED", err.Error()))
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(encoded)
}

func validHeaderLabel(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n")
}
