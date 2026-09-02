// Package finalize coordinates the report filesystem protocol with SQLite completion.
package finalize

import (
	"context"
	"errors"
	"fmt"

	"github.com/monshunter/xgoal/internal/completion"
	"github.com/monshunter/xgoal/internal/report"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

type Store interface {
	FinalizeGoal(context.Context, string, int64, sqlite.CompletionFacts, report.PreparedFiles, sqlite.EventInput) (completion.Result, sqlite.FinalReportRecord, error)
	MarkFinalReportCommitted(context.Context, string, int64) (sqlite.FinalReportRecord, error)
	PendingFinalReports(context.Context) ([]sqlite.FinalReportRecord, error)
	FinalReport(context.Context, string) (sqlite.FinalReportRecord, error)
}

type Manager struct {
	store Store
	files *report.FileManager
}

func New(store Store, files *report.FileManager) (*Manager, error) {
	if store == nil || files == nil {
		return nil, errors.New("finalizer requires store and file manager")
	}
	return &Manager{store: store, files: files}, nil
}

func (manager *Manager) Finalize(ctx context.Context, goalID string, expectedGoalVersion int64, facts sqlite.CompletionFacts, value report.Report, event sqlite.EventInput) (completion.Result, sqlite.FinalReportRecord, error) {
	artifact, err := report.Render(value)
	if err != nil {
		return completion.Result{}, sqlite.FinalReportRecord{}, err
	}
	facts.FinalReportHash = artifact.ReportHash
	prepared, err := manager.files.Prepare(goalID, artifact)
	if err != nil {
		return completion.Result{}, sqlite.FinalReportRecord{}, err
	}
	result, record, err := manager.store.FinalizeGoal(ctx, goalID, expectedGoalVersion, facts, prepared, event)
	if err != nil {
		return completion.Result{}, sqlite.FinalReportRecord{}, errors.Join(err, manager.files.Discard(prepared))
	}
	if !result.Complete {
		return result, sqlite.FinalReportRecord{}, manager.files.Discard(prepared)
	}
	if err := manager.files.Commit(record.Files); err != nil {
		return result, record, fmt.Errorf("publish final report; recovery required: %w", err)
	}
	committed, err := manager.store.MarkFinalReportCommitted(ctx, goalID, record.Version)
	return result, committed, err
}

// Recover publishes every report whose Goal transaction committed before file publication completed.
func (manager *Manager) Recover(ctx context.Context) error {
	records, err := manager.store.PendingFinalReports(ctx)
	if err != nil {
		return err
	}
	var result error
	for _, record := range records {
		if err := manager.files.Recover(record.Files); err != nil {
			result = errors.Join(result, fmt.Errorf("recover report for goal %s: %w", record.Files.GoalID, err))
			continue
		}
		if _, err := manager.store.MarkFinalReportCommitted(ctx, record.Files.GoalID, record.Version); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (manager *Manager) Read(ctx context.Context, goalID string) (sqlite.FinalReportRecord, []byte, []byte, error) {
	record, err := manager.store.FinalReport(ctx, goalID)
	if err != nil {
		return sqlite.FinalReportRecord{}, nil, nil, err
	}
	if record.State != sqlite.FinalReportCommitted {
		if err := manager.files.Recover(record.Files); err != nil {
			return sqlite.FinalReportRecord{}, nil, nil, fmt.Errorf("recover pending final report: %w", err)
		}
		record, err = manager.store.MarkFinalReportCommitted(ctx, goalID, record.Version)
		if err != nil {
			return sqlite.FinalReportRecord{}, nil, nil, fmt.Errorf("commit recovered final report: %w", err)
		}
	}
	jsonBytes, markdown, err := manager.files.Read(record.Files)
	return record, jsonBytes, markdown, err
}
