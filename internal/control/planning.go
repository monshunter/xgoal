package control

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/planner"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/validationplan"
)

// AcceptGoal atomically records the request and its replayable acceptance.
// Provider execution is exclusively owned by the daemon Engine.
func (service *Service) AcceptGoal(ctx context.Context, operation api.Operation, scope, key string, requestModel any) (domain.IdempotencyRecord, bool, error) {
	if operation.Name != "goal.create" {
		return domain.IdempotencyRecord{}, false, invalid("atomic acceptance requires goal.create", nil)
	}
	if existing, err := service.store.LookupIdempotentRequest(ctx, scope, key, requestModel); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, basestore.ErrNotFound) {
		return domain.IdempotencyRecord{}, false, mapStoreError(err)
	}
	if errors.Is(service.configError, config.ErrMigrationRequired) {
		return domain.IdempotencyRecord{}, false, &api.APIError{Status: http.StatusConflict, Code: "CONFIG_MIGRATION_REQUIRED", Message: service.configError.Error()}
	}
	var input createGoalRequest
	if err := api.DecodeStrict(operation.Body, &input); err != nil {
		return domain.IdempotencyRecord{}, false, invalid("goal request must be valid", err)
	}
	request, err := service.planningRequest(input)
	if err != nil {
		return domain.IdempotencyRecord{}, false, err
	}
	record, created, err := service.store.AcceptPlanningGoal(ctx, scope, key, requestModel, request)
	if err != nil {
		return record, created, mapStoreError(err)
	}
	service.wake(request.GoalID)
	return record, created, nil
}

func (service *Service) planningRequest(input createGoalRequest) (planner.Request, error) {
	if input.Mode == "" {
		input.Mode = "standard"
		if service.configuration != nil {
			input.Mode = service.configuration.Orchestration.DefaultMode
		}
	}
	if input.CreatedBy == "" {
		input.CreatedBy = "local-user"
	}
	if !validID(input.GoalID) || strings.TrimSpace(input.RawGoal) == "" || (input.Mode != "fast" && input.Mode != "standard") || !validID(input.CreatedBy) {
		return planner.Request{}, invalid("goal_id, raw_goal, mode fast|standard and a safe created_by are required", nil)
	}
	request := planner.Request{ProtocolVersion: planner.RequestVersion, GoalID: input.GoalID, RawGoal: input.RawGoal, Mode: input.Mode, CreatedBy: input.CreatedBy, ConfigHash: service.configHash, Generation: 1}
	explicitFiles := append([]string{}, input.AcceptanceFiles...)
	request.ExplicitAcceptanceFiles = &explicitFiles
	if input.Proposal != nil {
		request.Proposal = &planner.Proposal{ProtocolVersion: planner.ProposalVersion, Contract: input.Proposal.Contract, Plan: input.Proposal.Plan, Ambiguities: []string{}}
	}
	for id := range service.validators {
		request.TrustedValidatorIDs = append(request.TrustedValidatorIDs, id)
	}
	sort.Strings(request.TrustedValidatorIDs)
	if service.configuration == nil || service.configError != nil {
		request.BlockedReason = "a valid xgoal.yaml is required; configure the project, restart the daemon, then use goal plan with the current version and a reason"
		return request, nil
	}
	if profile, _, err := service.configuration.SelectProfile("planner", ""); err == nil {
		request.ProfileID = profile.ID
	}
	request.ValidationCapabilities = service.configuration.ValidationCapabilities()
	files := append([]string(nil), input.AcceptanceFiles...)
	if service.configuration.Planning != nil {
		files = append(files, service.configuration.Planning.AcceptanceFiles...)
	}
	var err error
	request.AcceptanceFiles, err = validationplan.Paths(files)
	if err != nil {
		return planner.Request{}, invalid("invalid acceptance files", err)
	}
	request.GeneratedValidationPolicy = service.configuration.GeneratedValidationPolicy()

	if request.ProfileID == "" {
		if request.Proposal != nil {
			request.ProfileID = "kernel"
		} else {
			request.BlockedReason = "no trusted Agent Profile supports Planner; configure a profile, restart the daemon, then use goal plan"
		}
	}
	if err := service.checkCurrentConfiguration(); err != nil {
		request.BlockedReason = err.Error()
	}
	return request, nil
}

