package clichat

// failingCASRepository and failingGetRunRepository force ledger fault paths.
// Duplicated from internal/cliworkflow (workflow_stack_settle_test.go).

import (
	"context"
	"errors"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

type failingCASRepository struct {
	workflowledger.Repository
	failStatus workflowledger.RunStatus
}

func (f *failingCASRepository) CompareAndSetRunStatus(ctx context.Context, runID string, expectedVersion uint64, status workflowledger.RunStatus, finishedAt *time.Time) error {
	if status == f.failStatus {
		return errors.New("injected CAS failure")
	}
	return f.Repository.CompareAndSetRunStatus(ctx, runID, expectedVersion, status, finishedAt)
}

type failingGetRunRepository struct {
	workflowledger.Repository
	err error
}

func (f *failingGetRunRepository) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	return workflowledger.RunSnapshot{}, f.err
}
