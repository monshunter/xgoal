package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/goalcompile"
)

func TestFinalizeCannotOmitFrozenScenarioMappingAndArtifacts(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	source := clock.NewFake(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	s, err := Open(ctx, root, source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	contract := planningProposal().Contract
	contract.AcceptanceCriteria[0].ID = "AC-1"
	contract.AcceptanceCriteria[0].Validators = []string{"go-test"}
	contract.AcceptanceCriteria[0].ScenarioIDs = []string{"business"}
	goal, facts, files := seedFinalizableReport(t, s, root, source, map[string]any{"protocol_version": goalcompile.ContractVersion, "contract": contract, "config_hash": strings.Repeat("b", 64), "created_by": "human", "mode": "standard"})
	_, _, err = s.FinalizeGoal(ctx, goal.ID, goal.Version, facts, files, EventInput{Type: "GoalCompleted", ActorType: "kernel", Payload: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "scenario") {
		t.Fatalf("omitted frozen scenario finalized: %v", err)
	}
	current, err := s.Goal(ctx, goal.ID)
	if err != nil || current.State != domain.GoalVerifying || current.FinalTree != "" {
		t.Fatalf("failed finalization changed Goal: %+v %v", current, err)
	}
}
