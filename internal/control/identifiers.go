package control

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func (service *Service) ResolveResource(ctx context.Context, op api.Operation) (api.Operation, error) {
	// Preserve the execution preflight error even before a write reserves an
	// idempotency record; read-only lookup and cancellation remain available.
	switch op.Name {
	case "goal.pause", "goal.resume", "goal.plan", "goal.replan", "goal.finalize", "work.retry", "gate.decide", "gate.resume":
		if errors.Is(service.configError, config.ErrMigrationRequired) {
			return op, &api.APIError{Status: http.StatusConflict, Code: "CONFIG_MIGRATION_REQUIRED", Message: service.configError.Error()}
		}
	}
	kind, _, _ := strings.Cut(op.Name, ".")
	if op.ResourceID == "" || kind != "goal" && kind != "work" && kind != "gate" && kind != "invocation" {
		return op, nil
	}
	id, err := service.store.ResolveIdentifier(ctx, kind, op.ResourceID)
	if err != nil {
		var ambiguous *sqlite.AmbiguousIdentifier
		if errors.As(err, &ambiguous) {
			return op, &api.APIError{Status: http.StatusConflict, Code: "AMBIGUOUS_ID", Message: err.Error()}
		}
		return op, mapStoreError(err)
	}
	op.ResourceID = id.ID
	return op, nil
}

func (service *Service) queryIdentifiers(ctx context.Context, op api.Operation) (int, any, error) {
	limit := 100
	if value := op.Query["limit"]; value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0, nil, invalid("limit must be 1..100", err)
		}
		limit = parsed
	}
	page, err := service.store.FindIdentifiers(ctx, op.Query["kind"], op.Query["prefix"], limit)
	if err != nil {
		return 0, nil, mapStoreError(err)
	}
	return http.StatusOK, page, nil
}
