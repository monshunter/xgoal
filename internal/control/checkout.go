package control

import (
	"context"
	"errors"
	"net/http"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/gitrepo"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/workspace"
)

func (service *Service) retryCheckoutWork(ctx context.Context, workID string, request versionRequest) (domain.WorkItem, error) {
	return service.retryCheckoutWorkGate(ctx, workID, request, nil)
}

func (service *Service) retryCheckoutWorkGate(ctx context.Context, workID string, request versionRequest, continuation *sqlite.GateContinuation) (domain.WorkItem, error) {
	goalID, err := service.store.WorkGoalID(ctx, workID)
	if err != nil {
		return domain.WorkItem{}, mapStoreError(err)
	}
	model, err := service.store.GoalExecutionModel(ctx, goalID)
	if err != nil {
		return domain.WorkItem{}, mapStoreError(err)
	}
	if model != workspace.ExecutionCurrentDirectory {
		return domain.WorkItem{}, checkoutAPIError(sqlite.ErrExecutionMigrationRequired)
	}
	repo, err := gitrepo.Open(ctx, service.projectRoot)
	if err != nil {
		return domain.WorkItem{}, checkoutAPIError(err)
	}
	identity, err := repo.ReadCheckoutIdentity(ctx)
	if err != nil {
		return domain.WorkItem{}, checkoutAPIError(err)
	}
	checkout, readErr := service.store.Checkout(ctx)
	if readErr != nil && !errors.Is(readErr, basestore.ErrNotFound) {
		return domain.WorkItem{}, mapStoreError(readErr)
	}
	baseTree := identity.HeadTree
	if readErr == nil {
		baseTree = checkout.AcceptedTree
	}
	actual, err := repo.SnapshotTree(ctx, gitrepo.SnapshotSpec{BaseTree: baseTree, ExcludePaths: []string{service.store.Info().ProjectDir}})
	if err != nil {
		return domain.WorkItem{}, checkoutAPIError(err)
	}
	if actual.Identity != identity {
		return domain.WorkItem{}, checkoutAPIError(gitrepo.ErrCheckoutChanged)
	}
	if continuation == nil && (readErr != nil || checkout.WorkID == "") {
		if _, err := service.store.AdmitCheckout(ctx, goalID, identity, actual.Tree); err != nil {
			return domain.WorkItem{}, checkoutAPIError(err)
		}
	}
	event := sqlite.EventInput{Type: "WorkRetryReady", ActorType: "human", Payload: map[string]any{"reason": request.Reason, "tree": actual.Tree}}
	var work domain.WorkItem
	if continuation == nil {
		work, err = service.store.RetryCheckoutWork(ctx, workID, request.ExpectedVersion, identity, actual.Tree, event)
	} else {
		work, err = service.store.ResumeWorkGate(ctx, workID, *continuation, identity, actual.Tree, event)
	}
	if err != nil {
		return domain.WorkItem{}, checkoutAPIError(err)
	}
	return work, nil
}
func checkoutAPIError(err error) error {
	code := "CHECKOUT_WAITING"
	if errors.Is(err, sqlite.ErrExecutionMigrationRequired) {
		code = "EXECUTION_MIGRATION_REQUIRED"
	}
	if errors.Is(err, sqlite.ErrCheckoutBusy) {
		code = "PROJECT_BUSY"
	}
	return &api.APIError{Status: http.StatusConflict, Code: code, Message: err.Error()}
}

func (service *Service) checkFinalizeCheckout(ctx context.Context, goalID string, request finalizeRequest) error {
	checkout, err := service.store.Checkout(ctx)
	if err != nil {
		return checkoutAPIError(err)
	}
	if checkout.GoalID != goalID || checkout.WorkID != "" || checkout.AcceptedTree != request.Facts.IntegrationTree || checkout.AcceptedTree != request.Facts.ExpectedTree || checkout.AcceptedTree != request.Report.Final.Tree || checkout.AcceptedCommit != request.Report.Final.Commit {
		return checkoutAPIError(sqlite.ErrCheckoutConflict)
	}
	repo, err := gitrepo.Open(ctx, service.projectRoot)
	if err != nil {
		return checkoutAPIError(err)
	}
	ref, err := repo.ResolveRef(ctx, "refs/xgoal/goals/"+goalID+"/integration")
	if err != nil {
		return checkoutAPIError(err)
	}
	if ref.Commit != checkout.AcceptedCommit || ref.Tree != checkout.AcceptedTree {
		return checkoutAPIError(sqlite.ErrCheckoutConflict)
	}
	if err := repo.CheckSnapshot(ctx, gitrepo.SnapshotSpec{BaseTree: checkout.AcceptedTree, ExcludePaths: []string{service.store.Info().ProjectDir}}, checkout.Identity, checkout.AcceptedTree); err != nil {
		return checkoutAPIError(err)
	}
	return nil
}
