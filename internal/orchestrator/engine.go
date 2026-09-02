// Package orchestrator joins xgoal's deterministic components into the v0.1
// single-writer execution loop. Agent output remains an untrusted claim; only
// captured Git state and current validator evidence can advance integration.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
	claudeadapter "github.com/monshunter/xgoal/internal/adapter/claude"
	codexadapter "github.com/monshunter/xgoal/internal/adapter/codex"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/finalize"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/patch"
	"github.com/monshunter/xgoal/internal/promotion"
	"github.com/monshunter/xgoal/internal/reconcile"
	finalreport "github.com/monshunter/xgoal/internal/report"
	"github.com/monshunter/xgoal/internal/review"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/workpacket"
	"github.com/monshunter/xgoal/internal/workspace"
)

const (
	maxAgentOutput = int64(16 << 20)
	maxPatchFile   = int64(64 << 20)
)

type Engine struct {
	store       *sqlite.Store
	projectRoot string
	runtimeRoot string
	config      config.Config
	configHash  string
	repository  *gitrepo.Repository
	workspaces  *workspace.Manager
	packets     *workpacket.Store
	patches     *patch.Store
	environment *environment.Local
	promotions  *promotion.Manager
	reviews     *review.Coordinator
	reviewStore *review.Store
	finalizer   *finalize.Manager
	adapters    map[string]adapter.Adapter
	reviewers   map[string]review.Adapter
	profiles    map[string]config.Agent

	queue    chan string
	mu       sync.Mutex
	runs     map[string]context.CancelFunc
	workRuns map[string]context.CancelCauseFunc
	serial   sync.Mutex
}

func New(ctx context.Context, store *sqlite.Store, projectRoot string, configuration config.Config) (*Engine, error) {
	if store == nil || !filepath.IsAbs(projectRoot) || filepath.Clean(projectRoot) != projectRoot {
		return nil, errors.New("orchestrator requires a store and clean absolute project root")
	}
	if err := configuration.Validate(); err != nil {
		return nil, err
	}
	repository, err := gitrepo.Open(ctx, projectRoot)
	if err != nil {
		return nil, err
	}
	if repository.Root() != projectRoot {
		return nil, errors.New("orchestrator project root does not match Git root")
	}
	runtimeRoot := store.Info().ProjectDir
	workspaces, err := workspace.NewManager(runtimeRoot, repository)
	if err != nil {
		return nil, err
	}
	packets, err := workpacket.NewStore(runtimeRoot)
	if err != nil {
		return nil, err
	}
	patches, err := patch.NewStore(runtimeRoot)
	if err != nil {
		return nil, err
	}
	local, err := environment.NewLocal(runtimeRoot, repository, clock.Real{})
	if err != nil {
		return nil, err
	}
	promotions, err := promotion.NewManager(runtimeRoot, repository, store)
	if err != nil {
		return nil, err
	}
	reviews, err := review.NewCoordinator(runtimeRoot)
	if err != nil {
		return nil, err
	}
	reviewStore, err := review.NewStore(runtimeRoot)
	if err != nil {
		return nil, err
	}
	files, err := finalreport.NewFileManager(runtimeRoot)
	if err != nil {
		return nil, err
	}
	finalizer, err := finalize.New(store, files)
	if err != nil {
		return nil, err
	}
	configHash, err := configuration.Hash()
	if err != nil {
		return nil, err
	}
	engine := &Engine{
		store: store, projectRoot: projectRoot, runtimeRoot: runtimeRoot,
		config: configuration, configHash: configHash, repository: repository,
		workspaces: workspaces, packets: packets, patches: patches, environment: local,
		promotions: promotions, reviews: reviews, reviewStore: reviewStore, finalizer: finalizer,
		adapters: make(map[string]adapter.Adapter), reviewers: make(map[string]review.Adapter),
		profiles: make(map[string]config.Agent), queue: make(chan string, 128),
		runs: make(map[string]context.CancelFunc), workRuns: make(map[string]context.CancelCauseFunc),
	}
	for _, profile := range configuration.Agents {
		runtimeAdapter, err := newAdapter(profile, runtimeRoot, projectRoot)
		if err != nil {
			return nil, fmt.Errorf("agent profile %q: %w", profile.ID, err)
		}
		engine.profiles[profile.ID] = profile
		engine.adapters[profile.ID] = runtimeAdapter
		if reviewer, ok := runtimeAdapter.(review.Adapter); ok {
			engine.reviewers[profile.ID] = reviewer
		}
	}
	if _, err := engine.implementationProfile(domain.RoleImplementer); err != nil {
		return nil, err
	}
	return engine, nil
}

