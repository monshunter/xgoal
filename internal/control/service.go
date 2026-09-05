package control

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
	claudeadapter "github.com/monshunter/xgoal/internal/adapter/claude"
	codexadapter "github.com/monshunter/xgoal/internal/adapter/codex"
	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/evidence"
	"github.com/monshunter/xgoal/internal/finalize"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/goalcompile"
	"github.com/monshunter/xgoal/internal/planner"
	"github.com/monshunter/xgoal/internal/project"
	"github.com/monshunter/xgoal/internal/protocol"
	finalreport "github.com/monshunter/xgoal/internal/report"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/workspace"
)

type Service struct {
	store         *sqlite.Store
	projectRoot   string
	finalizer     *finalize.Manager
	configHash    string
	validators    map[string]bool
	configuration *config.Config
	lifecycle     Lifecycle
}

// Lifecycle is the daemon-owned execution signal surface. Control operations
// persist state first; signals only ask the engine to re-read authoritative
// Store state or cancel an in-flight provider process.
type Lifecycle interface {
	Wake(goalID string)
	CancelGoal(goalID string)
	CancelWork(workID string)
}

func New(store *sqlite.Store, projectRoot string) (*Service, error) {
	if store == nil || !filepath.IsAbs(projectRoot) || filepath.Clean(projectRoot) != projectRoot {
		return nil, errors.New("control service requires a store and clean absolute project root")
	}
	files, err := finalreport.NewFileManager(store.Info().ProjectDir)
	if err != nil {
		return nil, err
	}
	finalizer, err := finalize.New(store, files)
	if err != nil {
		return nil, err
	}
	validators := make(map[string]bool)
	configHash := ""
	var loadedConfiguration *config.Config
	if cfg, loadErr := config.LoadFile(filepath.Join(projectRoot, "xgoal.yaml")); loadErr == nil {
		configHash, err = cfg.Hash()
		if err != nil {
			return nil, err
		}
		for _, validator := range cfg.Validators {
			validators[validator.ID] = true
		}
		loadedConfiguration = &cfg
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return nil, fmt.Errorf("load xgoal.yaml: %w", loadErr)
	}
	return &Service{store: store, projectRoot: projectRoot, finalizer: finalizer, configHash: configHash, validators: validators, configuration: loadedConfiguration}, nil
}

