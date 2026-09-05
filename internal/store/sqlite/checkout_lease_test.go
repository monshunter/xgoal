package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
)

func TestCheckoutFailureLeaseExpiryPreservesTerminalFailure(t *testing.T) {
	ctx := context.Background()
	store, request, _ := seedPromotionFixture(t, "failure_expiry")
	defer store.Close()
	attempt, err := store.Attempt(ctx, request.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	event := EventInput{Type: "AttemptFailed", ActorType: "kernel", Payload: map[string]any{}}
	if err := store.UpdateAttemptStateWithLease(ctx, attempt.ID, attempt.Version, request.LeaseID, request.LeaseGeneration, domain.AttemptFailed, event); err != nil {
		t.Fatal(err)
	}
	work, err := store.WorkItem(ctx, request.WorkItemID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkState(ctx, work.ID, work.Version, domain.WorkReconciling, event); err != nil {
		t.Fatal(err)
	}
	attempt, _ = store.Attempt(ctx, attempt.ID)
	work, _ = store.WorkItem(ctx, work.ID)
	store.source.(*clock.Fake).Advance(2 * time.Hour)
	lease, err := store.Lease(ctx, request.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveExpiredLease(ctx, lease.ID, lease.Generation, lease.Version, false, event); err == nil {
		t.Fatal("unconfirmed stop accepted")
	}
	if _, err := store.ResolveExpiredLease(ctx, lease.ID, lease.Generation, lease.Version, true, event); err != nil {
		t.Fatal(err)
	}
	afterAttempt, _ := store.Attempt(ctx, attempt.ID)
	afterWork, _ := store.WorkItem(ctx, work.ID)
	if afterAttempt.State != attempt.State || afterAttempt.Version != attempt.Version || afterWork.State != work.State || afterWork.Version != work.Version {
		t.Fatalf("failure facts changed: %+v / %+v", afterAttempt, afterWork)
	}
	lease, _ = store.Lease(ctx, lease.ID)
	if lease.State != domain.LeaseExpired {
		t.Fatalf("lease=%+v", lease)
	}
}
