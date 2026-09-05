package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/supervisor"
)

func TestProcessAttemptRecoveryPreservesUnknownAndRetiresProvenStoppedLease(t *testing.T) {
	for _, scenario := range []string{"before_intent", "stopped", "unknown", "legacy", "legacy_orphan", "orphan_stopped", "unknown_then_stopped"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(t.TempDir(), "state"), clock.Real{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			goal, work := seedReadyWork(t, s, scenario)
			work, err = s.WorkItem(ctx, work.ID)
			if err != nil {
				t.Fatal(err)
			}
			attempt := domain.Attempt{ID: "attempt_recover", WorkItemID: work.ID, AgentProfileID: "fixture", State: domain.AttemptCreated, BaseTree: "base", PacketHash: "packet", Version: 1}
			lease, err := s.ClaimWork(ctx, work.ID, work.Version, LeaseDraft{ID: "lease_recover", Holder: "daemon", TTL: time.Minute}, attempt, EventInput{Type: "Claimed", ActorType: "kernel", Payload: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "legacy" || scenario == "legacy_orphan" {
				if _, err := s.db.Exec(`UPDATE attempts SET process_journal_version=0 WHERE id=?`, attempt.ID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "legacy_orphan" || scenario == "orphan_stopped" {
				if _, err := s.db.Exec(`UPDATE leases SET state='REVOKED' WHERE id=?`, lease.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`UPDATE work_items SET state='RECONCILING' WHERE id=?`, work.ID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "stopped" || scenario == "unknown" || scenario == "unknown_then_stopped" {
				intent := supervisor.ProcessIntent{ID: "process", Owner: supervisor.Owner{Kind: "attempt", ID: attempt.ID, GoalID: goal.ID, Generation: lease.Generation}}
				if err := s.BeginProcess(ctx, intent); err != nil {
					t.Fatal(err)
				}
				state := supervisor.ProcessTerminated
				if scenario == "unknown" || scenario == "unknown_then_stopped" {
					state = supervisor.ProcessUnknown
				}
				if err := s.FinishProcess(ctx, intent.ID, state, "recovery observation"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "unknown_then_stopped" {
				if err := s.ReconcileProcessAttempts(ctx); err != nil {
					t.Fatal(err)
				}
				if err := s.FinishProcess(ctx, "process", supervisor.ProcessTerminated, "confirmed on next startup"); err != nil {
					t.Fatal(err)
				}
			}
			for n := 0; n < 2; n++ {
				if err := s.ReconcileProcessAttempts(ctx); err != nil {
					t.Fatal(err)
				}
			}
			gotLease, _ := s.Lease(ctx, lease.ID)
			gotAttempt, _ := s.Attempt(ctx, attempt.ID)
			gotGoal, _ := s.Goal(ctx, goal.ID)
			gotWork, _ := s.WorkItem(ctx, work.ID)
			confirmed := scenario == "before_intent" || scenario == "stopped" || scenario == "orphan_stopped" || scenario == "unknown_then_stopped"
			if confirmed {
				if gotLease.State != domain.LeaseRevoked || gotAttempt.State != domain.AttemptInterrupted || gotWork.State != domain.WorkReconciling {
					t.Fatalf("unreconciled=%+v %+v %+v", gotLease, gotAttempt, gotWork)
				}
			} else {
				if (gotLease.State != domain.LeaseActive && scenario != "legacy_orphan") || gotAttempt.State != domain.AttemptCreated {
					t.Fatalf("unknown execution released=%+v %+v", gotLease, gotAttempt)
				}
			}
			gates, err := s.Gates(ctx, goal.ID, true)
			if err != nil || len(gates) != 1 || gotGoal.State != domain.GoalWaiting {
				t.Fatalf("waiting=%+v gates=%+v err=%v", gotGoal, gates, err)
			}
			if confirmed && gates[0].ReasonCode != "checkout_retry_required" {
				t.Fatalf("stale recovery gate: %+v", gates[0])
			}
		})
	}
}

func TestProcessRecoveryPreservesPendingPromotionLeaseForReadback(t *testing.T) {
	ctx := context.Background()
	s, request, _ := seedPromotionFixture(t, "process_recovery_promotion")
	defer s.Close()
	if _, _, err := s.Ensure(ctx, request); err != nil {
		t.Fatal(err)
	}
	before, err := s.Lease(ctx, request.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileProcessAttempts(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := s.Lease(ctx, request.LeaseID)
	if err != nil || after.Version != before.Version || after.State != domain.LeaseActive {
		t.Fatalf("promotion lease=%+v err=%v", after, err)
	}
	if err := s.Preflight(ctx, request); err != nil {
		t.Fatalf("readback lost ownership: %v", err)
	}
}
