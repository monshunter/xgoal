package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/evidence"
	"github.com/monshunter/xgoal/internal/protocol"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestEvidenceRepositoryIsAppendOnlyAndInvalidatesSetsOnBindingDrift(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 9, 2, 13, 30, 0, 0, time.UTC)
	source := clock.NewFake(now)
	store, err := Open(ctx, t.TempDir(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records := []evidence.Record{
		newEvidenceRecord("evidence_1", "validator_1", now),
		newEvidenceRecord("evidence_2", "validator_2", now.Add(time.Second)),
	}
	for _, record := range records {
		if err := store.AppendEvidence(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AppendEvidence(ctx, records[0]); err == nil {
		t.Fatal("AppendEvidence() accepted a duplicate id")
	}
	set, err := evidence.NewSet(
		"set_1", evidence.SetFinal, records[0].Evidence.GoalRevisionHash,
		records[0].Evidence.ConfigHash, records[0].Evidence.TreeHash,
		[]string{"evidence_2", "evidence_1"}, now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEvidenceSet(ctx, set); err != nil {
		t.Fatal(err)
	}
	loadedSet, err := store.EvidenceSet(ctx, set.ID)
	if err != nil || loadedSet.Hash != set.Hash || loadedSet.EvidenceIDs[0] != "evidence_1" {
		t.Fatalf("EvidenceSet() = %+v, %v", loadedSet, err)
	}
	if current, err := store.EvidenceSetCurrent(ctx, set.ID); err != nil || !current {
		t.Fatalf("EvidenceSetCurrent() = %v, %v", current, err)
	}

	exact := evidence.BindingOf(records[0])
	snapshot, err := store.MarkEvidenceStaleIfMismatched(ctx, records[0].Evidence.ID, exact)
	if err != nil || snapshot.State != domain.EvidenceCurrent || snapshot.StateSequence != 1 {
		t.Fatalf("exact MarkEvidenceStaleIfMismatched() = %+v, %v", snapshot, err)
	}
	source.Advance(time.Minute)
	drifted := exact
	drifted.TreeHash = strings.Repeat("9", 40)
	snapshot, err = store.MarkEvidenceStaleIfMismatched(ctx, records[0].Evidence.ID, drifted)
	if err != nil || snapshot.State != domain.EvidenceStale || snapshot.StateSequence != 2 || snapshot.StateReason != "subject tree changed" {
		t.Fatalf("drifted MarkEvidenceStaleIfMismatched() = %+v, %v", snapshot, err)
	}
	if current, err := store.EvidenceSetCurrent(ctx, set.ID); err != nil || current {
		t.Fatalf("stale EvidenceSetCurrent() = %v, %v", current, err)
	}
	source.Advance(time.Minute)
	snapshot, err = store.TransitionEvidence(ctx, records[0].Evidence.ID, domain.EvidenceSuperseded, "new validator run")
	if err != nil || snapshot.StateSequence != 3 || snapshot.State != domain.EvidenceSuperseded {
		t.Fatalf("TransitionEvidence() = %+v, %v", snapshot, err)
	}
	if _, err := store.TransitionEvidence(ctx, records[0].Evidence.ID, domain.EvidenceCurrent, "illegal revival"); err == nil {
		t.Fatal("TransitionEvidence() revived immutable evidence")
	}

	var recordCount, stateCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence_records`).Scan(&recordCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence_state_changes WHERE evidence_id = 'evidence_1'`).Scan(&stateCount); err != nil {
		t.Fatal(err)
	}
	if recordCount != 2 || stateCount != 3 {
		t.Fatalf("append-only counts = records %d, states %d", recordCount, stateCount)
	}
}

func TestEvidenceSetRejectsStaleMemberAtomically(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 9, 2, 14, 0, 0, 0, time.UTC)
	store, err := Open(ctx, t.TempDir(), clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := newEvidenceRecord("evidence_stale", "validator", now)
	if err := store.AppendEvidence(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionEvidence(ctx, record.Evidence.ID, domain.EvidenceStale, "config changed"); err != nil {
		t.Fatal(err)
	}
	set, err := evidence.NewSet("set_stale", evidence.SetChange, record.Evidence.GoalRevisionHash, record.Evidence.ConfigHash, record.Evidence.TreeHash, []string{record.Evidence.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEvidenceSet(ctx, set); err == nil {
		t.Fatal("CreateEvidenceSet() accepted stale evidence")
	}
	if _, err := store.EvidenceSet(ctx, set.ID); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("EvidenceSet() error = %v, want not found", err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence_sets WHERE id = ?`, set.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed set insert was not atomic: count=%d err=%v", count, err)
	}
}

func newEvidenceRecord(id, producer string, createdAt time.Time) evidence.Record {
	return evidence.Record{
		Evidence: protocol.Evidence{
			ProtocolVersion: protocol.EvidenceVersion,
			ID:              id, Kind: "command", SubjectID: "work_1", Producer: producer,
			Authority:        domain.AuthorityDeterministic,
			GoalRevisionHash: strings.Repeat("a", 64), ConfigHash: strings.Repeat("b", 64),
			TreeHash: strings.Repeat("c", 40), PayloadHash: strings.Repeat("d", 64),
			State: domain.EvidenceCurrent, CreatedAt: createdAt,
		},
		DefinitionHash: strings.Repeat("e", 64), EnvironmentHash: strings.Repeat("f", 64),
		ReceiptHash: strings.Repeat("1", 64),
	}
}
