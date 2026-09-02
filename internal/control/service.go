package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

type Service struct {
	store       *sqlite.Store
	projectRoot string
}

func New(store *sqlite.Store, projectRoot string) (*Service, error) {
	if store == nil || !filepath.IsAbs(projectRoot) || filepath.Clean(projectRoot) != projectRoot {
		return nil, errors.New("control service requires a store and clean absolute project root")
	}
	return &Service{store: store, projectRoot: projectRoot}, nil
}

func (service *Service) Events(ctx context.Context, goalID, afterID string, limit int) ([]domain.Event, error) {
	return service.store.GoalEventsAfter(ctx, goalID, afterID, limit)
}

func (service *Service) Execute(ctx context.Context, operation api.Operation) (int, any, error) {
	switch operation.Name {
	case "project.init":
		var request struct{}
		if err := api.DecodeStrict(operation.Body, &request); err != nil {
			return 0, nil, invalid("project init body must be empty", err)
		}
		return http.StatusOK, map[string]any{"project_root": service.projectRoot, "state": service.store.Info()}, nil
	case "goal.create":
		var request createGoalRequest
		if err := api.DecodeStrict(operation.Body, &request); err != nil || !validID(request.GoalID) || strings.TrimSpace(request.RawGoal) == "" || (request.Mode != "fast" && request.Mode != "standard") {
			return 0, nil, invalid("goal_id, raw_goal and mode fast|standard are required", err)
		}
		goal := domain.Goal{ID: request.GoalID, State: domain.GoalDraft, Version: 1}
		if err := service.store.CreateGoal(ctx, goal, sqlite.EventInput{Type: "GoalCreated", ActorType: "human", Payload: map[string]any{"raw_goal": request.RawGoal, "mode": request.Mode}}); err != nil {
			return 0, nil, mapStoreError(err)
		}
		return http.StatusCreated, goalView(goal), nil
	case "goal.pause", "goal.resume", "goal.cancel":
		var request versionRequest
		if err := api.DecodeStrict(operation.Body, &request); err != nil || request.ExpectedVersion <= 0 {
			return 0, nil, invalid("expected_version is required", err)
		}
		target := domain.GoalWaiting
		if operation.Name == "goal.resume" {
			target = domain.GoalRunning
		} else if operation.Name == "goal.cancel" {
			target = domain.GoalCancelled
		}
		if err := service.store.UpdateGoalState(ctx, operation.ResourceID, request.ExpectedVersion, target, sqlite.EventInput{Type: eventName(operation.Name), ActorType: "human", Payload: map[string]any{"reason": request.Reason}}); err != nil {
			return 0, nil, mapStoreError(err)
		}
		goal, err := service.store.Goal(ctx, operation.ResourceID)
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		return http.StatusOK, goalView(goal), nil
	case "gate.decide":
		var request gateDecisionRequest
		if err := api.DecodeStrict(operation.Body, &request); err != nil || request.ExpectedVersion <= 0 || !request.Decision.Valid() || !validID(request.DecidedBy) || strings.TrimSpace(request.Reason) == "" {
			return 0, nil, invalid("expected_version, decision, decided_by and reason are required", err)
		}
		gate, err := service.store.DecideGate(ctx, operation.ResourceID, request.ExpectedVersion, request.Decision, request.DecidedBy, request.Reason, sqlite.EventInput{Type: "GateDecided", ActorType: "human", ActorID: request.DecidedBy, Payload: map[string]any{"decision": request.Decision, "reason": request.Reason}})
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		return http.StatusOK, gate, nil
	case "work.retry":
		var request versionRequest
		if err := api.DecodeStrict(operation.Body, &request); err != nil || request.ExpectedVersion <= 0 {
			return 0, nil, invalid("expected_version is required", err)
		}
		if err := service.store.UpdateWorkState(ctx, operation.ResourceID, request.ExpectedVersion, domain.WorkReady, sqlite.EventInput{Type: "WorkRetryReady", ActorType: "human", Payload: map[string]any{"reason": request.Reason}}); err != nil {
			return 0, nil, mapStoreError(err)
		}
		work, err := service.store.WorkItem(ctx, operation.ResourceID)
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		return http.StatusOK, work, nil
	case "goal.replan":
		return service.replan(ctx, operation)
	case "project.clean":
		var request cleanRequest
		if err := api.DecodeStrict(operation.Body, &request); err != nil {
			return 0, nil, invalid("invalid clean request", err)
		}
		return http.StatusOK, map[string]any{"project_id": operation.ResourceID, "dry_run": request.DryRun, "removed": []string{}}, nil
	default:
		return 0, nil, &api.APIError{Status: http.StatusNotImplemented, Code: "OPERATION_UNAVAILABLE", Message: "operation is not implemented"}
	}
}

