package control_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/control"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/goalcompile"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestGoalLifecycleThroughControlService(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), clock.NewFake(time.Date(2026, 9, 2, 22, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := control.New(store, root)
	if err != nil {
		t.Fatal(err)
	}
	status, response, err := service.Execute(ctx, api.Operation{Name: "goal.create", Body: raw(map[string]any{"goal_id": "goal_api", "raw_goal": "implement bounded change", "mode": "standard"})})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create status=%d response=%+v err=%v", status, response, err)
	}
	status, response, err = service.Query(ctx, api.Operation{Name: "goal.get", ResourceID: "goal_api"})
	if err != nil || status != http.StatusOK {
		t.Fatalf("get status=%d response=%+v err=%v", status, response, err)
	}
	view := response.(map[string]any)
	if revision, ok := view["goal_revision"].(*sqlite.GoalRevisionSummary); !ok || revision != nil {
		t.Fatalf("draft Goal unexpectedly has a frozen revision: %+v", view["goal_revision"])
	}
	events, err := service.Events(ctx, "goal_api", "", 10)
	if err != nil || len(events) != 1 || events[0].EventType != "GoalCreated" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestGoalCreateCompilesTrustedPlannerProposalIntoRunningGraph(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	configuration, err := os.ReadFile("../../xgoal.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), configuration, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := control.New(store, root)
	if err != nil {
		t.Fatal(err)
	}
	contract := goalcompile.Contract{
		Summary: "implement", Rationale: "value", InScope: []string{"repository"}, OutOfScope: []string{"production"}, Constraints: []string{"no push"},
		AcceptanceCriteria: []goalcompile.AcceptanceCriterion{{ID: "AC-1", Statement: "tests pass", Validators: []string{"go-test-all"}}},
		QualityAttributes:  []string{"correctness"}, HumanGates: []string{"scope expansion"}, CompletionPolicy: goalcompile.CompletionPolicy{RequireAllRequiredItems: true, RequireNoBlockingFindings: true, RequireFinalValidation: true},
	}
	plan := goalcompile.Plan{Summary: "one item", WorkItems: []goalcompile.PlanWork{{ClientKey: "implement", Title: "implement", Objective: "make change", ReadScope: []string{"/**"}, WriteScope: []string{"/internal/**"}, AcceptanceCriteria: []string{"AC-1"}, Validators: []string{"go-test-all"}, RecommendedRole: "implementer", Required: true}}}
	status, response, err := service.Execute(ctx, api.Operation{Name: "goal.create", Body: raw(map[string]any{"goal_id": "goal_compiled", "raw_goal": "implement", "mode": "standard", "created_by": "tester", "proposal": map[string]any{"contract": contract, "plan": plan}})})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create compiled status=%d response=%+v err=%v", status, response, err)
	}
	persisted, err := store.Goal(ctx, "goal_compiled")
	if err != nil || persisted.State != "RUNNING" || persisted.ActiveRevisionID == "" {
		t.Fatalf("compiled goal = %+v, %v", persisted, err)
	}
	items, err := store.GoalWorkItems(ctx, persisted.ID)
	if err != nil || len(items) != 1 || items[0].State != "READY" {
		t.Fatalf("compiled work = %+v, %v", items, err)
	}
}

func TestControlServicePauseResumeReplanAndCancelPreserveHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	configuration, err := os.ReadFile("../../xgoal.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), configuration, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), clock.NewFake(time.Date(2026, 9, 2, 23, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := control.New(store, root)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &lifecycleRecorder{}
	service.SetLifecycle(lifecycle)

	status, _, err := service.Execute(ctx, api.Operation{Name: "goal.create", Body: raw(map[string]any{
		"goal_id": "goal_lifecycle", "raw_goal": "implement", "mode": "standard", "proposal": controlProposal(),
	})})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create status=%d err=%v", status, err)
	}
	goal, err := store.Goal(ctx, "goal_lifecycle")
	if err != nil || goal.State != domain.GoalRunning || goal.Version != 3 {
		t.Fatalf("created goal = %+v, %v", goal, err)
	}
	goalRevisionID := goal.ActiveRevisionID
	initialWork, err := store.GoalWorkItems(ctx, goal.ID)
	if err != nil || len(initialWork) != 1 {
		t.Fatalf("initial work = %+v, %v", initialWork, err)
	}
	oldWorkID := initialWork[0].ID

	status, _, err = service.Execute(ctx, api.Operation{Name: "goal.pause", ResourceID: goal.ID, Body: raw(map[string]any{"expected_version": goal.Version, "reason": "operator pause"})})
	if err != nil || status != http.StatusOK {
		t.Fatalf("pause status=%d err=%v", status, err)
	}
	goal, _ = store.Goal(ctx, goal.ID)
	if goal.State != domain.GoalWaiting || goal.Version != 4 || goal.ActiveRevisionID != goalRevisionID {
		t.Fatalf("paused goal = %+v", goal)
	}

	status, _, err = service.Execute(ctx, api.Operation{Name: "goal.resume", ResourceID: goal.ID, Body: raw(map[string]any{"expected_version": goal.Version, "reason": "operator resume"})})
	if err != nil || status != http.StatusOK {
		t.Fatalf("resume status=%d err=%v", status, err)
	}
	goal, _ = store.Goal(ctx, goal.ID)
	if goal.State != domain.GoalRunning || goal.Version != 5 || goal.ActiveRevisionID != goalRevisionID {
		t.Fatalf("resumed goal = %+v", goal)
	}

	replacement := domain.WorkItem{
		ID: "goal_lifecycle_work_replanned", State: domain.WorkPending, Title: "replacement", Objective: "replace the original work",
		ReadScope: []string{"/**"}, WriteScope: []string{"/internal/**"}, AcceptanceCriteria: []string{"AC-1"}, ValidatorIDs: []string{"go-test-all"}, RecommendedRole: domain.RoleImplementer, Required: true, Version: 1,
	}
	status, response, err := service.Execute(ctx, api.Operation{Name: "goal.replan", ResourceID: goal.ID, Body: raw(map[string]any{
		"plan_revision_id": "goal_lifecycle_plan_2", "goal_revision_id": goalRevisionID, "revision": 2,
		"expected_goal_version": goal.Version, "work_items": []domain.WorkItem{replacement}, "dependencies": []domain.WorkDependency{},
		"reason": "replace obsolete work", "impact_analysis": "the original READY item is superseded without deleting its history",
	})})
	if err != nil || status != http.StatusOK {
		t.Fatalf("replan status=%d response=%+v err=%v", status, response, err)
	}
	goal, _ = store.Goal(ctx, goal.ID)
	if goal.State != domain.GoalRunning || goal.Version != 6 || goal.ActiveRevisionID != goalRevisionID {
		t.Fatalf("replanned goal = %+v", goal)
	}
	oldPlan, err := store.PlanRevision(ctx, "goal_lifecycle_plan_1")
	if err != nil || oldPlan.Status != domain.PlanSuperseded {
		t.Fatalf("old plan = %+v, %v", oldPlan, err)
	}
	newPlan, err := store.PlanRevision(ctx, "goal_lifecycle_plan_2")
	if err != nil || newPlan.Status != domain.PlanActive {
		t.Fatalf("new plan = %+v, %v", newPlan, err)
	}
	oldWork, err := store.WorkItem(ctx, oldWorkID)
	if err != nil || oldWork.State != domain.WorkReady {
		t.Fatalf("superseded work history = %+v, %v", oldWork, err)
	}
	newWork, err := store.WorkItem(ctx, replacement.ID)
	if err != nil || newWork.State != domain.WorkReady {
		t.Fatalf("replacement work = %+v, %v", newWork, err)
	}

	status, _, err = service.Execute(ctx, api.Operation{Name: "goal.cancel", ResourceID: goal.ID, Body: raw(map[string]any{"expected_version": goal.Version, "reason": "operator cancel"})})
	if err != nil || status != http.StatusOK {
		t.Fatalf("cancel status=%d err=%v", status, err)
	}
	goal, _ = store.Goal(ctx, goal.ID)
	if goal.State != domain.GoalCancelled || goal.Version != 7 || goal.ActiveRevisionID != goalRevisionID {
		t.Fatalf("cancelled goal = %+v", goal)
	}
	if len(lifecycle.wakes) != 3 || len(lifecycle.cancels) != 2 {
		t.Fatalf("lifecycle wakes=%v cancels=%v", lifecycle.wakes, lifecycle.cancels)
	}
	events, err := store.GoalEventsAfter(ctx, goal.ID, "", 100)
	if err != nil || !eventsInclude(events, "GoalPaused", "GoalResumed", "PlanReplaced", "GoalCancelled") {
		t.Fatalf("goal events=%+v err=%v", events, err)
	}
	var foundImpact bool
	for _, event := range events {
		if event.EventType == "PlanReplaced" && strings.Contains(string(event.Payload), "original READY item") {
			foundImpact = true
		}
	}
	if !foundImpact {
		t.Fatalf("replan impact analysis not retained: %+v", events)
	}
}

