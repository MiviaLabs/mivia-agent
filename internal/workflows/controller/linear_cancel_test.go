package controller

import (
	"context"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func newCancelRunFixture(t *testing.T, status workflowledger.RunStatus) (workflowledger.Repository, string) {
	t.Helper()
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	runID := "wfr-cancel"
	run := workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending, ActiveStepID: "one"}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if status == workflowledger.RunStatusPending {
		return repo, runID
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	// pending can only CAS to running; reach other statuses through it.
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatalf("CAS to running: %v", err)
	}
	if status == workflowledger.RunStatusRunning {
		return repo, runID
	}
	stored, err = repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, status, nil); err != nil {
		t.Fatalf("CAS to %q: %v", status, err)
	}
	return repo, runID
}

func TestCancelRunPending(t *testing.T) {
	ctx := context.Background()
	repo, runID := newCancelRunFixture(t, workflowledger.RunStatusPending)
	if err := CancelRun(ctx, repo, runID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want canceled", run.Status)
	}
	if run.Version != 3 {
		t.Fatalf("run version = %d, want 3 (pending->running->canceled)", run.Version)
	}
}

func TestCancelRunRunning(t *testing.T) {
	ctx := context.Background()
	repo, runID := newCancelRunFixture(t, workflowledger.RunStatusRunning)
	if err := CancelRun(ctx, repo, runID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want canceled", run.Status)
	}
}

func TestCancelRunWaitingApprovalMarksAttempts(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	wf := humanDeliveryWorkflow(t, false)
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-cancel-gate", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run status = %q, want waiting_approval", got.Status)
	}
	if err := CancelRun(ctx, repo, ctrl.RunID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want canceled", run.Status)
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusCanceled {
		t.Fatalf("attempts = %+v, want one canceled attempt", attempts)
	}
}

func TestCancelRunDeliveryPendingRefused(t *testing.T) {
	ctx := context.Background()
	repo, runID := newCancelRunFixture(t, workflowledger.RunStatusDeliveryPending)
	err := CancelRun(ctx, repo, runID)
	if err == nil || !strings.Contains(err.Error(), "waiting for delivery") {
		t.Fatalf("CancelRun on delivery_pending = %v, want a delivery refusal", err)
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want unchanged delivery_pending", run.Status)
	}
}

func TestCancelRunTerminalNoOp(t *testing.T) {
	ctx := context.Background()
	repo, runID := newCancelRunFixture(t, workflowledger.RunStatusSucceeded)
	if err := CancelRun(ctx, repo, runID); err != nil {
		t.Fatalf("CancelRun on a terminal run: %v", err)
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded || run.Version != 3 {
		t.Fatalf("run = %q v%d, want untouched succeeded v3 (pending->running->succeeded fixture CASes)", run.Status, run.Version)
	}
}

func TestCancelRunMissingRun(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	if err := CancelRun(ctx, repo, "wfr-missing"); err != workflowledger.ErrNotFound {
		t.Fatalf("CancelRun on a missing run = %v, want ErrNotFound", err)
	}
}
