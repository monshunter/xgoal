package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
	reviewartifact "github.com/monshunter/xgoal/internal/review"
	basestore "github.com/monshunter/xgoal/internal/store"
)

type ReviewRun struct {
	ID                      string
	AttemptID               string
	ReviewerProfileID       string
	ImplementationProfileID string
	ImplementationSessionID string
	ReviewerSessionID       string
	CandidateTree           string
	PacketPath              string
	PacketHash              string
	ResultPath              string
	ResultHash              string
	Status                  protocol.ReviewStatus
	Findings                []ReviewFinding
	CreatedAt               time.Time
}

type ReviewFinding struct {
	ReviewID      string
	Finding       protocol.ReviewFinding
	State         domain.FindingState
	StateSequence int64
	StateReason   string
	CreatedAt     time.Time
	ChangedAt     time.Time
}

func (s *Store) RecordReview(ctx context.Context, packet reviewartifact.PacketArtifact, result reviewartifact.ResultArtifact, reviewerSessionID string) (ReviewRun, bool, error) {
	if reviewerSessionID == "" || packet.Packet.ID == "" || result.Result.ProtocolVersion != protocol.ReviewResultVersion || packet.Packet.ImplementationAttemptID == "" {
		return ReviewRun{}, false, errors.New("invalid review record")
	}
	if err := validateReviewArtifactPath(s.info.ProjectDir, packet.Path, packet.Packet.ID, "packet.json"); err != nil {
		return ReviewRun{}, false, err
	}
	if err := validateReviewArtifactPath(s.info.ProjectDir, result.Path, packet.Packet.ID, "result.json"); err != nil {
		return ReviewRun{}, false, err
	}
	suppliedPacketHash, err := packet.Packet.Hash()
	if err != nil || suppliedPacketHash != packet.Hash {
		return ReviewRun{}, false, errors.New("review packet value does not match its artifact hash")
	}
	_, diskPacketHash, err := reviewartifact.ReadPacket(packet.Path)
	if err != nil || diskPacketHash != packet.Hash {
		return ReviewRun{}, false, errors.New("review packet artifact does not match record")
	}
	diskResult, diskResultHash, err := reviewartifact.ReadResult(result.Path)
	if err != nil || diskResultHash != result.Hash || !sameReviewResult(diskResult, result.Result) {
		return ReviewRun{}, false, errors.New("review result artifact does not match record")
	}
	created := false
	var stored ReviewRun
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		if _, err := readAttempt(ctx, tx, packet.Packet.ImplementationAttemptID); err != nil {
			return err
		}
		existing, err := readReview(ctx, tx, packet.Packet.ID)
		if err == nil {
			if existing.PacketHash != packet.Hash || existing.ResultHash != result.Hash || existing.ReviewerSessionID != reviewerSessionID {
				return fmt.Errorf("review %q: %w", packet.Packet.ID, basestore.ErrIdempotencyConflict)
			}
			stored = existing
			return nil
		}
		if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		now := s.source.Now().UTC()
		_, err = tx.ExecContext(ctx, `
INSERT INTO review_runs(id, attempt_id, reviewer_profile_id, implementation_profile_id,
    implementation_session_id, reviewer_session_id, candidate_tree, packet_path,
    packet_hash, result_path, result_hash, review_status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			packet.Packet.ID, packet.Packet.ImplementationAttemptID, packet.Packet.ReviewerProfileID,
			packet.Packet.ImplementationProfileID, packet.Packet.ImplementationSessionID, reviewerSessionID,
			packet.Packet.CandidateTree, packet.Path, packet.Hash, result.Path, result.Hash,
			result.Result.ReviewStatus, now.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("insert review %q: %w", packet.Packet.ID, err)
		}
		for _, finding := range result.Result.Findings {
			_, err = tx.ExecContext(ctx, `
INSERT INTO review_findings(id, review_id, severity, category, path, line, claim, basis,
    recommended_fix, authority, state, state_sequence, state_reason, created_at, changed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 'review opened finding', ?, ?)`,
				finding.ID, packet.Packet.ID, finding.Severity, finding.Category, finding.Path,
				finding.Line, finding.Claim, finding.Basis, finding.RecommendedFix,
				domain.AuthorityInference, domain.FindingOpen, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
			if err != nil {
				return fmt.Errorf("insert review finding %q: %w", finding.ID, err)
			}
		}
		prepared, err := prepareEvent(EventInput{Type: "ReviewRecorded", ActorType: "kernel", CorrelationID: packet.Packet.ImplementationAttemptID, Payload: map[string]any{"status": result.Result.ReviewStatus, "packet_hash": packet.Hash, "result_hash": result.Hash}})
		if err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "review", packet.Packet.ID, prepared); err != nil {
			return err
		}
		if err := refreshOpenFindingProjection(ctx, tx, packet.Packet.ID, now); err != nil {
			return err
		}
		created = true
		stored = ReviewRun{ID: packet.Packet.ID, AttemptID: packet.Packet.ImplementationAttemptID, ReviewerProfileID: packet.Packet.ReviewerProfileID, ImplementationProfileID: packet.Packet.ImplementationProfileID, ImplementationSessionID: packet.Packet.ImplementationSessionID, ReviewerSessionID: reviewerSessionID, CandidateTree: packet.Packet.CandidateTree, PacketPath: packet.Path, PacketHash: packet.Hash, ResultPath: result.Path, ResultHash: result.Hash, Status: result.Result.ReviewStatus, CreatedAt: now}
		for _, finding := range result.Result.Findings {
			stored.Findings = append(stored.Findings, ReviewFinding{ReviewID: packet.Packet.ID, Finding: finding, State: domain.FindingOpen, StateSequence: 1, StateReason: "review opened finding", CreatedAt: now, ChangedAt: now})
		}
		return nil
	})
	return stored, created, err
}

func (s *Store) Review(ctx context.Context, id string) (ReviewRun, error) {
	if id == "" {
		return ReviewRun{}, errors.New("review id is required")
	}
	run, err := readReview(ctx, s.db, id)
	if err != nil {
		return ReviewRun{}, err
	}
	packet, packetHash, packetErr := reviewartifact.ReadPacket(run.PacketPath)
	result, resultHash, resultErr := reviewartifact.ReadResult(run.ResultPath)
	if packetErr != nil || resultErr != nil || packetHash != run.PacketHash || resultHash != run.ResultHash ||
		packet.ID != run.ID || packet.ImplementationAttemptID != run.AttemptID || packet.CandidateTree != run.CandidateTree ||
		result.ReviewStatus != run.Status || len(result.Findings) != len(run.Findings) {
		return ReviewRun{}, errors.New("persisted review artifacts are missing, changed, or inconsistent")
	}
	persistedFindings := make(map[string]protocol.ReviewFinding, len(run.Findings))
	for _, finding := range run.Findings {
		persistedFindings[finding.Finding.ID] = finding.Finding
	}
	for _, finding := range result.Findings {
		if persisted, exists := persistedFindings[finding.ID]; !exists || persisted != finding {
			return ReviewRun{}, errors.New("persisted review findings differ from the immutable result")
		}
	}
	return run, nil
}

func (s *Store) TransitionFinding(ctx context.Context, id string, target domain.FindingState, reason string, event EventInput) (ReviewFinding, error) {
	if id == "" || !target.Valid() || reason == "" {
		return ReviewFinding{}, errors.New("invalid finding transition")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return ReviewFinding{}, err
	}
	var result ReviewFinding
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		current, err := readFinding(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := domain.ValidateFindingTransition(current.State, target); err != nil {
			return err
		}
		now := s.source.Now().UTC()
		updated, err := tx.ExecContext(ctx, `UPDATE review_findings SET state=?, state_sequence=state_sequence+1, state_reason=?, changed_at=? WHERE id=? AND state_sequence=?`, target, reason, now.Format(time.RFC3339Nano), id, current.StateSequence)
		if err != nil {
			return err
		}
		affected, _ := updated.RowsAffected()
		if affected != 1 {
			return basestore.ErrConflict
		}
		if err := s.appendEvent(ctx, tx, "finding", id, prepared); err != nil {
			return err
		}
		if err := refreshOpenFindingProjection(ctx, tx, current.ReviewID, now); err != nil {
			return err
		}
		current.State = target
		current.StateSequence++
		current.StateReason = reason
		current.ChangedAt = now
		result = current
		return nil
	})
	return result, err
}

type reviewQueryer interface {
	rowQueryer
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readReview(ctx context.Context, queryer reviewQueryer, id string) (ReviewRun, error) {
	var run ReviewRun
	var created string
	err := queryer.QueryRowContext(ctx, `SELECT id,attempt_id,reviewer_profile_id,implementation_profile_id,implementation_session_id,reviewer_session_id,candidate_tree,packet_path,packet_hash,result_path,result_hash,review_status,created_at FROM review_runs WHERE id=?`, id).Scan(&run.ID, &run.AttemptID, &run.ReviewerProfileID, &run.ImplementationProfileID, &run.ImplementationSessionID, &run.ReviewerSessionID, &run.CandidateTree, &run.PacketPath, &run.PacketHash, &run.ResultPath, &run.ResultHash, &run.Status, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return ReviewRun{}, fmt.Errorf("review %q: %w", id, basestore.ErrNotFound)
	}
	if err != nil {
		return ReviewRun{}, err
	}
	run.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return ReviewRun{}, err
	}
	rows, err := queryer.QueryContext(ctx, `SELECT id,severity,category,path,line,claim,basis,recommended_fix,state,state_sequence,state_reason,created_at,changed_at FROM review_findings WHERE review_id=? ORDER BY id`, id)
	if err != nil {
		return ReviewRun{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var f ReviewFinding
		var createdAt, changedAt string
		f.ReviewID = id
		if err := rows.Scan(&f.Finding.ID, &f.Finding.Severity, &f.Finding.Category, &f.Finding.Path, &f.Finding.Line, &f.Finding.Claim, &f.Finding.Basis, &f.Finding.RecommendedFix, &f.State, &f.StateSequence, &f.StateReason, &createdAt, &changedAt); err != nil {
			return ReviewRun{}, err
		}
		f.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return ReviewRun{}, err
		}
		f.ChangedAt, err = time.Parse(time.RFC3339Nano, changedAt)
		if err != nil {
			return ReviewRun{}, err
		}
		run.Findings = append(run.Findings, f)
	}
	return run, rows.Err()
}
func readFinding(ctx context.Context, queryer rowQueryer, id string) (ReviewFinding, error) {
	var f ReviewFinding
	var created, changed string
	err := queryer.QueryRowContext(ctx, `SELECT review_id,id,severity,category,path,line,claim,basis,recommended_fix,state,state_sequence,state_reason,created_at,changed_at FROM review_findings WHERE id=?`, id).Scan(&f.ReviewID, &f.Finding.ID, &f.Finding.Severity, &f.Finding.Category, &f.Finding.Path, &f.Finding.Line, &f.Finding.Claim, &f.Finding.Basis, &f.Finding.RecommendedFix, &f.State, &f.StateSequence, &f.StateReason, &created, &changed)
	if errors.Is(err, sql.ErrNoRows) {
		return f, basestore.ErrNotFound
	}
	if err != nil {
		return f, err
	}
	f.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return f, err
	}
	f.ChangedAt, err = time.Parse(time.RFC3339Nano, changed)
	return f, err
}

func refreshOpenFindingProjection(ctx context.Context, tx *sql.Tx, reviewID string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE goal_completion_facts SET open_blocking_findings=(SELECT COUNT(*) FROM review_findings rf JOIN review_runs rr ON rr.id=rf.review_id JOIN attempts a ON a.id=rr.attempt_id JOIN work_items w ON w.id=a.work_item_id JOIN plan_revisions p ON p.id=w.plan_revision_id JOIN goal_revisions gr ON gr.id=p.goal_revision_id WHERE gr.goal_id=goal_completion_facts.goal_id AND rf.state='OPEN' AND rf.severity IN ('blocker','high')),version=version+1,updated_at=? WHERE goal_id=(SELECT gr.goal_id FROM review_runs rr JOIN attempts a ON a.id=rr.attempt_id JOIN work_items w ON w.id=a.work_item_id JOIN plan_revisions p ON p.id=w.plan_revision_id JOIN goal_revisions gr ON gr.id=p.goal_revision_id WHERE rr.id=?)`, now.Format(time.RFC3339Nano), reviewID)
	return err
}
func validateReviewArtifactPath(root, path, id, name string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	want := filepath.Join(resolvedRoot, "reviews", id, name)
	if path != want {
		return errors.New("review artifact is outside the project review directory")
	}
	return nil
}
func sameReviewResult(left, right protocol.ReviewResult) bool {
	leftHash, err := left.Hash()
	if err != nil {
		return false
	}
	rightHash, err := right.Hash()
	return err == nil && leftHash == rightHash
}
