package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/project"
	finalreport "github.com/monshunter/xgoal/internal/report"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestPublicFinalizeRejectsLiveCheckoutDriftAndPreservesScene(t *testing.T) {
	for _, drift := range []string{"source", "HEAD", "symbolic_HEAD", "index", "private_ref"} {
		t.Run(drift, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			configuration, err := os.ReadFile("../../xgoal.example.yaml")
			if err != nil {
				t.Fatal(err)
			}
			write := func(path string, contents []byte) {
				t.Helper()
				if err := os.WriteFile(path, contents, 0600); err != nil {
					t.Fatal(err)
				}
			}
			git := func(args ...string) string {
				t.Helper()
				command := exec.Command("git", append([]string{"-C", root}, args...)...)
				command.Env = project.GitEnvironment()
				out, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			write(filepath.Join(root, "xgoal.yaml"), configuration)
			write(filepath.Join(root, "source.txt"), []byte("accepted\n"))
			git("init", "-b", "main")
			git("config", "user.name", "test")
			git("config", "user.email", "test@example.invalid")
			git("add", "xgoal.yaml", "source.txt")
			git("-c", "commit.gpgsign=false", "commit", "-m", "baseline")
			repo, err := gitrepo.Open(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			root = repo.Root()
			identity, err := repo.ReadCheckoutIdentity(ctx)
			if err != nil {
				t.Fatal(err)
			}
			indexPath := filepath.Join(repo.CommonDir(), "index")
			index, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			store, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), clock.Real{})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			goal := domain.Goal{ID: "goal_finalize", State: domain.GoalDraft, Version: 1}
			if err := store.CreateGoal(ctx, goal, sqlite.EventInput{Type: "GoalCreated", ActorType: "human", Payload: map[string]any{}}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AdmitCheckout(ctx, goal.ID, identity, identity.HeadTree); err != nil {
				t.Fatal(err)
			}
			ref := "refs/xgoal/goals/" + goal.ID + "/integration"
			if _, _, err := repo.EnsureIntegrationRef(ctx, ref, identity.HeadCommit); err != nil {
				t.Fatal(err)
			}
			service, err := New(store, root)
			if err != nil {
				t.Fatal(err)
			}
			request := finalizeRequest{ExpectedVersion: goal.Version,
				Facts:  sqlite.CompletionFacts{IntegrationTree: identity.HeadTree, ExpectedTree: identity.HeadTree},
				Report: finalreport.Report{Final: finalreport.FinalTrace{Commit: identity.HeadCommit, Tree: identity.HeadTree}},
			}
			if err := service.checkFinalizeCheckout(ctx, goal.ID, request); err != nil {
				t.Fatalf("accepted checkout rejected: %v", err)
			}
			other, err := repo.CreateCommit(ctx, gitrepo.CommitSpec{Tree: identity.HeadTree, Parent: identity.HeadCommit, Message: "different identity", Timestamp: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			switch drift {
			case "source":
				write(filepath.Join(root, "source.txt"), []byte("operator change\n"))
			case "HEAD":
				git("update-ref", identity.SymbolicHEAD, other.ID)
			case "symbolic_HEAD":
				git("update-ref", "refs/heads/other", identity.HeadCommit)
				git("symbolic-ref", "HEAD", "refs/heads/other")
			case "index":
				blob, err := repo.WriteBlob(ctx, []byte("staged operator change\n"))
				if err != nil {
					t.Fatal(err)
				}
				git("update-index", "--cacheinfo", "100644,"+blob+",source.txt")
			case "private_ref":
				git("update-ref", "--no-deref", ref, other.ID)
			}
			beforeSource, _ := os.ReadFile(filepath.Join(root, "source.txt"))
			beforeIndex, _ := os.ReadFile(indexPath)
			beforeHead := git("rev-parse", "HEAD")
			beforeSymbolicHead := git("symbolic-ref", "HEAD")
			beforeRef := git("rev-parse", ref)
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = service.Execute(ctx, api.Operation{Name: "goal.finalize", ResourceID: goal.ID, Body: body})
			var failure *api.APIError
			if !errors.As(err, &failure) || failure.Code != "CHECKOUT_WAITING" || failure.Status != http.StatusConflict {
				t.Fatalf("public finalization did not reject %s drift: %v", drift, err)
			}
			afterSource, _ := os.ReadFile(filepath.Join(root, "source.txt"))
			afterIndex, _ := os.ReadFile(indexPath)
			if string(afterSource) != string(beforeSource) || string(afterIndex) != string(beforeIndex) || git("rev-parse", "HEAD") != beforeHead || git("symbolic-ref", "HEAD") != beforeSymbolicHead || git("rev-parse", ref) != beforeRef {
				t.Fatal("rejected finalization changed the operator's scene")
			}
			persisted, err := store.Goal(ctx, goal.ID)
			if err != nil || persisted.State != goal.State || persisted.Version != goal.Version {
				t.Fatalf("rejected finalization mutated Goal: %+v %v", persisted, err)
			}
			// Only this test's explicit operator restoration changes the scene.
			write(filepath.Join(root, "source.txt"), []byte("accepted\n"))
			write(indexPath, index)
			git("symbolic-ref", "HEAD", identity.SymbolicHEAD)
			git("update-ref", identity.SymbolicHEAD, identity.HeadCommit)
			git("update-ref", "--no-deref", ref, identity.HeadCommit)
			if err := service.checkFinalizeCheckout(ctx, goal.ID, request); err != nil {
				t.Fatalf("exact restored checkout remains blocked: %v", err)
			}
		})
	}
}