func (service *Service) SetLifecycle(lifecycle Lifecycle) { service.lifecycle = lifecycle }

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
		if err := api.DecodeStrict(operation.Body, &request); err != nil {
			return 0, nil, invalid("goal request must be valid", err)
		}
		if request.Mode == "" {
			request.Mode = "standard"
			if service.configuration != nil {
				request.Mode = service.configuration.Orchestration.DefaultMode
			}
		}
		if !validID(request.GoalID) || strings.TrimSpace(request.RawGoal) == "" || (request.Mode != "fast" && request.Mode != "standard") {
			return 0, nil, invalid("goal_id, raw_goal and mode fast|standard are required", nil)
		}
		if request.CreatedBy == "" {
			request.CreatedBy = "local-user"
		}
		if !validID(request.CreatedBy) {
			return 0, nil, invalid("created_by must be a safe actor id", nil)
		}
		var compiled goalcompile.Compiled
		compiledReady := false
		if service.configHash != "" && service.configuration != nil && request.Proposal != nil {
			var compileErr error
			compiled, compileErr = goalcompile.Compile(request.GoalID, request.GoalID+"_revision_1", request.GoalID+"_plan_1", request.Proposal.Contract, request.Proposal.Plan, service.validators)
			if compileErr != nil {
				return 0, nil, invalid("Planner proposal failed deterministic validation", compileErr)
			}
			compiledReady = true
		}
		goal := domain.Goal{ID: request.GoalID, State: domain.GoalDraft, Version: 1}
		if err := service.store.CreateGoal(ctx, goal, sqlite.EventInput{Type: "GoalCreated", ActorType: "human", Payload: map[string]any{"raw_goal": request.RawGoal, "mode": request.Mode}}); err != nil {
			return 0, nil, mapStoreError(err)
		}
		if service.configHash == "" || service.configuration == nil {
			response := goalView(goal)
			response["planner_proposal_required"] = true
			response["reason"] = "a valid xgoal.yaml is required"
			return http.StatusCreated, response, nil
		}
		proposal := request.Proposal
		plannerActor := request.CreatedBy
		if request.Proposal == nil {
			planned, profileID, planErr := service.planGoal(ctx, request)
			plannerActor = profileID
			if planErr != nil {
				if gateErr := service.openPlannerGate(ctx, request.GoalID, profileID, planErr); gateErr != nil {
					return 0, nil, mapStoreError(gateErr)
				}
				response := goalView(goal)
				response["planner_gate_required"] = true
				response["reason"] = planErr.Error()
				return http.StatusCreated, response, nil
			}
			proposal = &goalProposal{Contract: planned.Contract, Plan: planned.Plan}
		}
		if !compiledReady {
			var compileErr error
			compiled, compileErr = goalcompile.Compile(request.GoalID, request.GoalID+"_revision_1", request.GoalID+"_plan_1", proposal.Contract, proposal.Plan, service.validators)
			if compileErr != nil {
				if gateErr := service.openPlannerGate(ctx, request.GoalID, plannerActor, compileErr); gateErr != nil {
					return 0, nil, mapStoreError(gateErr)
				}
				response := goalView(goal)
				response["planner_gate_required"] = true
				response["reason"] = compileErr.Error()
				return http.StatusCreated, response, nil
			}
		}
		revision, err := service.store.FreezeGoalRevision(ctx, sqlite.GoalRevisionDraft{
			ID: request.GoalID + "_revision_1", GoalID: request.GoalID, Revision: 1, RawGoal: request.RawGoal,
			Contract: map[string]any{"protocol_version": goalcompile.ContractVersion, "contract": compiled.Contract, "config_hash": service.configHash, "created_by": request.CreatedBy, "mode": request.Mode},
		}, 1, sqlite.EventInput{Type: "GoalRevisionFrozen", ActorType: "planner", ActorID: plannerActor, Payload: map[string]any{"proposal_hash": compiled.ContractHash}})
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		plan, err := service.store.CreatePlanRevision(ctx, sqlite.PlanRevisionDraft{
			ID: request.GoalID + "_plan_1", GoalRevisionID: revision.ID, Revision: 1,
			WorkItems: compiled.WorkItems, Dependencies: compiled.Dependencies,
		}, sqlite.EventInput{Type: "PlanRevisionCreated", ActorType: "planner", ActorID: plannerActor, Payload: map[string]any{"proposal_hash": compiled.PlanHash}})
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		if _, err := service.store.ActivatePlanRevision(ctx, plan.ID, plan.Version, 2, sqlite.EventInput{Type: "PlanActivated", ActorType: "kernel", Payload: map[string]any{"mode": request.Mode}}); err != nil {
			return 0, nil, mapStoreError(err)
		}
		if _, err := service.store.RefreshReadyWork(ctx, request.GoalID, sqlite.EventInput{Type: "WorkReady", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			return 0, nil, mapStoreError(err)
		}
		goal, err = service.store.Goal(ctx, request.GoalID)
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		service.wake(goal.ID)
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
		if target == domain.GoalRunning {
			service.wake(goal.ID)
		} else {
			service.cancel(goal.ID)
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
		if goalID, resolveErr := service.store.WorkGoalID(ctx, work.ID); resolveErr == nil {
			service.wake(goalID)
		}
		return http.StatusOK, work, nil
	case "work.cancel":
		var request versionRequest
		if err := api.DecodeStrict(operation.Body, &request); err != nil || request.ExpectedVersion <= 0 {
			return 0, nil, invalid("expected_version is required", err)
		}
		work, err := service.store.CancelWork(ctx, operation.ResourceID, request.ExpectedVersion, sqlite.EventInput{Type: "WorkCancelled", ActorType: "human", Payload: map[string]any{"reason": request.Reason}})
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		service.cancelWork(work.ID)
		return http.StatusOK, work, nil
	case "goal.replan":
		return service.replan(ctx, operation)
	case "goal.finalize":
		var request finalizeRequest
		if err := api.DecodeStrict(operation.Body, &request); err != nil || request.ExpectedVersion <= 0 {
			return 0, nil, invalid("expected_version, completion facts, and report are required", err)
		}
		result, record, err := service.finalizer.Finalize(ctx, operation.ResourceID, request.ExpectedVersion, request.Facts, request.Report, sqlite.EventInput{Type: "GoalCompleted", ActorType: "kernel", Payload: map[string]any{"protocol_version": finalreport.ProtocolVersion}})
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		if !result.Complete {
			return http.StatusConflict, map[string]any{"goal_id": operation.ResourceID, "completed": false, "reasons": result.Reasons}, nil
		}
		return http.StatusOK, map[string]any{"goal_id": operation.ResourceID, "completed": true, "report_hash": record.Files.ReportHash, "report_state": record.State, "report_version": record.Version}, nil
	case "doctor.active-probe":
		return service.activeProbe(ctx, operation)
	case "project.clean":
		var request cleanRequest
		if err := api.DecodeStrict(operation.Body, &request); err != nil {
			return 0, nil, invalid("invalid clean request", err)
		}
		candidates, err := service.store.CleanableWorkspaces(ctx)
		if err != nil {
			return 0, nil, mapStoreError(err)
		}
		candidateIDs := make([]string, len(candidates))
		for index, candidate := range candidates {
			candidateIDs[index] = candidate.Snapshot.ID
		}
		if request.DryRun || len(candidates) == 0 {
			return http.StatusOK, map[string]any{"project_id": operation.ResourceID, "dry_run": request.DryRun, "candidates": candidateIDs, "removed": []string{}, "protection": "active, non-cancelled, Evidence/Environment/Validator, and Final Report references retained"}, nil
		}
		repository, err := gitrepo.Open(ctx, service.projectRoot)
		if err != nil {
			return 0, nil, err
		}
		manager, err := workspace.NewManager(service.store.Info().ProjectDir, repository)
		if err != nil {
			return 0, nil, err
		}
		removed := []string{}
		for _, candidate := range candidates {
			if err := manager.Cleanup(ctx, candidate.Snapshot.ID); err != nil {
				return 0, nil, fmt.Errorf("clean workspace %q: %w", candidate.Snapshot.ID, err)
			}
			if _, err := service.store.TransitionWorkspaceArtifact(ctx, candidate.Snapshot.ID, candidate.Version, sqlite.WorkspaceArtifactCleaned); err != nil {
				return 0, nil, fmt.Errorf("record cleaned workspace %q: %w", candidate.Snapshot.ID, err)
			}
			removed = append(removed, candidate.Snapshot.ID)
		}
		return http.StatusOK, map[string]any{"project_id": operation.ResourceID, "dry_run": false, "candidates": candidateIDs, "removed": removed}, nil
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
			"goal_revision": status.GoalRevision,
			"final_tree":    status.Goal.FinalTree, "final_evidence_set_id": status.Goal.FinalEvidenceSetID,
			"final_report_hash": status.Goal.FinalReportHash, "version": status.Goal.Version,
			"work_items": status.WorkItems, "attempts": status.Attempts, "leases": status.Leases,
			"workspaces": status.Workspaces, "gates": status.Gates,
			"failures": status.Failures, "findings": status.Findings, "latest_tree": status.LatestTree,
			"validation_summary": status.Validation, "latest_material_progress_hash": status.LatestProgressHash,
			"execution_boundary": service.executionBoundary(),
			"authority":          status.Authority,
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
		record, jsonBytes, markdown, err := service.finalizer.Read(ctx, operation.ResourceID)
		if err != nil {
			if errors.Is(err, basestore.ErrNotFound) {
				return 0, nil, &api.APIError{Status: http.StatusNotFound, Code: "REPORT_NOT_READY", Message: "final report has not been generated"}
			}
			return 0, nil, &api.APIError{Status: http.StatusServiceUnavailable, Code: "REPORT_UNAVAILABLE", Message: err.Error()}
		}
		return http.StatusOK, map[string]any{
			"goal_id": operation.ResourceID, "report_hash": record.Files.ReportHash, "tree": record.TreeHash,
			"json_hash": record.Files.JSONHash, "markdown_hash": record.Files.MarkdownHash,
			"json": json.RawMessage(jsonBytes), "markdown": string(markdown), "authority": domain.AuthorityFact,
		}, nil
	default:
		return 0, nil, &api.APIError{Status: http.StatusNotImplemented, Code: "OPERATION_UNAVAILABLE", Message: "operation is not implemented"}
	}
}

