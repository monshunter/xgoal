package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/evidence"
	"github.com/monshunter/xgoal/internal/patch"
	"github.com/monshunter/xgoal/internal/promotion"
	"github.com/monshunter/xgoal/internal/protocol"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestPromotionJournalPreflightAndEffectObservation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, request, _ := seedPromotionFixture(t, "success")
	defer store.Close()
	record, created, err := store.Ensure(ctx, request)
	if err != nil || !created || record.State != promotion.Requested {
		t.Fatalf("Ensure() = %+v, %v, %v", record, created, err)
	}
	recoverable, err := store.RecoverablePromotions(ctx)
	if err != nil || len(recoverable) != 1 || recoverable[0].ID != request.ID {
		t.Fatalf("RecoverablePromotions() = %+v, %v", recoverable, err)
	}
	if err := store.Preflight(ctx, request); err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	commit := strings.Repeat("7", 40)
	record, err = store.RecordCommit(ctx, request.ID, commit)
	if err != nil || record.State != promotion.CommitCreated || record.IntegrationCommit != commit {
		t.Fatalf("RecordCommit() = %+v, %v", record, err)
	}
	record, err = store.RecordRefUpdate(ctx, request.ID, commit)
	if err != nil || record.State != promotion.RefUpdated {
		t.Fatalf("RecordRefUpdate() = %+v, %v", record, err)
	}
	beforeCancel, err := store.WorkItem(ctx, request.WorkItemID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelWork(ctx, beforeCancel.ID, beforeCancel.Version, EventInput{Type: "WorkCancelled", ActorType: "human", Payload: map[string]any{}}); !errors.Is(err, basestore.ErrConflict) {
		t.Fatalf("pending promotion cancel=%v", err)
	}
	afterCancel, err := store.WorkItem(ctx, request.WorkItemID)
	if err != nil || afterCancel.State != beforeCancel.State || afterCancel.Version != beforeCancel.Version {
		t.Fatalf("cancel changed pending promotion work: %+v %v", afterCancel, err)
	}
	observation := promotion.Observation{
		IntegrationRef: request.IntegrationRef, IntegrationCommit: commit, IntegrationTree: request.CandidateTree,
		Trailers: map[string]string{
			"XGoal-Goal": request.GoalID, "XGoal-Goal-Revision": "1", "XGoal-Work-Item": request.WorkItemID,
			"XGoal-Attempt": request.AttemptID, "XGoal-Evidence-Set": request.EvidenceSetID,
		},
	}
	gate, err := store.CreateGate(ctx, GateDraft{ID: "recovery_success", GoalID: request.GoalID, WorkItemID: request.WorkItemID, AttemptID: request.AttemptID, ReasonCode: "promotion_recovery_required", Facts: []any{}, Unknowns: []any{}, Options: []any{"restore"}, Recommendation: "restore", Action: domain.ActionReadFile, Scope: []string{"/**"}, ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), MaxUses: 1, Required: true}, EventInput{Type: "GateOpened", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := store.RecordWorker(ctx, WorkerProcess{AttemptID: request.AttemptID, PID: 1234, PGID: 1234, StartIdentity: "fixture-process-start", State: WorkerRunning, Version: 1}, EventInput{Type: "WorkerRecorded", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveWorkerRecovery(ctx, request.AttemptID, worker.Version, WorkerExited, "fixture process absent", EventInput{Type: "WorkerRecovered", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	stillOwned, err := store.Lease(ctx, request.LeaseID)
	if err != nil || stillOwned.State != domain.LeaseActive {
		t.Fatalf("pending promotion lease = %+v, %v", stillOwned, err)
	}
	record, err = store.Observe(ctx, request.ID, observation)
	if err != nil || record.State != promotion.Observed {
		t.Fatalf("Observe() = %+v, %v", record, err)
	}
	resolved, err := store.Gate(ctx, gate.ID)
	if err != nil || resolved.State != domain.GateApproved || resolved.Used != resolved.MaxUses || resolved.DecidedBy != "kernel" {
		t.Fatalf("recovery blocker = %+v, %v", resolved, err)
	}
	if recoverable, err := store.RecoverablePromotions(ctx); err != nil || len(recoverable) != 0 {
		t.Fatalf("terminal promotion remained recoverable = %+v, %v", recoverable, err)
	}
	effect, err := store.Effect(ctx, request.EffectID)
	if err != nil || effect.State != domain.EffectSucceeded || effect.ObservationHash == "" {
		t.Fatalf("promotion effect = %+v, %v", effect, err)
	}
	work, err := store.WorkItem(ctx, request.WorkItemID)
	if err != nil || work.State != domain.WorkCompleted {
		t.Fatalf("promoted work = %+v, %v", work, err)
	}
	attempt, err := store.Attempt(ctx, request.AttemptID)
	if err != nil || attempt.State != domain.AttemptSucceeded || attempt.ResultTree != request.CandidateTree || attempt.ResultKind != "PATCH_BUNDLE" {
		t.Fatalf("promoted attempt = %+v, %v", attempt, err)
	}
	lease, err := store.Lease(ctx, request.LeaseID)
	if err != nil || lease.State != domain.LeaseReleased {
		t.Fatalf("promoted lease = %+v, %v", lease, err)
	}
	replayed, created, err := store.Ensure(ctx, request)
	if err != nil || created || replayed.State != promotion.Observed || replayed.IntegrationCommit != commit {
		t.Fatalf("idempotent Ensure() = %+v, %v, %v", replayed, created, err)
	}
}

func TestPromotionPreflightRejectsStaleEvidenceAndFailureClosesEffect(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, request, evidenceID := seedPromotionFixture(t, "stale")
	defer store.Close()
	if _, _, err := store.Ensure(ctx, request); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Evidence(ctx, evidenceID)
	if err != nil {
		t.Fatal(err)
	}
	drifted := evidence.BindingOf(snapshot.Record)
	drifted.TreeHash = strings.Repeat("8", 40)
	if _, err := store.MarkEvidenceStaleIfMismatched(ctx, evidenceID, drifted); err != nil {
		t.Fatal(err)
	}
	if err := store.Preflight(ctx, request); err == nil {
		t.Fatal("Preflight() accepted stale evidence")
	}
	if err := store.Fail(ctx, request.ID, "stale evidence"); err != nil {
		t.Fatal(err)
	}
	persisted, err := readPromotion(ctx, store.db, request.ID)
	if err != nil || persisted.State != promotion.Failed {
		t.Fatalf("failed promotion = %+v, %v", persisted, err)
	}
	effect, err := store.Effect(ctx, request.EffectID)
	if err != nil || effect.State != domain.EffectFailed || effect.ObservationHash == "" {
		t.Fatalf("failed effect = %+v, %v", effect, err)
	}
	work, err := store.WorkItem(ctx, request.WorkItemID)
	if err != nil || work.State != domain.WorkReconciling {
		t.Fatalf("failed promotion work = %+v, %v", work, err)
	}
	attempt, err := store.Attempt(ctx, request.AttemptID)
	if err != nil || attempt.State != domain.AttemptFailed {
		t.Fatalf("failed promotion attempt = %+v, %v", attempt, err)
	}
	lease, err := store.Lease(ctx, request.LeaseID)
	if err != nil || lease.State != domain.LeaseReleased {
		t.Fatalf("failed promotion lease = %+v, %v", lease, err)
	}
}

func seedPromotionFixture(t *testing.T, suffix string) (*Store, promotion.Request, string) {
	t.Helper()
	ctx := context.Background()
	source := clock.NewFake(time.Date(2026, 9, 2, 16, 0, 0, 0, time.UTC))
	store, err := Open(ctx, t.TempDir(), source)
	if err != nil {
		t.Fatal(err)
	}
	goal, work := seedReadyWork(t, store, "work_promotion_"+suffix)
	revision, err := store.GoalRevision(ctx, goal.ActiveRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	attempt := domain.Attempt{
		ID: "attempt_" + suffix, WorkItemID: work.ID, AgentProfileID: "fake", State: domain.AttemptCreated,
		BaseTree: strings.Repeat("1", 40), PacketHash: strings.Repeat("2", 64), Version: 1,
	}
	lease, err := store.ClaimWork(ctx, work.ID, work.Version, LeaseDraft{
		ID: "lease_" + suffix, Holder: "daemon/worker", TTL: time.Hour,
	}, attempt, EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	for index, state := range []domain.AttemptState{
		domain.AttemptPreparing, domain.AttemptStarting, domain.AttemptRunning,
		domain.AttemptCollecting, domain.AttemptValidating, domain.AttemptPromoting,
	} {
		if err := store.UpdateAttemptStateWithLease(ctx, attempt.ID, int64(index+1), lease.ID, lease.Generation, state,
			EventInput{Type: "AttemptAdvanced", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpdateWorkState(ctx, work.ID, 3, domain.WorkRunning, EventInput{Type: "WorkRunning", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkState(ctx, work.ID, 4, domain.WorkVerifying, EventInput{Type: "WorkVerifying", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	content := []byte("promotion " + suffix)
	digest := sha256.Sum256(content)
	contentHash := hex.EncodeToString(digest[:])
	bundle, err := (protocol.PatchBundle{
		ProtocolVersion: protocol.PatchBundleVersion, AttemptID: attempt.ID,
		BaseCommit: strings.Repeat("4", 40), BaseTree: strings.Repeat("1", 40),
		Entries: []protocol.PatchEntry{{
			Path: "internal/result.txt", Kind: protocol.PatchAdded, ModeAfter: "100644",
			ContentHashAfter: contentHash, ObjectRef: "objects/sha256/" + contentHash,
		}},
		Objects: []protocol.PatchObject{{Ref: "objects/sha256/" + contentHash, Hash: contentHash, Length: int64(len(content))}},
	}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	patchStore, err := patch.NewStore(store.Info().ProjectDir)
	if err != nil {
		t.Fatal(err)
	}
	bundlePath, err := patchStore.Save(patch.Captured{Bundle: bundle, Objects: map[string][]byte{contentHash: content}})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.RecordPatchBundle(ctx, bundle, bundlePath); err != nil || !created {
		t.Fatalf("RecordPatchBundle() = created=%v, err=%v", created, err)
	}
	bundleHash := bundle.BundleHash
	candidateTree := strings.Repeat("6", 40)
	configHash := strings.Repeat("b", 64)
	evidenceID := "evidence_" + suffix
	record := evidence.Record{
		Evidence: protocol.Evidence{
			ProtocolVersion: protocol.EvidenceVersion, ID: evidenceID, Kind: "command", SubjectID: work.ID,
			Producer: "validator", Authority: domain.AuthorityDeterministic,
			GoalRevisionHash: revision.Hash, ConfigHash: configHash, TreeHash: candidateTree,
			PayloadHash: strings.Repeat("c", 64), State: domain.EvidenceCurrent, CreatedAt: source.Now(),
		},
		DefinitionHash: strings.Repeat("d", 64), EnvironmentHash: strings.Repeat("e", 64), ReceiptHash: strings.Repeat("f", 64),
	}
	if err := store.AppendEvidence(ctx, record); err != nil {
		t.Fatal(err)
	}
	set, err := evidence.NewSet("evidence_set_"+suffix, evidence.SetChange, revision.Hash, configHash, candidateTree, []string{evidenceID}, source.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEvidenceSet(ctx, set); err != nil {
		t.Fatal(err)
	}
	request := promotion.Request{
		ID: "promotion_" + suffix, EffectID: "effect_promotion_" + suffix,
		EffectKey: "project/" + goal.ID + "/" + work.ID + "/" + attempt.ID + "/promotion/1",
		GoalID:    goal.ID, GoalRevision: revision.Revision, GoalRevisionHash: revision.Hash, ConfigHash: configHash,
		WorkItemID: work.ID, AttemptID: attempt.ID, LeaseID: lease.ID, LeaseGeneration: lease.Generation,
		BundleHash: bundleHash, EvidenceSetID: set.ID, IntegrationRef: "refs/heads/xgoal/" + goal.ID + "/integration",
		OldCommit: strings.Repeat("4", 40), OldTree: strings.Repeat("1", 40), CandidateTree: candidateTree,
		ValidationWorktree: "/private/worktree/" + suffix, CommitAt: source.Now(),
	}
	return store, request, evidenceID
}