func (service *Service) Query(ctx context.Context, operation api.Operation) (int, any, error) {
	switch operation.Name {
	case "doctor":
		return http.StatusOK, service.Doctor(ctx), nil
	case "goal.get":
		status, err := service.store.GoalStatus(ctx, operation.ResourceID)
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		response := map[string]any{
			"goal_id": status.Goal.ID, "state": status.Goal.State, "active_revision_id": status.Goal.ActiveRevisionID,
			"final_tree": status.Goal.FinalTree, "final_evidence_set_id": status.Goal.FinalEvidenceSetID,
			"final_report_hash": status.Goal.FinalReportHash, "version": status.Goal.Version,
			"work_items": status.WorkItems, "attempts": status.Attempts, "leases": status.Leases,
			"workspaces": status.Workspaces, "gates": status.Gates, "budgets": status.Budgets,
			"failures": status.Failures, "latest_material_progress_hash": status.LatestProgressHash,
			"authority": status.Authority,
		}
		return http.StatusOK, response, nil
	case "goal.work-items":
		items, err := service.store.GoalWorkItems(ctx, operation.ResourceID)
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		return http.StatusOK, map[string]any{"work_items": items}, nil
	case "goal.events":
		events, err := service.store.GoalEventsAfter(ctx, operation.ResourceID, operation.Query["after_event_id"], parseLimit(operation.Query["limit"]))
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		return http.StatusOK, map[string]any{"events": events}, nil
	case "goal.gates":
		gates, err := service.store.Gates(ctx, operation.ResourceID, operation.Query["state"] == "open")
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		return http.StatusOK, map[string]any{"gates": gates}, nil
	case "attempt.logs":
		if _, err := service.store.Attempt(ctx, operation.ResourceID); err != nil {
			return 0, nil, mapStoreError(err)
		}
		events, err := service.store.Events(ctx, "attempt", operation.ResourceID)
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		return http.StatusOK, map[string]any{"attempt_id": operation.ResourceID, "events": events}, nil
	case "goal.report":
		goal, err := service.store.Goal(ctx, operation.ResourceID)
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		if goal.FinalReportHash == "" {
			return 0, nil, &api.APIError{Status: http.StatusNotFound, Code: "REPORT_NOT_READY", Message: "final report has not been generated"}
		}
		return http.StatusOK, map[string]any{"goal_id": goal.ID, "report_hash": goal.FinalReportHash, "tree": goal.FinalTree}, nil
	default:
		return 0, nil, &api.APIError{Status: http.StatusNotImplemented, Code: "OPERATION_UNAVAILABLE", Message: "operation is not implemented"}
	}
}

type createGoalRequest struct {
	GoalID  string `json:"goal_id"`
	RawGoal string `json:"raw_goal"`
	Mode    string `json:"mode"`
}

type versionRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason,omitempty"`
}

type gateDecisionRequest struct {
	ExpectedVersion int64               `json:"expected_version"`
	Decision        domain.GateDecision `json:"decision"`
	DecidedBy       string              `json:"decided_by"`
	Reason          string              `json:"reason"`
}

type cleanRequest struct {
	DryRun bool `json:"dry_run"`
}

type replanRequest struct {
	PlanRevisionID      string                  `json:"plan_revision_id"`
	GoalRevisionID      string                  `json:"goal_revision_id"`
	Revision            int64                   `json:"revision"`
	ExpectedGoalVersion int64                   `json:"expected_goal_version"`
	WorkItems           []domain.WorkItem       `json:"work_items"`
	Dependencies        []domain.WorkDependency `json:"dependencies"`
	Reason              string                  `json:"reason"`
}