type createGoalRequest struct {
	GoalID    string        `json:"goal_id"`
	RawGoal   string        `json:"raw_goal"`
	Mode      string        `json:"mode"`
	CreatedBy string        `json:"created_by,omitempty"`
	Proposal  *goalProposal `json:"proposal,omitempty"`
}

type goalProposal struct {
	Contract goalcompile.Contract `json:"contract"`
	Plan     goalcompile.Plan     `json:"plan"`
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

type finalizeRequest struct {
	ExpectedVersion int64                  `json:"expected_version"`
	Facts           sqlite.CompletionFacts `json:"facts"`
	Report          finalreport.Report     `json:"report"`
}

type activeProbeRequest struct {
	ProfileID            string `json:"profile_id"`
	AcknowledgeTransport bool   `json:"acknowledge_provider_transport"`
	TimeoutMilliseconds  int64  `json:"timeout_milliseconds"`
}

type replanRequest struct {
	PlanRevisionID      string                  `json:"plan_revision_id"`
	GoalRevisionID      string                  `json:"goal_revision_id"`
	Revision            int64                   `json:"revision"`
	ExpectedGoalVersion int64                   `json:"expected_goal_version"`
	WorkItems           []domain.WorkItem       `json:"work_items"`
	Dependencies        []domain.WorkDependency `json:"dependencies"`
	Reason              string                  `json:"reason"`
	ImpactAnalysis      string                  `json:"impact_analysis"`
}

func (service *Service) replan(ctx context.Context, operation api.Operation) (int, any, error) {
	var request replanRequest
	if err := api.DecodeStrict(operation.Body, &request); err != nil || !validID(request.PlanRevisionID) || !validID(request.GoalRevisionID) || request.Revision <= 0 || request.ExpectedGoalVersion <= 0 || strings.TrimSpace(request.Reason) == "" || strings.TrimSpace(request.ImpactAnalysis) == "" {
		return 0, nil, invalid("complete replacement plan, reason, and impact_analysis are required", err)
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
	plan, err := service.store.CreatePlanRevision(ctx, sqlite.PlanRevisionDraft{ID: request.PlanRevisionID, GoalRevisionID: request.GoalRevisionID, Revision: request.Revision, WorkItems: request.WorkItems, Dependencies: request.Dependencies}, sqlite.EventInput{Type: "ReplanProposed", ActorType: "human", Payload: map[string]any{"reason": request.Reason, "impact_analysis": request.ImpactAnalysis}})
	if err != nil {
		return 0, nil, mapStoreError(err)
	}
	plan, err = service.store.ActivateReplan(ctx, plan.ID, plan.Version, request.ExpectedGoalVersion, sqlite.EventInput{Type: "PlanReplaced", ActorType: "kernel", Payload: map[string]any{"reason": request.Reason, "impact_analysis": request.ImpactAnalysis}})
	if err != nil {
		return 0, nil, mapStoreError(err)
	}
	if _, err := service.store.RefreshReadyWork(ctx, operation.ResourceID, sqlite.EventInput{Type: "WorkReady", ActorType: "kernel", Payload: map[string]any{"plan_revision_id": plan.ID}}); err != nil {
		return 0, nil, mapStoreError(err)
	}
	service.wake(operation.ResourceID)
	return http.StatusOK, plan, nil
}

func (service *Service) planGoal(ctx context.Context, request createGoalRequest) (planner.Proposal, string, error) {
	var profile *config.Agent
	for index := range service.configuration.Agents {
		for _, role := range service.configuration.Agents[index].Roles {
			if role == string(domain.RolePlanner) {
				profile = &service.configuration.Agents[index]
				break
			}
		}
		if profile != nil {
			break
		}
	}
	if profile == nil {
		return planner.Proposal{}, "kernel", errors.New("no trusted Agent Profile supports the Planner role")
	}
	environment := make(map[string]string)
	for _, name := range profile.EnvironmentAllowlist {
		if value, exists := os.LookupEnv(name); exists {
			environment[name] = value
		}
	}
	runtimeAdapter, err := service.profileAdapter(*profile, environment)
	if err != nil {
		return planner.Proposal{}, profile.ID, err
	}
	plannerAdapter, ok := runtimeAdapter.(planner.Adapter)
	if !ok {
		return planner.Proposal{}, profile.ID, errors.New("selected Agent adapter has no Planner contract")
	}
	probeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = runtimeAdapter.Probe(probeContext, adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: profile.ID, Timeout: 10 * time.Second})
	cancel()
	if err != nil {
		return planner.Proposal{}, profile.ID, err
	}
	validators := make([]string, 0, len(service.validators))
	for id := range service.validators {
		validators = append(validators, id)
	}
	sort.Strings(validators)
	packetPath, packetHash, err := planner.Prepare(service.store.Info().ProjectDir, planner.Packet{
		ProtocolVersion: planner.PacketVersion, GoalID: request.GoalID, RawGoal: request.RawGoal, Mode: request.Mode,
		ConfigHash: service.configHash, TrustedValidators: validators, ProjectRoot: service.projectRoot,
		ProjectNetwork: service.configuration.Runtime.ProjectNetwork, ProjectSecrets: service.configuration.Runtime.ProjectSecrets,
	})
	if err != nil {
		return planner.Proposal{}, profile.ID, err
	}
	schema, err := planner.Schema()
	if err != nil {
		return planner.Proposal{}, profile.ID, err
	}
	invocationID, err := randomComponent("planner")
	if err != nil {
		return planner.Proposal{}, profile.ID, err
	}
	execution, err := plannerAdapter.Plan(ctx, planner.Invocation{
		InvocationID: invocationID, ProfileID: profile.ID, WorkDir: service.projectRoot,
		PacketPath: packetPath, PacketHash: packetHash,
		Prompt:       "Act as the read-only xgoal Planner. Read the immutable Planner Packet at " + packetPath + " and inspect the repository only as needed. Return one bounded Goal Contract and acyclic Work Graph using only trusted validator IDs. Every Work Item must use recommended_role implementer and explicit read/write scopes. Do not modify files. If an important product meaning or authorization cannot be safely inferred, list it in ambiguities instead of guessing.",
		OutputSchema: schema, Environment: environment, Timeout: profile.Timeout.Duration, MaxOutputBytes: 8 << 20,
	}, nil)
	if err != nil {
		return planner.Proposal{}, profile.ID, err
	}
	if len(execution.Proposal.Ambiguities) != 0 {
		return planner.Proposal{}, profile.ID, fmt.Errorf("Planner reported unresolved ambiguities: %s", strings.Join(execution.Proposal.Ambiguities, "; "))
	}
	return execution.Proposal, profile.ID, nil
}

func (service *Service) openPlannerGate(ctx context.Context, goalID, profileID string, cause error) error {
	gateID, err := randomComponent("gate_planner")
	if err != nil {
		return err
	}
	_, err = service.store.CreateGate(ctx, sqlite.GateDraft{
		ID: gateID, GoalID: goalID, ReasonCode: "planner_clarification",
		Facts:          map[string]any{"profile_id": profileID, "error": cause.Error()},
		Unknowns:       []string{"a bounded and deterministically valid Goal Contract and Work Graph"},
		Options:        []string{"provide a corrected proposal", "clarify the raw goal", "cancel the goal"},
		Recommendation: "clarify the goal or provide a proposal that passes deterministic compilation",
		Action:         domain.ActionExpandScope, Scope: []string{"goal/" + goalID + "/contract"},
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour), MaxUses: 1, Revocable: true, Required: true,
	}, sqlite.EventInput{Type: "GateOpened", ActorType: "kernel", Payload: map[string]any{"reason": "planner_clarification"}})
	return err
}