func TestControlServiceRetryKeepsPriorAttemptAndCreatesANewGeneration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	configuration, err := os.ReadFile("../../xgoal.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), configuration, 0o600); err != nil {
		t.Fatal(err)
	}
	source := clock.NewFake(time.Date(2026, 9, 2, 23, 30, 0, 0, time.UTC))
	store, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := control.New(store, root)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &lifecycleRecorder{}
	service.SetLifecycle(lifecycle)
	if status, _, err := service.Execute(ctx, api.Operation{Name: "goal.create", Body: raw(map[string]any{
		"goal_id": "goal_retry", "raw_goal": "implement", "mode": "standard", "proposal": controlProposal(),
	})}); err != nil || status != http.StatusCreated {
		t.Fatalf("create status=%d err=%v", status, err)
	}
	goalWork, err := store.GoalWorkItems(ctx, "goal_retry")
	if err != nil || len(goalWork) != 1 {
		t.Fatalf("goal work = %+v, %v", goalWork, err)
	}
	work := goalWork[0]
	firstAttempt := domain.Attempt{ID: "attempt_retry_1", WorkItemID: work.ID, AgentProfileID: "codex", State: domain.AttemptCreated, BaseTree: "base", PacketHash: "packet-1", Version: 1}
	firstLease, err := store.ClaimWork(ctx, work.ID, work.Version, sqlite.LeaseDraft{ID: "lease_retry_1", Holder: "daemon/fixture", TTL: time.Second}, firstAttempt, sqlite.EventInput{Type: "WorkClaimed", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goal(ctx, "goal_retry")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.Execute(ctx, api.Operation{Name: "goal.replan", ResourceID: goal.ID, Body: raw(map[string]any{
		"plan_revision_id": "goal_retry_plan_2", "goal_revision_id": goal.ActiveRevisionID, "revision": 2,
		"expected_goal_version": goal.Version, "work_items": []domain.WorkItem{{
			ID: "goal_retry_replanned_work", State: domain.WorkPending, Title: "replacement", Objective: "replace active work",
			ReadScope: []string{"/**"}, WriteScope: []string{"/internal/**"}, AcceptanceCriteria: []string{"AC-1"}, ValidatorIDs: []string{"go-test-all"}, RecommendedRole: domain.RoleImplementer, Required: true, Version: 1,
		}}, "dependencies": []domain.WorkDependency{}, "reason": "unsafe concurrent replan", "impact_analysis": "would supersede a plan that still owns an active Lease",
	})})
	var apiError *api.APIError
	if !errors.As(err, &apiError) || apiError.Status != http.StatusConflict {
		t.Fatalf("active-Lease replan error=%v, want conflict", err)
	}
	activePlan, err := store.PlanRevision(ctx, "goal_retry_plan_1")
	if err != nil || activePlan.Status != domain.PlanActive {
		t.Fatalf("active plan after rejected replan = %+v, %v", activePlan, err)
	}
	source.Advance(2 * time.Second)
	if _, err := store.ResolveExpiredLease(ctx, firstLease.ID, firstLease.Generation, firstLease.Version, true, sqlite.EventInput{Type: "LeaseExpired", ActorType: "kernel", Payload: map[string]any{"worker_stopped": true}}); err != nil {
		t.Fatal(err)
	}
	work, _ = store.WorkItem(ctx, work.ID)
	if work.State != domain.WorkReconciling || work.Version != 4 {
		t.Fatalf("reconciling work = %+v", work)
	}
	status, response, err := service.Execute(ctx, api.Operation{Name: "work.retry", ResourceID: work.ID, Body: raw(map[string]any{"expected_version": work.Version, "reason": "retry with fresh packet"})})
	if err != nil || status != http.StatusOK {
		t.Fatalf("retry status=%d response=%+v err=%v", status, response, err)
	}
	work, _ = store.WorkItem(ctx, work.ID)
	if work.State != domain.WorkReady || work.Version != 5 {
		t.Fatalf("retried work = %+v", work)
	}
	statusView, err := store.GoalStatus(ctx, "goal_retry")
	if err != nil || len(statusView.Attempts) != 1 || statusView.Attempts[0].ID != firstAttempt.ID {
		t.Fatalf("attempt history after retry = %+v, %v", statusView.Attempts, err)
	}
	secondAttempt := domain.Attempt{ID: "attempt_retry_2", WorkItemID: work.ID, AgentProfileID: "claude", State: domain.AttemptCreated, BaseTree: "base", PacketHash: "packet-2", Version: 1}
	secondLease, err := store.ClaimWork(ctx, work.ID, work.Version, sqlite.LeaseDraft{ID: "lease_retry_2", Holder: "daemon/fixture", TTL: time.Minute}, secondAttempt, sqlite.EventInput{Type: "WorkClaimed", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if secondLease.Generation != 2 {
		t.Fatalf("retry lease generation=%d, want 2", secondLease.Generation)
	}
	statusView, err = store.GoalStatus(ctx, "goal_retry")
	if err != nil || len(statusView.Attempts) != 2 || statusView.Attempts[0].ID == statusView.Attempts[1].ID {
		t.Fatalf("attempt history after new claim = %+v, %v", statusView.Attempts, err)
	}
	work, err = store.WorkItem(ctx, work.ID)
	if err != nil || work.State != domain.WorkClaimed || work.Version != 6 {
		t.Fatalf("claimed retry work = %+v, %v", work, err)
	}
	status, response, err = service.Execute(ctx, api.Operation{Name: "work.cancel", ResourceID: work.ID, Body: raw(map[string]any{"expected_version": work.Version, "reason": "operator stops this item"})})
	if err != nil || status != http.StatusOK {
		t.Fatalf("work cancel status=%d response=%+v err=%v", status, response, err)
	}
	work, _ = store.WorkItem(ctx, work.ID)
	interrupted, _ := store.Attempt(ctx, secondAttempt.ID)
	revoked, _ := store.Lease(ctx, secondLease.ID)
	goal, _ = store.Goal(ctx, goal.ID)
	if work.State != domain.WorkCancelled || work.Version != 7 || interrupted.State != domain.AttemptInterrupted || revoked.State != domain.LeaseRevoked || goal.State != domain.GoalWaiting {
		t.Fatalf("cancelled work=%+v attempt=%+v lease=%+v", work, interrupted, revoked)
	}
	statusView, err = store.GoalStatus(ctx, "goal_retry")
	if err != nil || len(statusView.Attempts) != 2 || statusView.Attempts[0].ID != firstAttempt.ID || statusView.Attempts[1].ID != secondAttempt.ID {
		t.Fatalf("history after work cancel = %+v, %v", statusView.Attempts, err)
	}
	workEvents, err := store.Events(ctx, "work", work.ID)
	if err != nil || !eventsInclude(workEvents, "WorkRetryReady", "WorkCancelled") {
		t.Fatalf("work events=%+v err=%v", workEvents, err)
	}
	goalEvents, err := store.Events(ctx, "goal", goal.ID)
	if err != nil || !eventsInclude(goalEvents, "GoalWaiting") {
		t.Fatalf("goal events=%+v err=%v", goalEvents, err)
	}
	if len(lifecycle.wakes) != 2 || len(lifecycle.workCancels) != 1 || lifecycle.workCancels[0] != work.ID {
		t.Fatalf("lifecycle wakes=%v work-cancels=%v", lifecycle.wakes, lifecycle.workCancels)
	}
}

func TestGoalCreateRejectsInvalidProvidedProposalBeforePersistingDraft(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	configuration, err := os.ReadFile("../../xgoal.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), configuration, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := control.New(store, root)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.Execute(ctx, api.Operation{Name: "goal.create", Body: raw(map[string]any{
		"goal_id": "goal_invalid_proposal", "raw_goal": "implement", "mode": "standard",
		"proposal": map[string]any{"contract": goalcompile.Contract{}, "plan": goalcompile.Plan{}},
	})})
	if err == nil {
		t.Fatal("invalid proposal was accepted")
	}
	if _, readErr := store.Goal(ctx, "goal_invalid_proposal"); !errors.Is(readErr, basestore.ErrNotFound) {
		t.Fatalf("invalid proposal left a persisted Goal: %v", readErr)
	}
}

