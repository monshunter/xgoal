package sqlite

import (
	"context"
	"fmt"

	"github.com/monshunter/xgoal/internal/domain"
)

// CleanableWorkspaces returns only unreferenced workspaces owned by cancelled Goals.
// Completed Goal workspaces remain protected because their Final Report is an audit root.
func (s *Store) CleanableWorkspaces(ctx context.Context) ([]WorkspaceArtifact, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT workspace.id
FROM workspaces workspace
JOIN attempts attempt ON attempt.id = workspace.attempt_id
JOIN work_items work ON work.id = attempt.work_item_id
JOIN plan_revisions plan ON plan.id = work.plan_revision_id
JOIN goal_revisions revision ON revision.id = plan.goal_revision_id
JOIN goals goal ON goal.id = revision.goal_id
WHERE workspace.execution_model = 'current-directory' AND workspace.state = ?
  AND goal.state = ?
  AND attempt.state IN ('SUCCEEDED','FAILED','TIMED_OUT','INTERRUPTED','INVALID_OUTPUT','QUARANTINED')
  AND NOT EXISTS (SELECT 1 FROM environment_snapshots environment WHERE environment.workspace_id = workspace.id)
  AND NOT EXISTS (SELECT 1 FROM validator_runs validation WHERE validation.workspace_id = workspace.id)
  AND NOT EXISTS (SELECT 1 FROM final_reports report WHERE report.goal_id = goal.id)
ORDER BY workspace.created_at, workspace.id`, WorkspaceArtifactActive, domain.GoalCancelled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make([]WorkspaceArtifact, 0, len(ids))
	for _, id := range ids {
		item, err := s.WorkspaceArtifact(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("read cleanable workspace %q: %w", id, err)
		}
		result = append(result, item)
	}
	return result, nil
}