func (service *Service) wake(goalID string) {
	if service.lifecycle != nil {
		service.lifecycle.Wake(goalID)
	}
}

func (service *Service) cancel(goalID string) {
	if service.lifecycle != nil {
		service.lifecycle.CancelGoal(goalID)
	}
}

func (service *Service) cancelWork(workID string) {
	if service.lifecycle != nil {
		service.lifecycle.CancelWork(workID)
	}
}

func (service *Service) Doctor(ctx context.Context) map[string]any {
	tools := make(map[string]any)
	for _, name := range []string{"git", "codex", "claude"} {
		path, err := exec.LookPath(name)
		status := map[string]any{"available": err == nil, "path": path, "probe": "passive"}
		if err == nil {
			output, runErr := passiveCommandOutput(ctx, path, "--version")
			status["version"] = strings.TrimSpace(string(output))
			status["version_available"] = runErr == nil
		}
		tools[name] = status
	}
	gitFacts := map[string]any{"repository": false}
	gitContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if output, err := passiveCommandOutput(gitContext, "git", "-C", service.projectRoot, "rev-parse", "--show-toplevel"); err == nil {
		gitFacts["repository"] = true
		gitFacts["top_level"] = strings.TrimSpace(string(output))
		gitFacts["head"] = commandOutput(gitContext, "git", "-C", service.projectRoot, "rev-parse", "HEAD")
		gitFacts["tree"] = commandOutput(gitContext, "git", "-C", service.projectRoot, "rev-parse", "HEAD^{tree}")
		gitFacts["common_dir"] = commandOutput(gitContext, "git", "-C", service.projectRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
		gitFacts["status_porcelain"] = commandOutput(gitContext, "git", "-C", service.projectRoot, "status", "--porcelain=v1")
	}
	profiles := []any{}
	validators := []any{}
	unmet := []string{}
	if service.configuration == nil {
		unmet = append(unmet, "valid xgoal.yaml is unavailable")
	} else {
		for _, profile := range service.configuration.Agents {
			profiles = append(profiles, service.passiveProfile(ctx, profile))
		}
		for _, definition := range service.configuration.Validators {
			available := false
			resolved := ""
			if len(definition.Argv) > 0 {
				resolved, _ = exec.LookPath(definition.Argv[0])
				available = resolved != ""
			}
			cwd := filepath.Join(service.projectRoot, filepath.FromSlash(definition.CWD))
			if definition.CWD == "" {
				cwd = service.projectRoot
			}
			if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
				available = false
			}
			validators = append(validators, map[string]any{"id": definition.ID, "type": definition.Type, "required": definition.Required, "argv": definition.Argv, "resolved_command": resolved, "cwd": cwd, "available": available, "probe": "passive"})
			if definition.Required && !available {
				unmet = append(unmet, "required validator unavailable: "+definition.ID)
			}
		}
	}
	boundary := service.executionBoundary()
	return map[string]any{
		"project_root": service.projectRoot, "store": service.store.Info(), "tools": tools,
		"os": runtime.GOOS, "arch": runtime.GOARCH, "git": gitFacts, "config_hash": service.configHash,
		"agent_profiles": profiles, "validators": validators, "unmet_capabilities": unmet,
		"provider_transport": boundary["provider_transport"], "provider_credential_status": "passive_not_inspected",
		"active_probe_evidence": "none", "project_network_policy": boundary["project_network_policy"], "project_secrets_policy": boundary["project_secrets_policy"], "isolation_level": boundary["isolation_level"],
		"isolation_limit": "local-process L0 cannot strongly isolate the user home or network", "model_calls": 0,
	}
}

