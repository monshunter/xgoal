package orchestrator

import (
	"context"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/planner"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPlanningAcceptanceInputsAreFrozenAndInvalidFilesStopBeforeProvider(t *testing.T) {
	for _, kind := range []string{"document", "missing", "symlink", "denied"} {
		t.Run(kind, func(t *testing.T) {
			engine, request, provider := planningFixture(t)
			path := "acceptance.md"
			if kind == "denied" {
				path = ".env"
			}
			if kind == "symlink" {
				if err := os.Symlink("xgoal.yaml", filepath.Join(engine.projectRoot, path)); err != nil {
					t.Fatal(err)
				}
			} else if kind != "missing" {
				if err := os.WriteFile(filepath.Join(engine.projectRoot, path), []byte("# Acceptance\nPreserve the exact output.\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "missing" {
				for _, args := range [][]string{{"add", "--", path}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@invalid", "commit", "-qm", "acceptance"}} {
					if out, err := exec.Command("git", append([]string{"-C", engine.projectRoot}, args...)...).CombinedOutput(); err != nil {
						t.Fatalf("%s: %v", out, err)
					}
				}
			}
			request.AcceptanceFiles = []string{path}
			if _, err := engine.store.RetryPlanning(context.Background(), request.GoalID, 1, request, "acceptance inputs"); err != nil {
				t.Fatal(err)
			}
			provider.plan = func(_ context.Context, invocation planner.Invocation) (planner.Execution, error) {
				packet, err := planner.ValidateInvocation(invocation)
				if err != nil || len(packet.AcceptanceInputs) != 1 || packet.AcceptanceInputs[0].Path != path {
					t.Fatalf("missing frozen material: %+v %v", packet.AcceptanceInputs, err)
				}
				return planner.Execution{Proposal: planningProposal(), SessionID: "planner"}, nil
			}
			if err := engine.runPlanning(context.Background(), request.GoalID); err != nil {
				t.Fatal(err)
			}
			record, err := engine.store.Planning(context.Background(), request.GoalID)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "document" {
				if record.Goal.State != domain.GoalRunning {
					t.Fatalf("not published: %+v", record)
				}
				revision, _ := engine.store.GoalRevision(context.Background(), record.Goal.ActiveRevisionID)
				frozen, err := decodeFrozenContract(revision.ContractJSON)
				if err != nil || len(frozen.Contract.AcceptanceInputs) != 1 {
					t.Fatalf("material was not frozen: %s %v", revision.ContractJSON, err)
				}
			} else if provider.calls != 0 || record.State != "WAITING" {
				t.Fatalf("invalid input reached provider: calls=%d state=%s", provider.calls, record.State)
			}
		})
	}
}
