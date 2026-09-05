package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/gitrepo"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func testCheckoutIdentity(t *testing.T) gitrepo.CheckoutIdentity {
	root := t.TempDir()
	return gitrepo.CheckoutIdentity{Root: root, CommonDir: filepath.Join(root, ".git"), HeadCommit: strings.Repeat("a", 40), HeadTree: strings.Repeat("b", 40), SymbolicHEAD: "refs/heads/main", IndexHash: strings.Repeat("c", 64), IndexTree: strings.Repeat("b", 40), GitConfigHash: strings.Repeat("d", 64), IndexPresent: true}
}
func TestCheckoutAdmissionRejectsDirtyAndForeignGoalWithoutChangingOwnership(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, work := seedReadyWork(t, store, "first")
	second, _ := seedReadyWork(t, store, "second")
	identity := testCheckoutIdentity(t)
	dirty := strings.Repeat("e", 40)
	if _, err := store.AdmitCheckout(ctx, first.ID, identity, dirty); !errors.Is(err, ErrCheckoutConflict) {
		t.Fatalf("dirty admission=%v", err)
	}
	if _, err := store.Checkout(ctx); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("rejected admission created record: %v", err)
	}
	admitted, err := store.AdmitCheckout(ctx, first.ID, identity, identity.HeadTree)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimCheckoutWork(ctx, first.ID, work.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveCheckoutFailure(ctx, first.ID, work.ID, identity, dirty); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitCheckout(ctx, second.ID, identity, dirty); err == nil {
		t.Fatal("another Goal took unaccepted files")
	}
	current, err := store.Checkout(ctx)
	if err != nil || current.GoalID != first.ID || current.AcceptedTree != admitted.AcceptedTree || current.ObservedTree != dirty {
		t.Fatalf("checkout=%+v err=%v", current, err)
	}
}
func TestCheckoutRetryRequiresObservedIdentityAndAtomicallyRestartsWork(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	goal, work := seedReadyWork(t, store, "retry")
	identity := testCheckoutIdentity(t)
	if _, err := store.AdmitCheckout(ctx, goal.ID, identity, identity.HeadTree); err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimCheckoutWork(ctx, goal.ID, work.ID); err != nil {
		t.Fatal(err)
	}
	dirty := strings.Repeat("e", 40)
	if err := store.ObserveCheckoutFailure(ctx, goal.ID, work.ID, identity, dirty); err != nil {
		t.Fatal(err)
	}
	event := EventInput{Type: "RetryRequested", ActorType: "human", Payload: map[string]any{"reason": "continue reviewed scene"}}
	if err := store.UpdateWorkState(ctx, work.ID, work.Version, domain.WorkWaiting, event); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateGoalState(ctx, goal.ID, goal.Version, domain.GoalWaiting, event); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetryCheckoutWork(ctx, work.ID, work.Version+1, identity, strings.Repeat("f", 40), event); !errors.Is(err, ErrCheckoutConflict) {
		t.Fatalf("unknown files retry=%v", err)
	}
	after, err := store.WorkItem(ctx, work.ID)
	if err != nil || after.State != domain.WorkWaiting || after.Version != work.Version+1 {
		t.Fatalf("rejected retry mutated work: %+v %v", after, err)
	}
	current, err := store.RetryCheckoutWork(ctx, work.ID, work.Version+1, identity, dirty, event)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != domain.WorkReady {
		t.Fatalf("work=%+v", current)
	}
	got, err := store.Goal(ctx, goal.ID)
	if err != nil || got.State != domain.GoalRunning {
		t.Fatalf("goal=%+v %v", got, err)
	}
	admitted, err := store.AdmitCheckout(ctx, goal.ID, identity, dirty)
	if err != nil || !admitted.RetryAuthorized || admitted.AcceptedTree != identity.HeadTree {
		t.Fatalf("retry admission=%+v %v", admitted, err)
	}
	if err := store.ClaimCheckoutWork(ctx, goal.ID, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitCheckout(ctx, goal.ID, identity, dirty); !errors.Is(err, ErrCheckoutBusy) {
		t.Fatalf("retry authorization reused: %v", err)
	}
}

func TestCheckoutClaimSharesAttemptLeaseTransaction(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	goal, work := seedReadyWork(t, store, "atomic_claim")
	identity := testCheckoutIdentity(t)
	if _, err := store.AdmitCheckout(ctx, goal.ID, identity, identity.HeadTree); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_claim BEFORE INSERT ON attempts BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	attempt := domain.Attempt{ID: "attempt_atomic", WorkItemID: work.ID, AgentProfileID: "fixture", State: domain.AttemptCreated, BaseTree: identity.HeadTree, PacketHash: strings.Repeat("a", 64), Version: 1}
	event := EventInput{Type: "LeaseAcquired", ActorType: "kernel", Payload: map[string]any{}}
	if _, err := store.ClaimWork(ctx, work.ID, work.Version, LeaseDraft{ID: "lease_atomic", Holder: "fixture", TTL: time.Minute}, attempt, event); err == nil {
		t.Fatal("expected rejected attempt insert")
	}
	checkout, err := store.Checkout(ctx)
	if err != nil || checkout.WorkID != "" {
		t.Fatalf("failed transaction retained checkout claim: %+v %v", checkout, err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_claim`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimWork(ctx, work.ID, work.Version, LeaseDraft{ID: "lease_atomic", Holder: "fixture", TTL: time.Minute}, attempt, event); err != nil {
		t.Fatal(err)
	}
	checkout, err = store.Checkout(ctx)
	if err != nil || checkout.WorkID != work.ID {
		t.Fatalf("claim did not own checkout: %+v %v", checkout, err)
	}
}