func (service *Service) executionBoundary() map[string]any {
	projectNetwork := "deny"
	projectSecrets := "deny"
	if service.configuration != nil {
		projectNetwork = service.configuration.Runtime.ProjectNetwork
		projectSecrets = service.configuration.Runtime.ProjectSecrets
	}
	return map[string]any{
		"isolation_level": "L0", "provider_transport": "trusted_profiles_only",
		"project_network_policy": projectNetwork, "project_secrets_policy": projectSecrets,
		"credential_isolation": "L0", "authority": domain.AuthorityFact,
	}
}

func (service *Service) passiveProfile(ctx context.Context, profile config.Agent) map[string]any {
	result := map[string]any{"id": profile.ID, "adapter": profile.Adapter, "roles": profile.Roles, "provider_transport": profile.ProviderTransport, "credential_source": profile.CredentialSource, "project_network": service.configuration.Runtime.ProjectNetwork, "isolation_level": "L0", "probe": adapter.ProbePassive}
	environment := make(map[string]string)
	for _, name := range profile.EnvironmentAllowlist {
		if value, exists := os.LookupEnv(name); exists {
			environment[name] = value
		}
	}
	spec := adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: profile.ID, Timeout: 10 * time.Second}
	var capabilities adapter.Capabilities
	var err error
	switch profile.Adapter {
	case "codex-cli":
		capabilities, err = codexadapter.PassiveProbe(ctx, codexadapter.Config{Binary: profile.Command, ProjectRoot: service.projectRoot, Environment: environment}, spec)
	case "claude-cli":
		capabilities, err = claudeadapter.PassiveProbe(ctx, claudeadapter.Config{Binary: profile.Command, ProjectRoot: service.projectRoot, Environment: environment}, spec)
	default:
		err = errors.New("passive probe is unavailable for this adapter")
	}
	if err == nil {
		result["available"] = true
		result["capabilities"] = capabilities
		return result
	}

	result["available"] = false
	result["error"] = err.Error()
	return result
}

