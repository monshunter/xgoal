package promotion_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/promotion"
	"github.com/monshunter/xgoal/internal/workspace"
)

func TestPromotionRecoversAfterRefUpdateWithoutCreatingDuplicateCommit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repositoryPath := filepath.Join(t.TempDir(), "repo")
	initializePromotionRepository(t, repositoryPath)
	repository, err := gitrepo.Open(ctx, repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	base, err := repository.ResolveRevision(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.EnsureIntegrationBranch(ctx, "xgoal/goal_1/integration", base.Commit); err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	workspaces, err := workspace.NewManager(runtimeRoot, repository)
	if err != nil {
		t.Fatal(err)
	}
	validation, err := workspaces.Create(ctx, workspace.Spec{
		ID: "validation_1", AttemptID: "attempt_1", Kind: workspace.Validation,
		BaseCommit: base.Commit, BaseTree: base.Tree, ConfigHash: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer workspaces.Cleanup(ctx, validation.ID)
	if err := os.WriteFile(filepath.Join(validation.Path, "README.md"), []byte("promoted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidateTree, err := repository.IndexAndWriteTree(ctx, validation.Path)
	if err != nil {
		t.Fatal(err)
	}
	request := promotion.Request{
		ID: "promotion_1", EffectID: "effect_promotion_1", EffectKey: "project/goal_1/work_1/attempt_1/promotion/1",
		GoalID: "goal_1", GoalRevision: 1, GoalRevisionHash: strings.Repeat("a", 64), ConfigHash: strings.Repeat("b", 64),
		WorkItemID: "work_1", AttemptID: "attempt_1", LeaseID: "lease_1", LeaseGeneration: 1,
		BundleHash: strings.Repeat("c", 64), EvidenceSetID: "evidence_set_1",
		IntegrationRef: "refs/heads/xgoal/goal_1/integration", OldCommit: base.Commit, OldTree: base.Tree,
		CandidateTree: candidateTree, ValidationWorktree: validation.Path,
		CommitAt: time.Date(2026, 9, 2, 15, 0, 0, 0, time.UTC),
	}
	journal := &fakeJournal{failRefUpdateOnce: true}
	manager, err := promotion.NewManager(runtimeRoot, repository, journal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Promote(ctx, request); err == nil {
		t.Fatal("first Promote() did not expose the injected post-ref database failure")
	}
	afterCrash, err := repository.ResolveRef(ctx, request.IntegrationRef)
	if err != nil || afterCrash.Commit == base.Commit || afterCrash.Tree != candidateTree {
		t.Fatalf("ref after injected crash = %+v, %v", afterCrash, err)
	}

	restarted, err := promotion.NewManager(runtimeRoot, repository, journal)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := restarted.Promote(ctx, request)
	if err != nil {
		t.Fatalf("recovered Promote() error = %v", err)
	}
	if observation.IntegrationCommit != afterCrash.Commit || observation.IntegrationTree != candidateTree || observation.Trailers["XGoal-Attempt"] != request.AttemptID {
		t.Fatalf("recovered observation = %+v", observation)
	}
	commitCount, err := strconv.Atoi(strings.TrimSpace(runPromotionGit(t, repositoryPath, "rev-list", "--count", request.IntegrationRef)))
	if err != nil || commitCount != 2 {
		t.Fatalf("integration commit count = %d, %v; want base + one promotion", commitCount, err)
	}
	if got := strings.TrimSpace(runPromotionGit(t, repositoryPath, "rev-parse", "HEAD")); got != base.Commit {
		t.Fatalf("promotion moved user checkout HEAD to %s", got)
	}
	if status := runPromotionGit(t, repositoryPath, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("promotion changed user checkout: %q", status)
	}
	markerPath := filepath.Join(runtimeRoot, "promotions", request.ID, "marker.json")
	if info, err := os.Stat(markerPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("promotion marker = %v, %v", info, err)
	}

	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := restarted.Promote(ctx, request)
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("idempotent concurrent Promote() error = %v", err)
		}
	}
	if got := strings.TrimSpace(runPromotionGit(t, repositoryPath, "rev-list", "--count", request.IntegrationRef)); got != "2" {
		t.Fatalf("idempotent promotion created duplicate commits: %s", got)
	}
}

type fakeJournal struct {
	mu                sync.Mutex
	record            promotion.Record
	failRefUpdateOnce bool
}

func (journal *fakeJournal) Ensure(_ context.Context, request promotion.Request) (promotion.Record, bool, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.record.Version == 0 {
		journal.record = promotion.Record{Request: request, State: promotion.Requested, Version: 1}
		return journal.record, true, nil
	}
	if !promotion.EqualRequest(journal.record.Request, request) {
		return promotion.Record{}, false, errors.New("idempotency conflict")
	}
	return journal.record, false, nil
}

func (journal *fakeJournal) Preflight(context.Context, promotion.Request) error { return nil }

func (journal *fakeJournal) RecordCommit(_ context.Context, id, commit string) (promotion.Record, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.record.ID != id || (journal.record.IntegrationCommit != "" && journal.record.IntegrationCommit != commit) {
		return promotion.Record{}, errors.New("commit mismatch")
	}
	if journal.record.State == promotion.Requested {
		journal.record.State = promotion.CommitCreated
		journal.record.IntegrationCommit = commit
		journal.record.Version++
	}
	return journal.record, nil
}

func (journal *fakeJournal) RecordRefUpdate(_ context.Context, id, commit string) (promotion.Record, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.failRefUpdateOnce {
		journal.failRefUpdateOnce = false
		return promotion.Record{}, errors.New("injected database outage after ref update")
	}
	if journal.record.ID != id || journal.record.IntegrationCommit != commit {
		return promotion.Record{}, errors.New("ref mismatch")
	}
	if journal.record.State == promotion.CommitCreated {
		journal.record.State = promotion.RefUpdated
		journal.record.Version++
	}
	return journal.record, nil
}

func (journal *fakeJournal) Observe(_ context.Context, id string, observation promotion.Observation) (promotion.Record, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.record.ID != id || journal.record.IntegrationCommit != observation.IntegrationCommit {
		return promotion.Record{}, errors.New("observation mismatch")
	}
	if journal.record.State == promotion.RefUpdated {
		journal.record.State = promotion.Observed
		journal.record.Version++
	}
	return journal.record, nil
}

func (journal *fakeJournal) Fail(_ context.Context, id, _ string) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.record.ID != id {
		return errors.New("unknown promotion")
	}
	journal.record.State = promotion.Failed
	journal.record.Version++
	return nil
}

func initializePromotionRepository(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	runPromotionGit(t, path, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPromotionGit(t, path, "add", "README.md")
	runPromotionGit(t, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "base")
}

func runPromotionGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v error = %v\n%s", arguments, err, output)
	}
	return string(output)
}
