package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

// RunnableGoalIDs returns non-terminal Goals that have a frozen active
// revision. DRAFT Goals without a Planner proposal are deliberately excluded.
func (s *Store) RunnableGoalIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id
FROM goals
WHERE execution_model = 'current-directory' AND state IN (?, ?) AND active_revision_id <> ''
ORDER BY created_at, id`, domain.GoalRunning, domain.GoalVerifying)
	if err != nil {
		return nil, fmt.Errorf("list runnable goals: %w", err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// LatestWorkFailure returns the newest persisted failure for the Work Item.
func (s *Store) LatestWorkFailure(ctx context.Context, workID string) (FailureSummary, error) {
	if workID == "" {
		return FailureSummary{}, errors.New("work id is required")
	}
	var result FailureSummary
	var progress int
	var createdAt string
	err := s.db.QueryRowContext(ctx, `
SELECT id, failure_class, normalized_error, fingerprint, strategy, snapshot_hash,
       material_progress, repeat_count, created_at
FROM failure_records
WHERE work_item_id = ?
ORDER BY created_at DESC, id DESC
LIMIT 1`, workID).Scan(
		&result.ID, &result.Class, &result.Error, &result.Fingerprint, &result.Strategy,
		&result.SnapshotHash, &progress, &result.RepeatCount, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return FailureSummary{}, basestore.ErrNotFound
	}
	if err != nil {
		return FailureSummary{}, err
	}
	result.MaterialProgress = progress == 1
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	return result, err
}

// CurrentWorkEvidenceIDs returns current evidence references suitable for the
// next immutable Work Packet. The evidence itself remains Store-owned.
func (s *Store) CurrentWorkEvidenceIDs(ctx context.Context, workID string) ([]string, error) {
	if workID == "" {
		return nil, errors.New("work id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT record.id
FROM evidence_records record
WHERE record.subject_id = ?
  AND (SELECT change.state FROM evidence_state_changes change
       WHERE change.evidence_id = record.id
       ORDER BY change.sequence DESC LIMIT 1) = ?
ORDER BY record.created_at, record.id`, workID, domain.EvidenceCurrent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// WorkDependencyIDs returns the stable prerequisite IDs used in an immutable
// Work Packet.
func (s *Store) WorkDependencyIDs(ctx context.Context, workID string) ([]string, error) {
	if workID == "" {
		return nil, errors.New("work id is required")
	}
	if _, err := s.WorkItem(ctx, workID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT from_id
FROM work_dependencies
WHERE to_id = ? AND dependency_type = ?
ORDER BY from_id`, workID, domain.DependencyHard)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// WorkGoalID resolves the active Goal that owns one Work Item.
func (s *Store) WorkGoalID(ctx context.Context, workID string) (string, error) {
	if workID == "" {
		return "", errors.New("work id is required")
	}
	var goalID string
	err := s.db.QueryRowContext(ctx, `
SELECT revision.goal_id
FROM work_items work
JOIN plan_revisions plan ON plan.id = work.plan_revision_id
JOIN goal_revisions revision ON revision.id = plan.goal_revision_id
WHERE work.id = ?`, workID).Scan(&goalID)
	if err != nil {
		return "", fmt.Errorf("resolve goal for work %q: %w", workID, err)
	}
	return goalID, nil
}

// GoalFindings returns every immutable Review finding and its current
// resolution state for Final Report projection.
func (s *Store) GoalFindings(ctx context.Context, goalID string) ([]ReviewFinding, error) {
	if goalID == "" {
		return nil, errors.New("goal id is required")
	}
	if _, err := s.Goal(ctx, goalID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT finding.id
FROM review_findings finding
JOIN review_runs review ON review.id = finding.review_id
JOIN attempts attempt ON attempt.id = review.attempt_id
JOIN work_items work ON work.id = attempt.work_item_id
JOIN plan_revisions plan ON plan.id = work.plan_revision_id
JOIN goal_revisions revision ON revision.id = plan.goal_revision_id
WHERE revision.goal_id = ?
ORDER BY finding.id`, goalID)
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
	result := make([]ReviewFinding, 0, len(ids))
	for _, id := range ids {
		finding, err := readFinding(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		result = append(result, finding)
	}
	return result, nil
}
