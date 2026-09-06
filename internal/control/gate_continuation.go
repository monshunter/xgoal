package control

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/gitrepo"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/workspace"
)

func (service *Service) resumeGate(ctx context.Context, op api.Operation) (int, any, error) {
	var input struct {
		GateVersion  int64 `json:"expected_gate_version"`
		OwnerVersion int64 `json:"expected_owner_version"`
	}
	if err := api.DecodeStrict(op.Body, &input); err != nil || input.GateVersion <= 0 || input.OwnerVersion <= 0 {
		return 0, nil, invalid("expected_gate_version and expected_owner_version are required", err)
	}
	if err := service.checkCurrentConfiguration(); err != nil {
		return 0, nil, err
	}
	gate, err := service.store.Gate(ctx, op.ResourceID)
	if err != nil {
		return 0, nil, mapStoreError(err)
	}
	if gate.Version != input.GateVersion {
		return 0, nil, mapStoreError(basestore.ErrConflict)
	}
	if gate.State != domain.GateApproved || gate.Decision != domain.GateAllow || gate.Used != 0 || gate.Action != domain.ActionExecCommand {
		return 0, nil, mapStoreError(basestore.ErrAuthorizationDenied)
	}
	model, err := service.store.GoalExecutionModel(ctx, gate.GoalID)
	if err != nil {
		return 0, nil, mapStoreError(err)
	}
	if model != workspace.ExecutionCurrentDirectory {
		return 0, nil, checkoutAPIError(sqlite.ErrExecutionMigrationRequired)
	}
	var facts struct {
		Owner string `json:"owner"`
	}
	if err := json.Unmarshal(gate.FactsJSON, &facts); err != nil {
		return 0, nil, mapStoreError(err)
	}
	c := sqlite.GateContinuation{GateID: gate.ID, GateVersion: input.GateVersion, OwnerVersion: input.OwnerVersion}
	owner, ownerID := facts.Owner, gate.GoalID
	var result any
	switch {
	case gate.WorkItemID != "" && (gate.ReasonCode == "agent_blocked" || gate.ReasonCode == "checkout_retry_required"):
		owner, ownerID = "work", gate.WorkItemID
		result, err = service.retryCheckoutWorkGate(ctx, gate.WorkItemID, versionRequest{ExpectedVersion: c.OwnerVersion, Reason: "resume approved Gate"}, &c)
	case facts.Owner == "planning" && gate.WorkItemID == "":
		previous, readErr := service.store.Planning(ctx, gate.GoalID)
		if readErr != nil {
			return 0, nil, mapStoreError(readErr)
		}
		if previous.Observation == nil || previous.Observation.InputTree == "" {
			return 0, nil, &api.APIError{Status: http.StatusConflict, Code: "PLANNING_WAITING", Message: "no previous planning scene is available; inspect and use goal plan explicitly"}
		}
		repo, readErr := gitrepo.Open(ctx, service.projectRoot)
		if readErr != nil {
			return 0, nil, checkoutAPIError(readErr)
		}
		o := previous.Observation
		if readErr = repo.CheckSnapshot(ctx, gitrepo.SnapshotSpec{BaseTree: o.InputTree, ExcludePaths: []string{service.store.Info().ProjectDir}}, o.CheckoutIdentity, o.InputTree); readErr != nil {
			return 0, nil, checkoutAPIError(readErr)
		}
		var planning sqlite.PlanningRecord
		planning, err = service.store.ResumePlanningGate(ctx, gate.GoalID, c, service.configHash, o.CheckoutIdentity, o.InputTree)
		result = planningView(planning)
	case facts.Owner == "final" && gate.WorkItemID == "":
		checkout, readErr := service.store.Checkout(ctx)
		if readErr != nil {
			return 0, nil, checkoutAPIError(readErr)
		}
		repo, readErr := gitrepo.Open(ctx, service.projectRoot)
		if readErr != nil {
			return 0, nil, checkoutAPIError(readErr)
		}
		ref, readErr := repo.ResolveRef(ctx, "refs/xgoal/goals/"+gate.GoalID+"/integration")
		if readErr != nil {
			return 0, nil, checkoutAPIError(readErr)
		}
		if ref.Commit != checkout.AcceptedCommit || ref.Tree != checkout.AcceptedTree {
			return 0, nil, checkoutAPIError(sqlite.ErrCheckoutConflict)
		}
		if readErr = repo.CheckSnapshot(ctx, gitrepo.SnapshotSpec{BaseTree: checkout.AcceptedTree, ExcludePaths: []string{service.store.Info().ProjectDir}}, checkout.Identity, checkout.AcceptedTree); readErr != nil {
			return 0, nil, checkoutAPIError(readErr)
		}
		var goal domain.Goal
		goal, err = service.store.ResumeAcceptanceGate(ctx, gate.GoalID, c, service.configHash, checkout.Identity, checkout.AcceptedTree)
		result = goalView(goal)
	default:
		return 0, nil, &api.APIError{Status: http.StatusConflict, Code: "GATE_CONTINUATION_UNSUPPORTED", Message: "this Gate has no continuation owner; inspect its action and scope and use its dedicated operation"}
	}
	if err != nil {
		return 0, nil, mapStoreError(err)
	}
	service.wake(gate.GoalID)
	return http.StatusOK, map[string]any{"gate_id": gate.ID, "owner_kind": owner, "owner_id": ownerID, "result": result}, nil
}