func newAdapter(profile config.Agent, runtimeRoot, projectRoot string) (adapter.Adapter, error) {
	environmentValues := make(map[string]string)
	for _, name := range profile.EnvironmentAllowlist {
		if value, exists := os.LookupEnv(name); exists {
			environmentValues[name] = value
		}
	}
	switch profile.Adapter {
	case "codex-cli":
		return codexadapter.New(codexadapter.Config{Binary: profile.Command, RuntimeRoot: runtimeRoot, ProjectRoot: projectRoot, Environment: environmentValues})
	case "claude-cli":
		return claudeadapter.New(claudeadapter.Config{Binary: profile.Command, RuntimeRoot: runtimeRoot, ProjectRoot: projectRoot, Environment: environmentValues})
	default:
		return nil, fmt.Errorf("adapter %q is not executable by the daemon", profile.Adapter)
	}
}

// Wake requests a fresh Store read. It is safe to call repeatedly and never
// carries mutable state from an API request into the execution loop.
func (engine *Engine) Wake(goalID string) {
	if strings.TrimSpace(goalID) == "" {
		return
	}
	select {
	case engine.queue <- goalID:
	default:
	}
}

// CancelGoal cancels only the active invocation. Persisted pause/cancel state
// remains owned by the control service and is re-read before every next step.
func (engine *Engine) CancelGoal(goalID string) {
	engine.mu.Lock()
	cancel := engine.runs[goalID]
	engine.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// CancelWork interrupts only the matching in-flight Attempt. The control
// service has already terminalized the Work and revoked its Lease in Store.
func (engine *Engine) CancelWork(workID string) {
	engine.mu.Lock()
	cancel := engine.workRuns[workID]
	engine.mu.Unlock()
	if cancel != nil {
		cancel(errors.New("work item cancelled by operator"))
	}
}

func (engine *Engine) registerWork(workID string, cancel context.CancelCauseFunc) {
	engine.mu.Lock()
	engine.workRuns[workID] = cancel
	engine.mu.Unlock()
}

func (engine *Engine) unregisterWork(workID string) {
	engine.mu.Lock()
	delete(engine.workRuns, workID)
	engine.mu.Unlock()
}

// Run services the project-wide serial slot until ctx is cancelled.
func (engine *Engine) Run(ctx context.Context) {
	engine.enqueueRunnable(ctx)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			engine.cancelAll()
			return
		case goalID := <-engine.queue:
			if err := engine.RunGoal(ctx, goalID); err != nil {
				_ = engine.stopInvariant(ctx, goalID, err)
			}
		case <-ticker.C:
			engine.enqueueRunnable(ctx)
		}
	}
}

func (engine *Engine) enqueueRunnable(ctx context.Context) {
	ids, err := engine.store.RunnableGoalIDs(ctx)
	if err != nil {
		return
	}
	for _, id := range ids {
		engine.Wake(id)
	}
}

