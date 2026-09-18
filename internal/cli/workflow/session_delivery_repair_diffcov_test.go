package workflow

// Moved from chat's diffcov2_test.go with session_delivery_repair.go.

import (
	"context"
	"errors"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestDiffCov2SessionAutoDeliveryRepairLoopAdvanceErrors(t *testing.T) {
	ctx := context.Background()
	// The first advance fails: the loop settles the run failure and returns.
	SessionAutoDeliveryRepairLoop(ctx, workflowledger.NewMemoryRepository(), "", nil, nil, "no-run",
		func(context.Context) (workflowledger.RunSnapshot, error) {
			return workflowledger.RunSnapshot{}, errors.New("advance boom")
		}, nil, false)

	// The second advance fails after one repair-continue attempt.
	repo := workflowledger.NewMemoryRepository()
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: "wfr-run", WorkflowName: "wf", Status: workflowledger.RunStatusPending, ActiveStepID: "build",
		StartedAt: time.Now(),
	}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, "wfr-run", 1, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	calls := 0
	SessionAutoDeliveryRepairLoop(ctx, repo, "", nil, nil, "wfr-run",
		func(context.Context) (workflowledger.RunSnapshot, error) {
			calls++
			if calls == 1 {
				return workflowledger.RunSnapshot{RunID: "wfr-run", Status: workflowledger.RunStatusDeliveryPending}, nil
			}
			return workflowledger.RunSnapshot{}, errors.New("second advance boom")
		}, nil, false)
	if calls != 2 {
		t.Fatalf("advance calls = %d; want 2", calls)
	}
}
