package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	callindex "github.com/monshunter/xgoal/internal/invocation"
)

// Each reader pulls bounded pages independently. No subscriber or socket write
// runs on the Adapter notification path or while a Store connection is held.
func (handler *Handler) serveInvocationStream(writer http.ResponseWriter, request *http.Request, operation Operation) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, 500, "STREAM_UNAVAILABLE", "response writer cannot stream")
		return
	}
	status, response, err := handler.backend.Query(request.Context(), operation)
	if err != nil {
		writeBackendError(writer, err)
		return
	}
	page, ok := response.(callindex.LogPage)
	if !ok {
		writeError(writer, 500, "STREAM_UNAVAILABLE", "invalid invocation page")
		return
	}
	// Fix prefix selection once. Later matching IDs cannot redirect this reader.
	operation.ResourceID = page.Invocation.ID
	writer.Header().Set("XGoal-Invocation-ID", page.Invocation.ID)
	writer.Header().Set("Content-Type", "application/x-ndjson")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	encoder := json.NewEncoder(writer)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	lastSent := time.Time{}
	lastStatus, lastError := "", ""
	for {
		emit := lastSent.IsZero() || len(page.Events) > 0 || page.Complete || page.Invocation.Observation.Status != lastStatus || page.Invocation.Observation.LogError != lastError || time.Since(lastSent) >= 15*time.Second
		if emit {
			_ = http.NewResponseController(writer).SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := encoder.Encode(page); err != nil {
				return
			}
			flusher.Flush()
			lastSent = time.Now()
			lastStatus = page.Invocation.Observation.Status
			lastError = page.Invocation.Observation.LogError
		}
		if page.Complete {
			return
		}
		operation.Query["after"] = strconv.FormatInt(page.Next, 10)
		select {
		case <-request.Context().Done():
			return
		case <-ticker.C:
		}
		_, response, err = handler.backend.Query(request.Context(), operation)
		if err != nil {
			_ = http.NewResponseController(writer).SetWriteDeadline(time.Now().Add(5 * time.Second))
			_ = encoder.Encode(errorEnvelope("INVOCATION_STREAM_FAILED", err.Error()))
			flusher.Flush()
			return
		}
		page, ok = response.(callindex.LogPage)
		if !ok {
			return
		}
	}
}