func (service *Service) activeProbe(ctx context.Context, operation api.Operation) (int, any, error) {
	var request activeProbeRequest
	if err := api.DecodeStrict(operation.Body, &request); err != nil || !validID(request.ProfileID) || !request.AcknowledgeTransport || request.TimeoutMilliseconds <= 0 || request.TimeoutMilliseconds > int64((30*time.Minute)/time.Millisecond) {
		return 0, nil, invalid("active probe requires profile, explicit Provider Transport acknowledgement, and a positive timeout", err)
	}
	if service.configuration == nil {
		return 0, nil, invalid("a valid xgoal.yaml is required", nil)
	}
	var selected *config.Agent
	for index := range service.configuration.Agents {
		if service.configuration.Agents[index].ID == request.ProfileID {
			selected = &service.configuration.Agents[index]
			break
		}
	}
	if selected == nil || selected.ActiveProbe != "explicit" || selected.ProviderTransport != "allow" {
		return 0, nil, &api.APIError{Status: http.StatusForbidden, Code: "ACTIVE_PROBE_DENIED", Message: "profile does not permit an explicit active probe"}
	}
	environment := make(map[string]string)
	for _, name := range selected.EnvironmentAllowlist {
		if value, exists := os.LookupEnv(name); exists {
			environment[name] = value
		}
	}
	runtimeAdapter, err := service.profileAdapter(*selected, environment)
	if err != nil {
		return 0, nil, &api.APIError{Status: http.StatusServiceUnavailable, Code: "AGENT_UNAVAILABLE", Message: err.Error()}
	}
	timeout := time.Duration(request.TimeoutMilliseconds) * time.Millisecond
	capabilities, err := runtimeAdapter.Probe(ctx, adapter.ProbeSpec{Mode: adapter.ProbeActiveContract, ProfileID: selected.ID, ProviderTransport: true, Timeout: timeout})
	if err != nil {
		return 0, nil, &api.APIError{Status: http.StatusServiceUnavailable, Code: "ACTIVE_PROBE_FAILED", Message: err.Error()}
	}
	tree := commandOutput(ctx, "git", "-C", service.projectRoot, "rev-parse", "HEAD^{tree}")
	if len(tree) != 40 && len(tree) != 64 {
		return 0, nil, &api.APIError{Status: http.StatusServiceUnavailable, Code: "GIT_FACT_UNAVAILABLE", Message: "current Git tree is unavailable"}
	}
	definitionHash, err := canonical.Hash("agent-profile", config.APIVersion, *selected)
	if err != nil {
		return 0, nil, err
	}
	environmentHash, err := canonical.Hash("active-probe-environment", protocol.EvidenceVersion, map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "profile_id": selected.ID})
	if err != nil {
		return 0, nil, err
	}
	payloadHash, err := canonical.Hash("active-probe-capabilities", protocol.EvidenceVersion, capabilities)
	if err != nil {
		return 0, nil, err
	}
	evidenceID, err := randomComponent("evidence_probe")
	if err != nil {
		return 0, nil, err
	}
	now := time.Now().UTC()
	record := evidence.Record{Evidence: protocol.Evidence{
		ProtocolVersion: protocol.EvidenceVersion, ID: evidenceID, Kind: "active_agent_contract_probe", SubjectID: selected.ID,
		Producer: "xgoal/doctor", Authority: domain.AuthorityDeterministic, GoalRevisionHash: service.configHash,
		ConfigHash: service.configHash, TreeHash: tree, PayloadHash: payloadHash, State: domain.EvidenceCurrent, CreatedAt: now,
	}, DefinitionHash: definitionHash, EnvironmentHash: environmentHash, ReceiptHash: payloadHash}
	if err := service.store.AppendEvidence(ctx, record); err != nil {
		return 0, nil, mapStoreError(err)
	}
	return http.StatusOK, map[string]any{"profile_id": selected.ID, "capabilities": capabilities, "evidence_id": evidenceID, "payload_hash": payloadHash, "provider_transport": "used", "project_network": service.configuration.Runtime.ProjectNetwork, "timeout_milliseconds": request.TimeoutMilliseconds}, nil
}

