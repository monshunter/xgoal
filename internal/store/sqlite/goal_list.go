package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/planner"
	"github.com/monshunter/xgoal/internal/redact"
)

var ErrInvalidGoalListQuery = errors.New("invalid goal list query")

type GoalListQuery struct {
	State domain.GoalState
	Limit int
	After string
}

func (q GoalListQuery) Validate() error {
	if q.Limit < 1 || q.Limit > 100 {
		return fmt.Errorf("%w: limit must be 1..100", ErrInvalidGoalListQuery)
	}
	if q.State != "" && !q.State.Valid() {
		return fmt.Errorf("%w: state must be a Goal state", ErrInvalidGoalListQuery)
	}
	if _, _, err := decodeGoalCursor(q.After); err != nil {
		return err
	}
	return nil
}

type GoalSummary struct {
	GoalID        string           `json:"goal_id"`
	State         domain.GoalState `json:"state"`
	Version       int64            `json:"version"`
	Summary       string           `json:"summary"`
	PlanningState string           `json:"planning_state"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

type GoalPage struct {
	Items      []GoalSummary `json:"items"`
	NextCursor string        `json:"next_cursor"`
}

// ListGoals reads one bounded page of persisted facts. The immutable ID ordering
// allows traversal beyond the identifiers endpoint's cap without status refreshes.
func (s *Store) ListGoals(ctx context.Context, q GoalListQuery) (GoalPage, error) {
	page := GoalPage{Items: []GoalSummary{}}
	if err := q.Validate(); err != nil {
		return page, err
	}
	after := ""
	if q.After != "" {
		rowID, fingerprint, _ := decodeGoalCursor(q.After)
		if err := s.db.QueryRowContext(ctx, "SELECT id FROM goals WHERE rowid=?", rowID).Scan(&after); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return page, fmt.Errorf("%w: stale cursor; restart from the first page", ErrInvalidGoalListQuery)
			}
			return page, err
		}
		if fmt.Sprintf("%x", sha256.Sum256([]byte(after))) != fingerprint {
			return page, fmt.Errorf("%w: stale cursor; restart from the first page", ErrInvalidGoalListQuery)
		}
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT g.rowid,g.id,g.state,g.version,g.created_at,g.updated_at,
 COALESCE(json_extract(r.contract_json,'$.summary'),json_extract(e.request_json,'$.raw_goal'),''),
 g.active_revision_id,g.execution_model,g.planning_paused,
 COALESCE(e.id,''),COALESCE(e.state,''),
 COALESCE(json_extract(e.request_json,'$.blocked_reason'),''),
 COALESCE(json_extract(e.observation_json,'$.execution_stopped'),0),
 COALESCE(json_extract(e.observation_json,'$.failure_code'),'')
FROM goals g
LEFT JOIN goal_revisions r ON r.id=g.active_revision_id AND r.goal_id=g.id
LEFT JOIN effects e ON e.id=g.planning_effect_id
WHERE g.id>? AND (?='' OR g.state=?)
ORDER BY g.id LIMIT ?`, after, q.State, q.State, q.Limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var lastRowID int64
	for rows.Next() {
		if len(page.Items) == q.Limit {
			page.NextCursor = encodeGoalCursor(lastRowID, page.Items[len(page.Items)-1].GoalID)
			break
		}
		var rowID int64
		var item GoalSummary
		var created, updated, summary, model string
		var planning PlanningRecord
		planning.Observation = &planner.Observation{}
		if err := rows.Scan(&rowID, &item.GoalID, &item.State, &item.Version, &created, &updated, &summary,
			&planning.Goal.ActiveRevisionID, &model, &planning.Paused, &planning.Effect.ID, &planning.Effect.State,
			&planning.Request.BlockedReason, &planning.Observation.ExecutionStopped, &planning.Observation.FailureCode); err != nil {
			return page, err
		}
		if !item.State.Valid() || item.Version <= 0 {
			return page, fmt.Errorf("goal %q contains invalid persisted state", item.GoalID)
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return page, err
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return page, err
		}
		// Redact before truncation so a credential straddling the summary boundary
		// cannot escape detection. JSON and terminal output share this public summary.
		summary = strings.Join(strings.Fields(redact.String(summary)), " ")
		runes := []rune(summary)
		if len(runes) > 160 {
			summary = string(runes[:159]) + "…"
		}
		item.Summary = summary
		planning.Goal.State = item.State
		item.PlanningState = planningState(planning, model)
		page.Items = append(page.Items, item)
		lastRowID = rowID
	}
	return page, rows.Err()
}

// A bounded cursor locates the last ID without placing arbitrarily long legacy
// IDs in a URL. The fingerprint rejects rowid reuse or renumbering after cleanup.
func encodeGoalCursor(rowID int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%x", rowID, sha256.Sum256([]byte(id)))))
}

func decodeGoalCursor(cursor string) (int64, string, error) {
	if cursor == "" {
		return 0, "", nil
	}
	invalid := fmt.Errorf("%w: after must be a next_cursor returned by goal list", ErrInvalidGoalListQuery)
	if len(cursor) > 128 {
		return 0, "", invalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != cursor {
		return 0, "", invalid
	}
	key, fingerprint, ok := strings.Cut(string(raw), ":")
	rowID, err := strconv.ParseInt(key, 10, 64)
	if !ok || err != nil || rowID <= 0 || strconv.FormatInt(rowID, 10) != key || len(fingerprint) != 64 || strings.IndexFunc(fingerprint, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') }) >= 0 {
		return 0, "", invalid
	}
	return rowID, fingerprint, nil
}