func TestGoalCreateUsesConfiguredDefaultModeWhenOmitted(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	configuration, err := os.ReadFile("../../xgoal.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	configuration = []byte(strings.Replace(string(configuration), "defaultMode: standard", "defaultMode: fast", 1))
	if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), configuration, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := control.New(store, root)
	if err != nil {
		t.Fatal(err)
	}
	contract := goalcompile.Contract{
		Summary: "implement", Rationale: "value", InScope: []string{"repository"}, OutOfScope: []string{"production"}, Constraints: []string{"no push"},
		AcceptanceCriteria: []goalcompile.AcceptanceCriterion{{ID: "AC-1", Statement: "tests pass", Validators: []string{"go-test-all"}}},
		QualityAttributes:  []string{"correctness"}, HumanGates: []string{"scope expansion"}, CompletionPolicy: goalcompile.CompletionPolicy{RequireAllRequiredItems: true, RequireNoBlockingFindings: true, RequireFinalValidation: true},
	}
	plan := goalcompile.Plan{Summary: "one item", WorkItems: []goalcompile.PlanWork{{ClientKey: "implement", Title: "implement", Objective: "make change", ReadScope: []string{"/**"}, WriteScope: []string{"/internal/**"}, AcceptanceCriteria: []string{"AC-1"}, Validators: []string{"go-test-all"}, RecommendedRole: "implementer", Required: true}}}
	status, _, err := service.Execute(ctx, api.Operation{Name: "goal.create", Body: raw(map[string]any{"goal_id": "goal_default_mode", "raw_goal": "implement", "proposal": map[string]any{"contract": contract, "plan": plan}})})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create status=%d err=%v", status, err)
	}
	goal, err := store.Goal(ctx, "goal_default_mode")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := store.GoalRevision(ctx, goal.ActiveRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(revision.ContractJSON), `"mode":"fast"`) {
		t.Fatalf("configured default mode was not frozen: %s", revision.ContractJSON)
	}
}

