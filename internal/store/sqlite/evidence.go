package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/evidence"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func (s *Store) AppendEvidence(ctx context.Context, record evidence.Record) error {
	if err := record.Validate(); err != nil {
		return err
	}
	if record.Evidence.State != domain.EvidenceCurrent && record.Evidence.State != domain.EvidenceInvalid {
		return errors.New("new evidence must start CURRENT or INVALID")
	}
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		if _, err := readEvidence(ctx, tx, record.Evidence.ID); err == nil {
			return fmt.Errorf("evidence %q: %w", record.Evidence.ID, basestore.ErrAlreadyExists)
		} else if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		createdAt := record.Evidence.CreatedAt.UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO evidence_records(
    id, protocol_version, kind, subject_id, producer, authority,
    goal_revision_hash, config_hash, definition_hash, environment_hash,
    tree_hash, payload_hash, receipt_hash, created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.Evidence.ID, record.Evidence.ProtocolVersion, record.Evidence.Kind,
			record.Evidence.SubjectID, record.Evidence.Producer, record.Evidence.Authority,
			record.Evidence.GoalRevisionHash, record.Evidence.ConfigHash,
			record.DefinitionHash, record.EnvironmentHash, record.Evidence.TreeHash,
			record.Evidence.PayloadHash, record.ReceiptHash, createdAt,
		); err != nil {
			return fmt.Errorf("insert evidence %q: %w", record.Evidence.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO evidence_state_changes(evidence_id, sequence, state, reason, changed_at)
VALUES (?, 1, ?, 'created', ?)`, record.Evidence.ID, record.Evidence.State, createdAt); err != nil {
			return fmt.Errorf("insert initial evidence state %q: %w", record.Evidence.ID, err)
		}
		return nil
	})
}

func (s *Store) Evidence(ctx context.Context, id string) (evidence.Snapshot, error) {
	if id == "" {
		return evidence.Snapshot{}, errors.New("evidence id is required")
	}
	return readEvidence(ctx, s.db, id)
}

func (s *Store) TransitionEvidence(ctx context.Context, id string, target domain.EvidenceState, reason string) (evidence.Snapshot, error) {
	if id == "" || !target.Valid() || reason == "" {
		return evidence.Snapshot{}, errors.New("invalid evidence transition")
	}
	var result evidence.Snapshot
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		current, err := readEvidence(ctx, tx, id)
		if err != nil {
			return err
		}
		if !validEvidenceTransition(current.State, target) {
			return fmt.Errorf("invalid evidence transition %s -> %s", current.State, target)
		}
		changedAt := s.source.Now().UTC()
		if _, err := tx.ExecContext(ctx, `
INSERT INTO evidence_state_changes(evidence_id, sequence, state, reason, changed_at)
VALUES (?, ?, ?, ?, ?)`, id, current.StateSequence+1, target, reason, changedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("append evidence state %q: %w", id, err)
		}
		current.State = target
		current.Evidence.State = target
		current.StateSequence++
		current.StateReason = reason
		current.StateChangedAt = changedAt
		result = current
		return nil
	})
	return result, err
}

func (s *Store) MarkEvidenceStaleIfMismatched(ctx context.Context, id string, expected evidence.Binding) (evidence.Snapshot, error) {
	if err := expected.Validate(); err != nil {
		return evidence.Snapshot{}, err
	}
	var result evidence.Snapshot
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		current, err := readEvidence(ctx, tx, id)
		if err != nil {
			return err
		}
		reason := evidence.StalenessReason(current, expected)
		if reason == "" || current.State != domain.EvidenceCurrent {
			result = current
			return nil
		}
		changedAt := s.source.Now().UTC()
		if _, err := tx.ExecContext(ctx, `
INSERT INTO evidence_state_changes(evidence_id, sequence, state, reason, changed_at)
VALUES (?, ?, ?, ?, ?)`, id, current.StateSequence+1, domain.EvidenceStale, reason, changedAt.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		current.State = domain.EvidenceStale
		current.Evidence.State = domain.EvidenceStale
		current.StateSequence++
		current.StateReason = reason
		current.StateChangedAt = changedAt
		result = current
		return nil
	})
	return result, err
}

func (s *Store) CreateEvidenceSet(ctx context.Context, set evidence.Set) error {
	if set.Hash == "" || len(set.EvidenceIDs) == 0 {
		return errors.New("invalid evidence set")
	}
	expected, err := evidence.NewSet(set.ID, set.Phase, set.GoalRevisionHash, set.ConfigHash, set.TreeHash, set.EvidenceIDs, set.CreatedAt)
	if err != nil || expected.Hash != set.Hash {
		return errors.New("evidence set hash does not match its canonical identity")
	}
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		for _, evidenceID := range set.EvidenceIDs {
			snapshot, err := readEvidence(ctx, tx, evidenceID)
			if err != nil {
				return err
			}
			if snapshot.State != domain.EvidenceCurrent || snapshot.Evidence.GoalRevisionHash != set.GoalRevisionHash ||
				snapshot.Evidence.ConfigHash != set.ConfigHash || snapshot.Evidence.TreeHash != set.TreeHash {
				return fmt.Errorf("evidence %q is stale or has a different set binding", evidenceID)
			}
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO evidence_sets(id, phase, goal_revision_hash, config_hash, tree_hash, set_hash, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, set.ID, set.Phase, set.GoalRevisionHash, set.ConfigHash,
			set.TreeHash, set.Hash, set.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("insert evidence set %q: %w", set.ID, err)
		}
		for ordinal, evidenceID := range set.EvidenceIDs {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO evidence_set_members(evidence_set_id, evidence_id, ordinal)
VALUES (?, ?, ?)`, set.ID, evidenceID, ordinal); err != nil {
				return fmt.Errorf("insert evidence set member %q: %w", evidenceID, err)
			}
		}
		return nil
	})
}

