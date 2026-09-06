package control

import (
	"context"
	"net/http"
	"strconv"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func (service *Service) queryGoals(ctx context.Context, op api.Operation) (int, any, error) {
	q := sqlite.GoalListQuery{State: domain.GoalState(op.Query["state"]), After: op.Query["after"], Limit: 100}
	if value, ok := op.Query["limit"]; ok {
		limit, err := strconv.Atoi(value)
		if err != nil {
			return 0, nil, invalid("limit must be 1..100", err)
		}
		q.Limit = limit
	}
	page, err := service.store.ListGoals(ctx, q)
	return http.StatusOK, page, mapStoreError(err)
}