func (service *Service) replan(ctx context.Context, operation api.Operation) (int, any, error) {
	var request replanRequest
	if err := api.DecodeStrict(operation.Body, &request); err != nil || !validID(request.PlanRevisionID) || !validID(request.GoalRevisionID) || request.Revision <= 0 || request.ExpectedGoalVersion <= 0 || strings.TrimSpace(request.Reason) == "" {
		return 0, nil, invalid("complete replacement plan and reason are required", err)
	}
	revision, err := service.store.GoalRevision(ctx, request.GoalRevisionID)
	if err != nil {
		return 0, nil, mapStoreError(err)
	}
	if revision.GoalID != operation.ResourceID {
		return 0, nil, &api.APIError{Status: http.StatusConflict, Code: "SCOPE_MISMATCH", Message: "goal revision belongs to another goal"}
	}
	for index := range request.WorkItems {
		request.WorkItems[index].PlanRevisionID = request.PlanRevisionID
	}
	plan, err := service.store.CreatePlanRevision(ctx, sqlite.PlanRevisionDraft{ID: request.PlanRevisionID, GoalRevisionID: request.GoalRevisionID, Revision: request.Revision, WorkItems: request.WorkItems, Dependencies: request.Dependencies}, sqlite.EventInput{Type: "ReplanProposed", ActorType: "human", Payload: map[string]any{"reason": request.Reason}})
	if err != nil {
		return 0, nil, mapStoreError(err)
	}
	plan, err = service.store.ActivateReplan(ctx, plan.ID, plan.Version, request.ExpectedGoalVersion, sqlite.EventInput{Type: "PlanReplaced", ActorType: "kernel", Payload: map[string]any{"reason": request.Reason}})
	if err != nil {
		return 0, nil, mapStoreError(err)
	}
	if _, err := service.store.RefreshReadyWork(ctx, operation.ResourceID, sqlite.EventInput{Type: "WorkReady", ActorType: "kernel", Payload: map[string]any{"plan_revision_id": plan.ID}}); err != nil {
		return 0, nil, mapStoreError(err)
	}
	return http.StatusOK, plan, nil
}

func (service *Service) Doctor(ctx context.Context) map[string]any {
	tools := make(map[string]any)
	for _, name := range []string{"git", "codex", "claude"} {
		path, err := exec.LookPath(name)
		status := map[string]any{"available": err == nil, "path": path, "probe": "passive"}
		if err == nil {
			probeContext, cancel := context.WithTimeout(ctx, 2*time.Second)
			command := exec.CommandContext(probeContext, path, "--version")
			output, runErr := command.CombinedOutput()
			cancel()
			status["version"] = strings.TrimSpace(string(output))
			status["version_available"] = runErr == nil
		}
		tools[name] = status
	}
	gitFacts := map[string]any{"repository": false}
	gitContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(gitContext, "git", "-C", service.projectRoot, "rev-parse", "--show-toplevel").CombinedOutput(); err == nil {
		gitFacts["repository"] = true
		gitFacts["top_level"] = strings.TrimSpace(string(output))
	}
	return map[string]any{
		"project_root": service.projectRoot, "store": service.store.Info(), "tools": tools,
		"os": runtime.GOOS, "arch": runtime.GOARCH, "git": gitFacts, "validators": []any{},
		"provider_transport": "trusted_profiles_only", "provider_credential_status": "passive_not_inspected",
		"active_probe_evidence": "none", "project_network_policy": "deny", "isolation_level": "L0",
	}
}

func goalView(goal domain.Goal) map[string]any {
	return map[string]any{"goal_id": goal.ID, "state": goal.State, "active_revision_id": goal.ActiveRevisionID, "final_tree": goal.FinalTree, "final_evidence_set_id": goal.FinalEvidenceSetID, "final_report_hash": goal.FinalReportHash, "version": goal.Version}
}

func mapStoreError(err error) error {
	switch {
	case errors.Is(err, basestore.ErrNotFound):
		return &api.APIError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: err.Error()}
	case errors.Is(err, basestore.ErrIdempotencyConflict), errors.Is(err, basestore.ErrConflict), errors.Is(err, basestore.ErrAlreadyExists), errors.Is(err, basestore.ErrActiveLease), errors.Is(err, basestore.ErrStaleLease):
		return &api.APIError{Status: http.StatusConflict, Code: "CONFLICT", Message: err.Error()}
	case errors.Is(err, basestore.ErrExpired):
		return &api.APIError{Status: http.StatusGone, Code: "EXPIRED", Message: err.Error()}
	case errors.Is(err, basestore.ErrAuthorizationDenied):
		return &api.APIError{Status: http.StatusForbidden, Code: "POLICY_DENIED", Message: err.Error()}
	case errors.Is(err, basestore.ErrBudgetExceeded):
		return &api.APIError{Status: http.StatusTooManyRequests, Code: "BUDGET_EXHAUSTED", Message: err.Error()}
	default:
		return fmt.Errorf("state operation failed: %w", err)
	}
}

func invalid(message string, err error) error {
	if err != nil {
		message += ": " + err.Error()
	}
	return &api.APIError{Status: http.StatusBadRequest, Code: "INVALID_REQUEST", Message: message}
}

func validID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "/\\\r\n\x00")
}

func eventName(operation string) string {
	parts := strings.Split(operation, ".")
	if len(parts) != 2 {
		return "GoalChanged"
	}
	return "Goal" + strings.ToUpper(parts[1][:1]) + parts[1][1:]
}

func parseLimit(value string) int {
	var result int
	if _, err := fmt.Sscan(value, &result); err != nil || result <= 0 || result > 1000 {
		return 100
	}
	return result
}
