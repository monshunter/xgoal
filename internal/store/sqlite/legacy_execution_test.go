package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	finalreport "github.com/monshunter/xgoal/internal/report"
)

func TestCurrentDirectoryMigrationPreservesCompletedReportAndWaitsLegacyGoal(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	source := clock.NewFake(time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC))
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	old, err := openWithMigrations(ctx, root, source, migrations[:7])
	if err != nil {
		t.Fatal(err)
	}
	unfinished, _ := seedReadyWork(t, old, "legacy_unfinished")
	finalGoal, facts, files := seedFinalizableReport(t, old, root, source)
	event := EventInput{Type: "GoalCompleted", ActorType: "kernel", Payload: map[string]any{}}
	if _, _, err := old.FinalizeGoal(ctx, finalGoal.ID, finalGoal.Version, facts, files, event); err != nil {
		t.Fatal(err)
	}
	manager, err := finalreport.NewFileManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Commit(files); err != nil {
		t.Fatal(err)
	}
	if _, err := old.MarkFinalReportCommitted(ctx, finalGoal.ID, 1); err != nil {
		t.Fatal(err)
	}
	before, err := old.FinalReport(ctx, finalGoal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := Open(ctx, root, source)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if err := current.ReconcileLegacyExecution(ctx); err != nil {
		t.Fatal(err)
	}
	if err := current.ReconcileLegacyExecution(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := current.GoalStatus(ctx, unfinished.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Goal.State != domain.GoalWaiting || status.ExecutionModel != "git-worktree" || status.ExecutionBlocker == "" || len(status.Gates) != 1 {
		t.Fatalf("migration status=%+v", status)
	}
	ids, err := current.RunnableGoalIDs(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatalf("legacy goal scheduled: %v %v", ids, err)
	}
	after, err := current.FinalReport(ctx, finalGoal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Files.ReportHash != after.Files.ReportHash || before.State != after.State {
		t.Fatalf("historical report changed: %+v -> %+v", before, after)
	}
	completed, err := current.Goal(ctx, finalGoal.ID)
	if err != nil || completed.State != domain.GoalCompleted || completed.FinalReportHash != before.Files.ReportHash {
		t.Fatalf("completed history=%+v %v", completed, err)
	}
	backups, err := os.ReadDir(filepath.Join(root, "backups"))
	if err != nil || len(backups) == 0 {
		t.Fatalf("migration backup missing: %v", err)
	}
}
