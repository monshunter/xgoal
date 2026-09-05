package orchestrator

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/goalcompile"
	"github.com/monshunter/xgoal/internal/planner"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

type planningAdapter struct {
	adapter.Adapter
	calls int
	plan  func(context.Context, planner.Invocation) (planner.Execution, error)
}

func (runtime *planningAdapter) Probe(context.Context, adapter.ProbeSpec) (adapter.Capabilities, error) {
	return adapter.Capabilities{Version: "fixture"}, nil
}
func (runtime *planningAdapter) Plan(ctx context.Context, invocation planner.Invocation, _ adapter.EventSink) (planner.Execution, error) {
	runtime.calls++
	if _, err := planner.ValidateInvocation(invocation); err != nil {
		return planner.Execution{}, err
	}
	return runtime.plan(ctx, invocation)
}

func TestPlanningSavedObservationPublishesAfterResumeWithoutProviderReplay(t *testing.T) {
	engine, request, runtime := planningFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	runtime.plan = func(context.Context, planner.Invocation) (planner.Execution, error) {
		close(started)
		<-release
		return planner.Execution{Proposal: planningProposal(), SessionID: "independent-planner"}, nil
	}
	done := make(chan error, 1)
	go func() { done <- engine.runPlanning(context.Background(), request.GoalID) }()
	<-started
	record, err := engine.store.Planning(context.Background(), request.GoalID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.store.SetPlanningPaused(context.Background(), request.GoalID, record.Goal.Version, true, "inspect before publishing"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	record, err = engine.store.Planning(context.Background(), request.GoalID)
	if err != nil || record.Observation == nil || record.Observation.Proposal == nil || record.Goal.ActiveRevisionID != "" || record.State != "PAUSED" {
		t.Fatalf("paused observation=%+v err=%v", record, err)
	}
	if _, err := engine.store.SetPlanningPaused(context.Background(), request.GoalID, record.Goal.Version, false, "publish retained result"); err != nil {
		t.Fatal(err)
	}
	if err := engine.runPlanning(context.Background(), request.GoalID); err != nil {
		t.Fatal(err)
	}
	record, err = engine.store.Planning(context.Background(), request.GoalID)
	if err != nil || record.Goal.State != domain.GoalRunning || record.Goal.ActiveRevisionID == "" || record.Effect.State != domain.EffectSucceeded || runtime.calls != 1 {
		t.Fatalf("published=%+v calls=%d err=%v", record, runtime.calls, err)
	}
	items, err := engine.store.GoalWorkItems(context.Background(), request.GoalID)
	if err != nil || len(items) != 1 || items[0].State != domain.WorkReady {
		t.Fatalf("graph=%+v err=%v", items, err)
	}
}

func TestPlanningRejectsLateCancellationAndInputDrift(t *testing.T) {
	for _, scenario := range []string{"cancel", "source", "configuration"} {
		t.Run(scenario, func(t *testing.T) {
			engine, request, runtime := planningFixture(t)
			runtime.plan = func(context.Context, planner.Invocation) (planner.Execution, error) {
				switch scenario {
				case "cancel":
					goal, _ := engine.store.Goal(context.Background(), request.GoalID)
					if err := engine.store.UpdateGoalState(context.Background(), goal.ID, goal.Version, domain.GoalCancelled, event("GoalCancelled", "human", map[string]any{"reason": "cancel late planner"})); err != nil {
						return planner.Execution{}, err
					}
				case "source":
					if err := os.WriteFile(filepath.Join(engine.projectRoot, "unexpected.txt"), []byte("preserve this"), 0600); err != nil {
						return planner.Execution{}, err
					}
				case "configuration":
					content, _ := os.ReadFile(filepath.Join(engine.projectRoot, "xgoal.yaml"))
					content = []byte(strings.Replace(string(content), "name: "+engine.config.Metadata.Name, "name: changed", 1))
					if err := os.WriteFile(filepath.Join(engine.projectRoot, "xgoal.yaml"), content, 0600); err != nil {
						return planner.Execution{}, err
					}
				}
				return planner.Execution{Proposal: planningProposal(), SessionID: "planner"}, nil
			}
			if err := engine.runPlanning(context.Background(), request.GoalID); err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			record, err := engine.store.Planning(context.Background(), request.GoalID)
			if err != nil || record.Goal.ActiveRevisionID != "" {
				t.Fatalf("late result published=%+v err=%v", record, err)
			}
			if scenario == "cancel" && record.Goal.State != domain.GoalCancelled {
				t.Fatalf("cancel lost: %+v", record.Goal)
			}
			if scenario != "cancel" && record.State != "WAITING" {
				t.Fatalf("drift not waiting: %+v", record)
			}
		})
	}
}

func planningFixture(t *testing.T) (*Engine, planner.Request, *planningAdapter) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	content, err := os.ReadFile("../../xgoal.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), content, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "xgoal.yaml"}, {"-c", "user.name=xgoal", "-c", "user.email=xgoal@example.invalid", "commit", "--quiet", "-m", "baseline"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git:%s: %v", out, err)
		}
	}
	cfg, err := config.LoadFile(filepath.Join(root, "xgoal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := cfg.Hash()
	state, err := sqlite.Open(context.Background(), filepath.Join(base, "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	repo, err := gitrepo.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	profile := cfg.Agents[1]
	request := planner.Request{ProtocolVersion: planner.RequestVersion, GoalID: "goal_planning", RawGoal: "bounded change", Mode: "standard", CreatedBy: "tester", Generation: 1, ConfigHash: hash, ProfileID: profile.ID, TrustedValidatorIDs: []string{"go-test-all"}}
	if _, _, err := state.AcceptPlanningGoal(context.Background(), "POST /v1/goals", "once", map[string]any{"goal_id": request.GoalID, "raw_goal": request.RawGoal}, request); err != nil {
		t.Fatal(err)
	}
	runtime := &planningAdapter{}
	engine := &Engine{store: state, runtimeContext: context.Background(), projectRoot: root, runtimeRoot: state.Info().ProjectDir, repository: repo, config: cfg, configHash: hash, profiles: map[string]config.Agent{profile.ID: profile}, adapters: map[string]adapter.Adapter{profile.ID: runtime}}
	return engine, request, runtime
}

func planningProposal() planner.Proposal {
	return planner.Proposal{ProtocolVersion: planner.ProposalVersion, Ambiguities: []string{}, Contract: goalcompile.Contract{Summary: "bounded", Rationale: "test durable plan", InScope: []string{"source"}, OutOfScope: []string{"production"}, Constraints: []string{"no push"}, AcceptanceCriteria: []goalcompile.AcceptanceCriterion{{ID: "AC-1", Statement: "tests pass", Validators: []string{"go-test-all"}}}, QualityAttributes: []string{"correctness"}, HumanGates: []string{"scope expansion"}, CompletionPolicy: goalcompile.CompletionPolicy{RequireAllRequiredItems: true, RequireNoBlockingFindings: true, RequireFinalValidation: true}}, Plan: goalcompile.Plan{Summary: "one step", WorkItems: []goalcompile.PlanWork{{ClientKey: "change", Title: "change", Objective: "bounded change", ReadScope: []string{"/**"}, WriteScope: []string{"/internal/**"}, AcceptanceCriteria: []string{"AC-1"}, Validators: []string{"go-test-all"}, RecommendedRole: domain.RoleImplementer, Required: true}}}}
}
