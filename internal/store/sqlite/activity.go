package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/monshunter/xgoal/internal/invocation"
	basestore "github.com/monshunter/xgoal/internal/store"
)

// GoalActivity is a read-only observation. None of these clocks grants execution
// authority or proves that a Provider process is alive or a Goal is complete.
type GoalActivity struct {
	ObservationError       string              `json:"observation_error,omitempty"`
	HeartbeatAt            *time.Time          `json:"heartbeat_at"`
	LastOutputAt           *time.Time          `json:"last_output_at"`
	LastMaterialProgressAt *time.Time          `json:"last_material_progress_at"`
	MaterialProgressKind   string              `json:"material_progress_kind,omitempty"`
	LatestInvocation       *invocation.Summary `json:"latest_invocation,omitempty"`
	LatestEvent            *ActivityEvent      `json:"latest_event,omitempty"`
}

type ActivityEvent struct {
	ID   string    `json:"id"`
	Type string    `json:"type"`
	At   time.Time `json:"at"`
}

func (s *Store) goalActivity(ctx context.Context, status GoalStatus) (GoalActivity, error) {
	var activity GoalActivity
	for _, lease := range status.Leases {
		if !lease.HeartbeatAt.IsZero() && (activity.HeartbeatAt == nil || lease.HeartbeatAt.After(*activity.HeartbeatAt)) {
			at := lease.HeartbeatAt
			activity.HeartbeatAt = &at
		}
	}
	r, err := s.LatestInvocation(ctx, status.Goal.ID)
	if err == nil {
		summary := r.Summary()
		activity.LatestInvocation = &summary
	} else if !errors.Is(err, basestore.ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
		return activity, err
	}
	var output string
	err = s.db.QueryRowContext(ctx, `SELECT json_extract(observation_json,'$.last_output_at') FROM invocations WHERE goal_id=? AND json_extract(observation_json,'$.last_output_at') IS NOT NULL ORDER BY julianday(json_extract(observation_json,'$.last_output_at')) DESC, rowid DESC LIMIT 1`, status.Goal.ID).Scan(&output)
	if err == nil {
		at, parseErr := time.Parse(time.RFC3339Nano, output)
		if parseErr != nil {
			return activity, parseErr
		}
		activity.LastOutputAt = &at
	} else if !errors.Is(err, sql.ErrNoRows) {
		return activity, err
	}
	var latest ActivityEvent
	var at string
	err = s.db.QueryRowContext(ctx, `SELECT id,event_type,created_at FROM events WHERE aggregate_type='goal' AND aggregate_id=? ORDER BY sequence DESC LIMIT 1`, status.Goal.ID).Scan(&latest.ID, &latest.Type, &at)
	if err == nil {
		latest.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return activity, err
		}
		activity.LatestEvent = &latest
	} else if !errors.Is(err, sql.ErrNoRows) {
		return activity, err
	}
	// Validation progress compares outcomes for the same revision/config/tree and
	// definition. New receipt IDs, timestamps, output bytes and environment IDs
	// do not turn a repeated identical result into progress.
	err = s.db.QueryRowContext(ctx, `
WITH revisions AS (SELECT id,frozen_at FROM goal_revisions WHERE goal_id=?),
works AS (SELECT work.id FROM work_items work JOIN plan_revisions plan ON plan.id=work.plan_revision_id WHERE plan.goal_revision_id IN (SELECT id FROM revisions)),
attempts_for_goal AS (SELECT id FROM attempts WHERE work_item_id IN (SELECT id FROM works)),
outcomes AS (
 SELECT created_at,result,LAG(result) OVER (
   PARTITION BY goal_revision_hash,config_hash,tree_hash,definition_hash ORDER BY rowid
 ) AS previous_result
 FROM validator_runs WHERE workspace_id IN (SELECT id FROM workspaces WHERE attempt_id IN (SELECT id FROM attempts_for_goal))
),
environment_facts AS (
 SELECT rowid,created_at,goal_revision_hash,config_hash,json_remove(payload_json,'$.id','$.captured_at') AS facts
 FROM environment_snapshots WHERE workspace_id IN (SELECT id FROM workspaces WHERE attempt_id IN (SELECT id FROM attempts_for_goal))
),
environments AS (
 SELECT created_at,facts,LAG(facts) OVER (PARTITION BY goal_revision_hash,config_hash ORDER BY rowid) AS previous_facts FROM environment_facts
),
progress AS (
 SELECT frozen_at AS at,'goal_revision_approved' AS kind FROM revisions
 UNION ALL SELECT updated_at,'plan_approved' FROM plan_revisions WHERE goal_revision_id IN (SELECT id FROM revisions) AND status='ACTIVE'
 UNION ALL SELECT decided_at,'gate_decision' FROM gates WHERE goal_id=? AND decided_at IS NOT NULL
 UNION ALL SELECT updated_at,'patch_accepted' FROM promotions WHERE goal_id=? AND state='OBSERVED' AND candidate_tree<>old_tree
 UNION ALL SELECT changed_at,'finding_closed' FROM review_findings WHERE review_id IN (SELECT id FROM review_runs WHERE attempt_id IN (SELECT id FROM attempts_for_goal)) AND state IN ('RESOLVED_BY_PATCH','DISPROVED_BY_EVIDENCE','WAIVED_BY_HUMAN')
 UNION ALL SELECT created_at,'validation_changed' FROM outcomes WHERE previous_result IS NULL OR result<>previous_result
 UNION ALL SELECT created_at,'environment_changed' FROM environments WHERE previous_facts IS NULL OR facts<>previous_facts
 UNION ALL SELECT created_at,'final_report' FROM final_reports WHERE goal_id=? AND state='COMMITTED'
)
SELECT at,kind FROM progress ORDER BY julianday(at) DESC,kind LIMIT 1`, status.Goal.ID, status.Goal.ID, status.Goal.ID, status.Goal.ID).Scan(&at, &activity.MaterialProgressKind)
	if err == nil {
		parsed, parseErr := time.Parse(time.RFC3339Nano, at)
		if parseErr != nil {
			return activity, parseErr
		}
		activity.LastMaterialProgressAt = &parsed
	} else if !errors.Is(err, sql.ErrNoRows) {
		return activity, err
	}
	return activity, nil
}
