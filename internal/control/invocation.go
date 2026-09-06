package control

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	callindex "github.com/monshunter/xgoal/internal/invocation"
	"github.com/monshunter/xgoal/internal/redact"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func (service *Service) refreshLatestInvocation(ctx context.Context, goalID string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	record, err := service.store.LatestInvocation(ctx, goalID)
	if errors.Is(err, basestore.ErrNotFound) {
		return ""
	}
	if err == nil {
		// Scan releases the Store connection before file I/O and is capped at
		// 256 records per stream; stderr-only output needs no stdout event hint.
		_, err = service.store.RefreshInvocation(ctx, record.Input.ID)
	}
	if err == nil || errors.Is(err, basestore.ErrConflict) {
		return ""
	}
	return redact.String(err.Error())
}

func (service *Service) queryInvocations(ctx context.Context, op api.Operation) (int, any, error) {
	if _, err := service.store.Goal(ctx, op.ResourceID); err != nil {
		return 0, nil, mapStoreError(err)
	}
	role := op.Query["role"]
	if role != "" {
		if _, err := callindex.Directory("codex-cli", role, "id"); err != nil {
			return 0, nil, invalid("invalid role", err)
		}
	}
	records, err := service.store.Invocations(ctx, op.ResourceID, role)
	if err != nil {
		return 0, nil, mapStoreError(err)
	}
	views := make([]callindex.Summary, 0, len(records))
	for _, record := range records {
		if record.Observation.Status == "running" {
			if refreshed, err := service.store.RefreshInvocation(ctx, record.Input.ID); err == nil {
				record = refreshed
			} else if !errors.Is(err, basestore.ErrConflict) {
				return 0, nil, mapStoreError(err)
			}
		}
		views = append(views, record.Summary())
	}
	return http.StatusOK, map[string]any{"invocations": views}, nil
}
func (service *Service) queryInvocation(ctx context.Context, op api.Operation) (int, any, error) {
	record, err := service.store.RefreshInvocation(ctx, op.ResourceID)
	if errors.Is(err, basestore.ErrConflict) {
		record, err = service.store.Invocation(ctx, op.ResourceID)
	}
	if err != nil {
		return 0, nil, mapStoreError(err)
	}
	switch op.Name {
	case "invocation.context":
		result, err := callindex.ReadContext(ctx, service.store.Info().ProjectDir, record)
		if err != nil {
			return 0, nil, invocationUnavailable(err)
		}
		return http.StatusOK, result, nil
	case "invocation.logs":
		after := int64(0)
		limit := 100
		stream := op.Query["stream"]
		if stream == "" {
			stream = "stdout"
		}
		if op.Query["after"] != "" {
			after, err = strconv.ParseInt(op.Query["after"], 10, 64)
			if err != nil || after < 0 {
				return 0, nil, invalid("after must be a nonnegative sequence", err)
			}
		}
		if op.Query["limit"] != "" {
			limit, err = strconv.Atoi(op.Query["limit"])
			if err != nil || limit < 1 || limit > 256 {
				return 0, nil, invalid("limit must be 1..256", err)
			}
		}
		boundary := record.Observation.Cursor
		if stream == "stderr" {
			boundary = record.Observation.StderrCursor
		} else if stream != "stdout" {
			return 0, nil, invalid("stream must be stdout or stderr", nil)
		}
		if after > boundary {
			return 0, nil, invalid("cursor is beyond this invocation stream's durable boundary", nil)
		}
		page, err := callindex.Page(ctx, service.store.Info().ProjectDir, record, stream, after, limit)
		if err != nil {
			return 0, nil, invocationUnavailable(err)
		}
		return http.StatusOK, page, nil
	}
	return 0, nil, invalid("unknown invocation operation", nil)
}
func invocationUnavailable(err error) error {
	return &api.APIError{Status: http.StatusServiceUnavailable, Code: "INVOCATION_ARTIFACT_UNAVAILABLE", Message: err.Error()}
}