func (service *Service) checkCurrentConfiguration() error {
	current, err := config.LoadFile(filepath.Join(service.projectRoot, "xgoal.yaml"))
	if err == nil {
		var hash string
		hash, err = current.Hash()
		if err == nil && hash == service.configHash && service.configuration != nil {
			return nil
		}
	}
	return &api.APIError{Status: http.StatusConflict, Code: "CONFIGURATION_CHANGED", Message: sqlite.ErrConfigurationChanged.Error()}
}

func (service *Service) retryPlanning(ctx context.Context, operation api.Operation) (int, any, error) {
	var input struct {
		ExpectedVersion int64         `json:"expected_version"`
		Reason          string        `json:"reason"`
		Proposal        *goalProposal `json:"proposal,omitempty"`
	}
	if err := api.DecodeStrict(operation.Body, &input); err != nil || input.ExpectedVersion <= 0 || strings.TrimSpace(input.Reason) == "" {
		return 0, nil, invalid("expected_version, reason and an optional bounded proposal are required", err)
	}
	if err := service.checkCurrentConfiguration(); err != nil {
		return 0, nil, err
	}
	previous, err := service.store.Planning(ctx, operation.ResourceID)
	if err != nil {
		return 0, nil, mapStoreError(err)
	}
	explicitFiles := previous.Request.AcceptanceFiles
	if previous.Request.ExplicitAcceptanceFiles != nil {
		explicitFiles = *previous.Request.ExplicitAcceptanceFiles
	}
	request, err := service.planningRequest(createGoalRequest{AcceptanceFiles: explicitFiles, GoalID: operation.ResourceID, RawGoal: previous.Request.RawGoal, Mode: previous.Request.Mode, CreatedBy: previous.Request.CreatedBy, Proposal: input.Proposal})
	if err != nil {
		return 0, nil, err
	}
	if request.BlockedReason != "" {
		return 0, nil, &api.APIError{Status: http.StatusConflict, Code: "PLANNING_WAITING", Message: request.BlockedReason}
	}
	record, err := service.store.RetryPlanning(ctx, operation.ResourceID, input.ExpectedVersion, request, input.Reason)
	if err != nil {
		return 0, nil, mapStoreError(err)
	}
	service.cancel(record.Goal.ID)
	service.wake(record.Goal.ID)
	return http.StatusOK, planningView(record), nil
}

func planningFields(record sqlite.PlanningRecord) map[string]any {
	policy := record.Request.GeneratedValidationPolicy
	if policy == "" {
		policy = "allow"
	}
	acceptance := map[string]any{"policy": policy, "input_files": record.Request.AcceptanceFiles, "state": "preparing"}
	if record.Observation != nil && record.Observation.Proposal != nil {
		proposal := record.Observation.Proposal
		acceptance["criteria"] = len(proposal.Contract.AcceptanceCriteria)
		acceptance["generated_validators"] = len(proposal.Contract.GeneratedValidators)
		acceptance["state"] = "prepared"
	}
	if record.Goal.ActiveRevisionID != "" {
		acceptance["state"] = "frozen"
	}
	return map[string]any{"planning_state": record.State, "planning_generation": record.Generation, "planning_effect_id": record.Effect.ID, "planning_blocker": record.Blocker, "acceptance_preparation": acceptance}
}
func planningView(record sqlite.PlanningRecord) map[string]any {
	response := goalView(record.Goal)
	for key, value := range planningFields(record) {
		response[key] = value
	}
	return response
}