// Recover completes or deterministically reconciles Promotion effects that
// crossed a process boundary after their request was persisted.
func (engine *Engine) Recover(ctx context.Context) error {
	records, err := engine.store.RecoverablePromotions(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		observation, promoteErr := engine.promotions.Promote(ctx, record.Request)
		if promoteErr == nil {
			if observation.IntegrationTree != record.CandidateTree {
				return errors.New("recovered Promotion observation tree mismatch")
			}
			if _, err := engine.store.RefreshReadyWork(ctx, record.GoalID, event("WorkReady", "recovery", map[string]any{"recovered_promotion": record.ID})); err != nil {
				return err
			}
			continue
		}
		attempt, attemptErr := engine.store.Attempt(ctx, record.AttemptID)
		work, workErr := engine.store.WorkItem(ctx, record.WorkItemID)
		goal, goalErr := engine.store.Goal(ctx, record.GoalID)
		revision, revisionErr := engine.activeGoalRevision(ctx, goal)
		if errors.Join(attemptErr, workErr, goalErr, revisionErr) == nil && work.State == domain.WorkReconciling &&
			(attempt.State == domain.AttemptFailed || attempt.State == domain.AttemptQuarantined) {
			if err := engine.recordAndWait(ctx, goal, work, revision, attempt, reconcile.PatchConflict, promoteErr, attempt.AgentProfileID, record.BundleHash); err != nil {
				return err
			}
			continue
		}
		return errors.Join(promoteErr, attemptErr, workErr, goalErr, revisionErr)
	}
	return nil
}

func (engine *Engine) activeGoalRevision(ctx context.Context, goal domain.Goal) (domain.GoalRevision, error) {
	if goal.ID == "" || goal.ActiveRevisionID == "" {
		return domain.GoalRevision{}, errors.New("Goal has no active revision")
	}
	return engine.store.GoalRevision(ctx, goal.ActiveRevisionID)
}

func (engine *Engine) cancelAll() {
	engine.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(engine.runs))
	for _, cancel := range engine.runs {
		cancels = append(cancels, cancel)
	}
	engine.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// RunGoal executes one Goal to its next durable terminal/waiting point. The
// serial mutex mirrors the Store's project-wide one-active-Lease invariant.
func (engine *Engine) RunGoal(parent context.Context, goalID string) error {
	engine.serial.Lock()
	defer engine.serial.Unlock()
	ctx, cancel := context.WithCancel(parent)
	engine.mu.Lock()
	if _, running := engine.runs[goalID]; running {
		engine.mu.Unlock()
		cancel()
		return nil
	}
	engine.runs[goalID] = cancel
	engine.mu.Unlock()
	defer func() {
		cancel()
		engine.mu.Lock()
		delete(engine.runs, goalID)
		engine.mu.Unlock()
	}()

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		goal, err := engine.store.Goal(ctx, goalID)
		if err != nil {
			return err
		}
		switch goal.State {
		case domain.GoalRunning:
			if _, err := engine.store.RefreshReadyWork(ctx, goalID, event("WorkReady", "kernel", map[string]any{"source": "orchestrator"})); err != nil {
				return err
			}
			work, err := engine.store.NextReadyWork(ctx, goalID)
			if err == nil {
				if err := engine.executeWork(ctx, goal, work); err != nil {
					current, readErr := engine.store.WorkItem(context.Background(), work.ID)
					if readErr == nil && current.State == domain.WorkCancelled {
						continue
					}
					return err
				}
				continue
			}
			if !errors.Is(err, basestore.ErrNotFound) {
				return err
			}
			complete, err := engine.requiredWorkComplete(ctx, goalID)
			if err != nil || !complete {
				return err
			}
			if err := engine.finalizeGoal(ctx, goal); err != nil {
				return err
			}
			return nil
		case domain.GoalVerifying:
			if err := engine.finalizeGoal(ctx, goal); err != nil {
				return err
			}
			return nil
		default:
			return nil
		}
	}
}

func (engine *Engine) requiredWorkComplete(ctx context.Context, goalID string) (bool, error) {
	items, err := engine.store.GoalWorkItems(ctx, goalID)
	if err != nil {
		return false, err
	}
	if len(items) == 0 {
		return false, nil
	}
	for _, item := range items {
		if item.Required && item.State != domain.WorkCompleted {
			return false, nil
		}
	}
	return true, nil
}