func (service *Service) profileAdapter(profile config.Agent, environment map[string]string) (adapter.Adapter, error) {
	switch profile.Adapter {
	case "codex-cli":
		return codexadapter.New(codexadapter.Config{Binary: profile.Command, RuntimeRoot: service.store.Info().ProjectDir, ProjectRoot: service.projectRoot, Environment: environment})
	case "claude-cli":
		return claudeadapter.New(claudeadapter.Config{Binary: profile.Command, RuntimeRoot: service.store.Info().ProjectDir, ProjectRoot: service.projectRoot, Environment: environment})
	default:
		return nil, errors.New("runtime probe is unavailable for this adapter")
	}
}

func randomComponent(prefix string) (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(value), nil
}

func commandOutput(ctx context.Context, name string, arguments ...string) string {
	output, err := passiveCommandOutput(ctx, name, arguments...)
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(output))
}

type passiveBuffer struct{ bytes.Buffer }

func (buffer *passiveBuffer) Write(data []byte) (int, error) {
	n := len(data)
	if remaining := 8192 - buffer.Len(); remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = buffer.Buffer.Write(data)
	}
	return n, nil
}
func passiveCommandOutput(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	probeContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(probeContext, name, arguments...)
	command.Env = append(project.GitEnvironment(), "GIT_OPTIONAL_LOCKS=0")
	command.WaitDelay = 250 * time.Millisecond
	var output passiveBuffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	return output.Bytes(), err
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
	switch operation {
	case "goal.pause":
		return "GoalPaused"
	case "goal.resume":
		return "GoalResumed"
	case "goal.cancel":
		return "GoalCancelled"
	default:
		return "GoalChanged"
	}
}

func parseLimit(value string) int {
	var result int
	if _, err := fmt.Sscan(value, &result); err != nil || result <= 0 || result > 1000 {
		return 100
	}
	return result
}