func TestStrictRequestRejectsUnknownFields(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, _ := control.New(store, root)
	_, _, err = service.Execute(ctx, api.Operation{Name: "goal.create", Body: raw(map[string]any{"goal_id": "goal_api", "raw_goal": "x", "unexpected": true})})
	if err == nil {
		t.Fatal("unknown request field was accepted")
	}
}

func raw(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func controlProposal() map[string]any {
	return map[string]any{
		"contract": goalcompile.Contract{
			Summary: "implement", Rationale: "value", InScope: []string{"repository"}, OutOfScope: []string{"production"}, Constraints: []string{"no push"},
			AcceptanceCriteria: []goalcompile.AcceptanceCriterion{{ID: "AC-1", Statement: "tests pass", Validators: []string{"go-test-all"}}},
			QualityAttributes:  []string{"correctness"}, HumanGates: []string{"scope expansion"}, CompletionPolicy: goalcompile.CompletionPolicy{RequireAllRequiredItems: true, RequireNoBlockingFindings: true, RequireFinalValidation: true},
		},
		"plan": goalcompile.Plan{Summary: "one item", WorkItems: []goalcompile.PlanWork{{ClientKey: "implement", Title: "implement", Objective: "make change", ReadScope: []string{"/**"}, WriteScope: []string{"/internal/**"}, AcceptanceCriteria: []string{"AC-1"}, Validators: []string{"go-test-all"}, RecommendedRole: "implementer", Required: true}}},
	}
}

type lifecycleRecorder struct {
	wakes       []string
	cancels     []string
	workCancels []string
}

func (recorder *lifecycleRecorder) Wake(goalID string) {
	recorder.wakes = append(recorder.wakes, goalID)
}

func (recorder *lifecycleRecorder) CancelGoal(goalID string) {
	recorder.cancels = append(recorder.cancels, goalID)
}

func (recorder *lifecycleRecorder) CancelWork(workID string) {
	recorder.workCancels = append(recorder.workCancels, workID)
}

func eventsInclude(events []domain.Event, eventTypes ...string) bool {
	found := make(map[string]bool, len(events))
	for _, event := range events {
		found[event.EventType] = true
	}
	for _, eventType := range eventTypes {
		if !found[eventType] {
			return false
		}
	}
	return true
}