func (s *Store) EvidenceSet(ctx context.Context, id string) (evidence.Set, error) {
	if id == "" {
		return evidence.Set{}, errors.New("evidence set id is required")
	}
	return readEvidenceSet(ctx, s.db, id)
}

func (s *Store) EvidenceSetCurrent(ctx context.Context, id string) (bool, error) {
	var current bool
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		set, err := readEvidenceSet(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, evidenceID := range set.EvidenceIDs {
			snapshot, err := readEvidence(ctx, tx, evidenceID)
			if err != nil {
				return err
			}
			if snapshot.State != domain.EvidenceCurrent || snapshot.Evidence.GoalRevisionHash != set.GoalRevisionHash ||
				snapshot.Evidence.ConfigHash != set.ConfigHash || snapshot.Evidence.TreeHash != set.TreeHash {
				current = false
				return nil
			}
		}
		current = true
		return nil
	})
	return current, err
}

type evidenceQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readEvidence(ctx context.Context, queryer evidenceQueryer, id string) (evidence.Snapshot, error) {
	var result evidence.Snapshot
	var authority, state, createdAt, changedAt string
	err := queryer.QueryRowContext(ctx, `
SELECT r.protocol_version, r.id, r.kind, r.subject_id, r.producer, r.authority,
       r.goal_revision_hash, r.config_hash, r.definition_hash, r.environment_hash,
       r.tree_hash, r.payload_hash, r.receipt_hash, r.created_at,
       s.sequence, s.state, s.reason, s.changed_at
FROM evidence_records r
JOIN evidence_state_changes s ON s.evidence_id = r.id
WHERE r.id = ?
  AND s.sequence = (SELECT MAX(sequence) FROM evidence_state_changes WHERE evidence_id = r.id)`, id).Scan(
		&result.Evidence.ProtocolVersion, &result.Evidence.ID, &result.Evidence.Kind,
		&result.Evidence.SubjectID, &result.Evidence.Producer, &authority,
		&result.Evidence.GoalRevisionHash, &result.Evidence.ConfigHash,
		&result.DefinitionHash, &result.EnvironmentHash, &result.Evidence.TreeHash,
		&result.Evidence.PayloadHash, &result.ReceiptHash, &createdAt,
		&result.StateSequence, &state, &result.StateReason, &changedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return evidence.Snapshot{}, fmt.Errorf("evidence %q: %w", id, basestore.ErrNotFound)
	}
	if err != nil {
		return evidence.Snapshot{}, fmt.Errorf("read evidence %q: %w", id, err)
	}
	result.Evidence.Authority = domain.Authority(authority)
	result.State = domain.EvidenceState(state)
	result.Evidence.State = result.State
	result.Evidence.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return evidence.Snapshot{}, err
	}
	result.StateChangedAt, err = time.Parse(time.RFC3339Nano, changedAt)
	if err != nil {
		return evidence.Snapshot{}, err
	}
	return result, nil
}

func readEvidenceSet(ctx context.Context, queryer evidenceQueryer, id string) (evidence.Set, error) {
	var set evidence.Set
	var phase, createdAt string
	err := queryer.QueryRowContext(ctx, `
SELECT id, phase, goal_revision_hash, config_hash, tree_hash, set_hash, created_at
FROM evidence_sets WHERE id = ?`, id).Scan(&set.ID, &phase, &set.GoalRevisionHash, &set.ConfigHash,
		&set.TreeHash, &set.Hash, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return evidence.Set{}, fmt.Errorf("evidence set %q: %w", id, basestore.ErrNotFound)
	}
	if err != nil {
		return evidence.Set{}, err
	}
	set.Phase = evidence.SetPhase(phase)
	set.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return evidence.Set{}, err
	}
	rows, err := queryer.QueryContext(ctx, `
SELECT evidence_id FROM evidence_set_members WHERE evidence_set_id = ? ORDER BY ordinal`, id)
	if err != nil {
		return evidence.Set{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var evidenceID string
		if err := rows.Scan(&evidenceID); err != nil {
			return evidence.Set{}, err
		}
		set.EvidenceIDs = append(set.EvidenceIDs, evidenceID)
	}
	return set, rows.Err()
}

func validEvidenceTransition(from, to domain.EvidenceState) bool {
	switch from {
	case domain.EvidenceCurrent:
		return to == domain.EvidenceStale || to == domain.EvidenceSuperseded || to == domain.EvidenceInvalid
	case domain.EvidenceStale:
		return to == domain.EvidenceSuperseded || to == domain.EvidenceInvalid
	default:
		return false
	}
}
