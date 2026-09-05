package promotion_test

import (
	"bytes"
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
)

type currentPromotionFixture struct {
	repository  *gitrepo.Repository
	runtimeRoot string
	request     promotion.Request
	index       []byte
	worktrees   string
	branches    string
}

func newCurrentPromotionFixture(t *testing.T) currentPromotionFixture {
	t.Helper()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo")
	initializePromotionRepository(t, root)
	repository, err := gitrepo.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repository.ReadCheckoutIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/xgoal/goals/goal_1/integration"
	if _, _, err := repository.EnsureIntegrationRef(ctx, ref, identity.HeadCommit); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(repository.CommonDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := currentPromotionFixture{repository: repository, runtimeRoot: filepath.Join(t.TempDir(), "runtime"), index: index,
		worktrees: runPromotionGit(t, repository.Root(), "worktree", "list", "--porcelain"),
		branches:  runPromotionGit(t, repository.Root(), "for-each-ref", "refs/heads/")}
	if err := os.WriteFile(filepath.Join(repository.Root(), "README.md"), []byte("promoted\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.SnapshotTree(ctx, gitrepo.SnapshotSpec{BaseTree: identity.HeadTree})
	if err != nil {
		t.Fatal(err)
	}
	fixture.request = promotion.Request{
		ID: "promotion_1", EffectID: "effect_promotion_1", EffectKey: "project/goal_1/work_1/attempt_1/promotion/1",
		GoalID: "goal_1", GoalRevision: 1, GoalRevisionHash: strings.Repeat("a", 64), ConfigHash: strings.Repeat("b", 64),
		WorkItemID: "work_1", AttemptID: "attempt_1", LeaseID: "lease_1", LeaseGeneration: 1,
		BundleHash: strings.Repeat("c", 64), EvidenceSetID: "evidence_set_1",
		IntegrationRef: ref, OldCommit: identity.HeadCommit, OldTree: identity.HeadTree, CandidateTree: snapshot.Tree,
		CommitAt:       time.Date(2026, 9, 2, 15, 0, 0, 0, time.UTC),
		ExecutionModel: promotion.CurrentDirectory, ExecutionPath: repository.Root(), CheckoutIdentity: &identity,
	}
	return fixture
}

func (fixture currentPromotionFixture) manager(t *testing.T, journal *fakeJournal) *promotion.Manager {
	t.Helper()
	manager, err := promotion.NewManager(fixture.runtimeRoot, fixture.repository, journal)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func (fixture currentPromotionFixture) assertUserResultPreserved(t *testing.T) {
	t.Helper()
	if err := fixture.repository.CheckCheckoutIdentity(context.Background(), *fixture.request.CheckoutIdentity); err != nil {
		t.Fatal(err)
	}
	currentIndex, err := os.ReadFile(filepath.Join(fixture.repository.CommonDir(), "index"))
	if err != nil || !bytes.Equal(currentIndex, fixture.index) {
		t.Fatalf("user index changed: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(fixture.repository.Root(), "README.md"))
	if err != nil || string(contents) != "promoted\n" {
		t.Fatalf("result disappeared from current directory: %q %v", contents, err)
	}
	if got := runPromotionGit(t, fixture.repository.Root(), "worktree", "list", "--porcelain"); got != fixture.worktrees {
		t.Fatalf("worktree inventory changed: %s", got)
	}
	if got := runPromotionGit(t, fixture.repository.Root(), "for-each-ref", "refs/heads/"); got != fixture.branches {
		t.Fatalf("user branch refs changed: %s", got)
	}
}

func TestPromotionRecoversAfterRefUpdateWithoutCreatingDuplicateCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newCurrentPromotionFixture(t)
	request := fixture.request
	journal := &fakeJournal{failRefUpdateOnce: true}
	if _, err := fixture.manager(t, journal).Promote(ctx, request); err == nil {
		t.Fatal("first Promote did not expose injected post-ref database failure")
	}
	afterCrash, err := fixture.repository.ResolveRef(ctx, request.IntegrationRef)
	if err != nil || afterCrash.Commit == request.OldCommit || afterCrash.Tree != request.CandidateTree {
		t.Fatalf("ref after crash = %+v, %v", afterCrash, err)
	}
	restarted := fixture.manager(t, journal)
	observation, err := restarted.Promote(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if observation.IntegrationCommit != afterCrash.Commit || observation.IntegrationTree != request.CandidateTree || observation.Trailers["XGoal-Attempt"] != request.AttemptID {
		t.Fatalf("observation = %+v", observation)
	}
	count, err := strconv.Atoi(strings.TrimSpace(runPromotionGit(t, fixture.repository.Root(), "rev-list", "--count", request.IntegrationRef)))
	if err != nil || count != 2 {
		t.Fatalf("commit count = %d, %v", count, err)
	}
	fixture.assertUserResultPreserved(t)
	markerPath := filepath.Join(fixture.runtimeRoot, "promotions", request.ID, "marker.json")
	if info, err := os.Stat(markerPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("marker = %v %v", info, err)
	}
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() { defer wait.Done(); _, err := restarted.Promote(ctx, request); errorsSeen <- err }()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.TrimSpace(runPromotionGit(t, fixture.repository.Root(), "rev-list", "--count", request.IntegrationRef)); got != "2" {
		t.Fatalf("duplicate promotion commits: %s", got)
	}
	fixture.assertUserResultPreserved(t)
}

func TestPromotionRecoversMarkerAndObservationCrashWindows(t *testing.T) {
	for _, boundary := range []string{"commit journal", "observation journal"} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newCurrentPromotionFixture(t)
			journal := &fakeJournal{failCommitOnce: boundary == "commit journal", failObserveOnce: boundary == "observation journal"}
			if _, err := fixture.manager(t, journal).Promote(context.Background(), fixture.request); err == nil {
				t.Fatal("fault was not observed")
			}
			if journal.record.State == promotion.Failed || journal.record.State == promotion.Observed {
				t.Fatalf("crash lost pending effect: %s", journal.record.State)
			}
			if _, err := os.Stat(filepath.Join(fixture.runtimeRoot, "promotions", fixture.request.ID, "marker.json")); err != nil {
				t.Fatalf("durable marker missing: %v", err)
			}
			if _, err := fixture.manager(t, journal).Promote(context.Background(), fixture.request); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(runPromotionGit(t, fixture.repository.Root(), "rev-list", "--count", fixture.request.IntegrationRef)); got != "2" {
				t.Fatalf("recovery created duplicate commits: %s", got)
			}
			if journal.record.State != promotion.Observed {
				t.Fatalf("not observed: %s", journal.record.State)
			}
			fixture.assertUserResultPreserved(t)
		})
	}
}

func TestPromotionRejectsCurrentTreeOrGitIdentityDriftBeforeJournal(t *testing.T) {
	for _, changed := range []string{"source", "index", "branch"} {
		t.Run(changed, func(t *testing.T) {
			fixture := newCurrentPromotionFixture(t)
			switch changed {
			case "source":
				if err := os.WriteFile(filepath.Join(fixture.repository.Root(), "README.md"), []byte("user edit\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "index":
				runPromotionGit(t, fixture.repository.Root(), "add", "README.md")
			case "branch":
				runPromotionGit(t, fixture.repository.Root(), "branch", "user-branch")
				runPromotionGit(t, fixture.repository.Root(), "symbolic-ref", "HEAD", "refs/heads/user-branch")
			}
			index, err := os.ReadFile(filepath.Join(fixture.repository.CommonDir(), "index"))
			if err != nil {
				t.Fatal(err)
			}
			head := runPromotionGit(t, fixture.repository.Root(), "symbolic-ref", "HEAD")
			contents, err := os.ReadFile(filepath.Join(fixture.repository.Root(), "README.md"))
			if err != nil {
				t.Fatal(err)
			}
			journal := &fakeJournal{}
			_, err = fixture.manager(t, journal).Promote(context.Background(), fixture.request)
			if !errors.Is(err, gitrepo.ErrCheckoutChanged) {
				t.Fatalf("drift not rejected: %v", err)
			}
			if journal.record.Version != 0 {
				t.Fatal("stale candidate entered journal")
			}
			afterIndex, _ := os.ReadFile(filepath.Join(fixture.repository.CommonDir(), "index"))
			afterContents, _ := os.ReadFile(filepath.Join(fixture.repository.Root(), "README.md"))
			if !bytes.Equal(index, afterIndex) || !bytes.Equal(contents, afterContents) || runPromotionGit(t, fixture.repository.Root(), "symbolic-ref", "HEAD") != head {
				t.Fatal("drift rejection modified user state")
			}
			ref, err := fixture.repository.ResolveRef(context.Background(), fixture.request.IntegrationRef)
			if err != nil || ref.Commit != fixture.request.OldCommit {
				t.Fatalf("stale candidate advanced ref: %+v %v", ref, err)
			}
		})
	}
}

func TestPromotionRechecksTreeImmediatelyBeforeRefCAS(t *testing.T) {
	fixture := newCurrentPromotionFixture(t)
	journal := &fakeJournal{}
	journal.onPreflight = func(count int) {
		if count == 2 {
			if err := os.WriteFile(filepath.Join(fixture.repository.Root(), "README.md"), []byte("user edit\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, err := fixture.manager(t, journal).Promote(context.Background(), fixture.request)
	if !errors.Is(err, gitrepo.ErrCheckoutChanged) {
		t.Fatalf("pre-CAS drift not rejected: %v", err)
	}
	ref, err := fixture.repository.ResolveRef(context.Background(), fixture.request.IntegrationRef)
	if err != nil || ref.Commit != fixture.request.OldCommit {
		t.Fatalf("ref advanced after drift: %+v %v", ref, err)
	}
	if journal.record.State == promotion.Observed {
		t.Fatal("drifted candidate observed")
	}
}

func TestPromotionRetainsAppliedEffectWhenCheckoutDriftsBeforeObservation(t *testing.T) {
	fixture := newCurrentPromotionFixture(t)
	journal := &fakeJournal{}
	journal.afterRefUpdate = func() {
		if err := os.WriteFile(filepath.Join(fixture.repository.Root(), "README.md"), []byte("user edit\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := fixture.manager(t, journal).Promote(context.Background(), fixture.request)
	if !errors.Is(err, gitrepo.ErrCheckoutChanged) {
		t.Fatalf("observation drift not rejected: %v", err)
	}
	ref, err := fixture.repository.ResolveRef(context.Background(), fixture.request.IntegrationRef)
	if err != nil || ref.Tree != fixture.request.CandidateTree {
		t.Fatalf("expected applied effect: %+v %v", ref, err)
	}
	if journal.record.State != promotion.RefUpdated {
		t.Fatalf("applied effect did not remain pending: %s", journal.record.State)
	}
	contents, err := os.ReadFile(filepath.Join(fixture.repository.Root(), "README.md"))
	if err != nil || string(contents) != "user edit\n" {
		t.Fatal("promotion overwrote the failure scene")
	}
	journal.afterRefUpdate = nil
	if _, err := fixture.manager(t, journal).Promote(context.Background(), fixture.request); !errors.Is(err, gitrepo.ErrCheckoutChanged) {
		t.Fatalf("recovery accepted drift: %v", err)
	}
	// The operator restores the exact known candidate. Recovery itself never
	// rewrites files, HEAD, the index, or the already-applied private ref.
	if err := os.WriteFile(filepath.Join(fixture.repository.Root(), "README.md"), []byte("promoted\n"), 0600); err != nil {
		t.Fatal(err)
	}
	observation, err := fixture.manager(t, journal).Promote(context.Background(), fixture.request)
	if err != nil || observation.IntegrationCommit != ref.Commit {
		t.Fatalf("pending observation did not recover: %+v %v", observation, err)
	}
	fixture.assertUserResultPreserved(t)
}

func TestSequentialPromotionsLeaveUserHEADAndIndexAtOriginalBaseline(t *testing.T) {
	fixture := newCurrentPromotionFixture(t)
	ctx := context.Background()
	first, err := fixture.manager(t, &fakeJournal{}).Promote(ctx, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repository.Root(), "next.txt"), []byte("second work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	next := fixture.request
	next.ID, next.EffectID, next.EffectKey = "promotion_2", "effect_2", "project/goal_1/work_2/attempt_2/promotion/1"
	next.WorkItemID, next.AttemptID, next.LeaseID, next.EvidenceSetID = "work_2", "attempt_2", "lease_2", "evidence_set_2"
	next.OldCommit, next.OldTree = first.IntegrationCommit, first.IntegrationTree
	snapshot, err := fixture.repository.SnapshotTree(ctx, gitrepo.SnapshotSpec{BaseTree: next.OldTree})
	if err != nil {
		t.Fatal(err)
	}
	next.CandidateTree = snapshot.Tree
	second, err := fixture.manager(t, &fakeJournal{}).Promote(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := fixture.repository.ReadCommit(ctx, second.IntegrationCommit)
	if err != nil || commit.Parent != first.IntegrationCommit {
		t.Fatalf("private history does not extend accepted result: %+v %v", commit, err)
	}
	if got := strings.TrimSpace(runPromotionGit(t, fixture.repository.Root(), "rev-list", "--count", next.IntegrationRef)); got != "3" {
		t.Fatalf("sequential history = %s; want base and two promotions", got)
	}
	fixture.assertUserResultPreserved(t)
	if content, err := os.ReadFile(filepath.Join(fixture.repository.Root(), "next.txt")); err != nil || string(content) != "second work\n" {
		t.Fatalf("second result is missing: %q %v", content, err)
	}
}

func TestPromotionCannotFollowPrivateSymrefIntoUserBranch(t *testing.T) {
	fixture := newCurrentPromotionFixture(t)
	runPromotionGit(t, fixture.repository.Root(), "symbolic-ref", fixture.request.IntegrationRef, "refs/heads/main")
	journal := &fakeJournal{}
	if _, err := fixture.manager(t, journal).Promote(context.Background(), fixture.request); err == nil {
		t.Fatal("promotion accepted a private ref that aliases the user branch")
	}
	fixture.assertUserResultPreserved(t)
	if journal.record.State == promotion.Observed {
		t.Fatal("symbolic private ref was accepted as a completed promotion")
	}
}

func TestCurrentPromotionRejectsMixedOrForeignExecutionContracts(t *testing.T) {
	fixture := newCurrentPromotionFixture(t)
	for _, mutate := range []struct {
		name  string
		apply func(*promotion.Request)
	}{
		{"legacy branch", func(r *promotion.Request) { r.IntegrationRef = "refs/heads/main" }},
		{"another Goal ref", func(r *promotion.Request) { r.IntegrationRef = "refs/xgoal/goals/other/integration" }},
		{"legacy execution path", func(r *promotion.Request) { r.ValidationWorktree = "/legacy/tree" }},
		{"missing identity", func(r *promotion.Request) { r.CheckoutIdentity = nil }},
		{"foreign root", func(r *promotion.Request) { r.ExecutionPath = filepath.Dir(r.ExecutionPath) }},
		{"excluded source root", func(r *promotion.Request) { r.ExcludePaths = []string{r.ExecutionPath} }},
		{"relative exclusion", func(r *promotion.Request) { r.ExcludePaths = []string{"runtime"} }},
		{"escaping marker", func(r *promotion.Request) { r.ID = "../outside" }},
		{"unknown model", func(r *promotion.Request) { r.ExecutionModel = "unknown" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			request := fixture.request
			mutate.apply(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("invalid current-directory contract accepted")
			}
		})
	}
	request := fixture.request
	request.OldTree = request.CandidateTree
	journal := &fakeJournal{}
	if _, err := fixture.manager(t, journal).Promote(context.Background(), request); err == nil {
		t.Fatal("base commit/tree mismatch accepted")
	}
	if journal.record.Version != 0 {
		t.Fatal("incorrect base entered journal")
	}
}

type fakeJournal struct {
	mu                sync.Mutex
	record            promotion.Record
	failRefUpdateOnce bool
	failCommitOnce    bool
	failObserveOnce   bool
	preflightCount    int
	onPreflight       func(int)
	afterRefUpdate    func()
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

func (journal *fakeJournal) Preflight(context.Context, promotion.Request) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.preflightCount++
	if journal.onPreflight != nil {
		journal.onPreflight(journal.preflightCount)
	}
	return nil
}

func (journal *fakeJournal) RecordCommit(_ context.Context, id, commit string) (promotion.Record, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.failCommitOnce {
		journal.failCommitOnce = false
		return promotion.Record{}, errors.New("injected database outage after marker publication")
	}
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
	if journal.afterRefUpdate != nil {
		journal.afterRefUpdate()
	}
	return journal.record, nil
}

func (journal *fakeJournal) Observe(_ context.Context, id string, observation promotion.Observation) (promotion.Record, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.failObserveOnce {
		journal.failObserveOnce = false
		return promotion.Record{}, errors.New("injected database outage before observation")
	}
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
