package controller

import (
	"context"
	"errors"
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

func TestCancelRunWithAttemptsWithClaimKeepsClaim(t *testing.T) {
	ctx := context.Background()
	repo, runID := newCancelRunFixture(t, workflowledger.RunStatusRunning)
	const holder = "cancel-holder"
	if err := repo.ClaimRun(ctx, runID, holder); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.ReleaseRun(context.Background(), runID, holder) }()
	if _, err := CancelRunWithAttemptsWithClaim(ctx, repo, runID, holder); err != nil {
		t.Fatalf("CancelRunWithAttemptsWithClaim: %v", err)
	}
	if err := repo.ClaimRun(ctx, runID, "other-holder"); !errors.Is(err, workflowledger.ErrClaimHeld) {
		t.Fatalf("claim after cancel settlement = %v, want ErrClaimHeld", err)
	}
}

// TestCancelRunWithAttemptsReturnsCanceledAttemptsWithErrorRef: the exported
// CancelRunWithAttempts must return every attempt it canceled with the
// canceled status and the operator-cancel ErrorRef set, while leaving terminal
// attempts untouched.
func TestCancelRunWithAttemptsReturnsCanceledAttemptsWithErrorRef(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	runID := "wfr-cancel-attempts-return"
	run := workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending, ActiveStepID: "one"}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []workflowledger.StepAttempt{
		{AttemptID: "wfa-one-1", RunID: runID, StepID: "one", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning},
		{AttemptID: "wfa-one-2", RunID: runID, StepID: "one", AttemptNo: 2, Status: workflowledger.AttemptStatusRunning},
	} {
		if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
			t.Fatal(err)
		}
	}
	// A terminal attempt must be left untouched and excluded from the result.
	if err := repo.CompleteStepAttempt(ctx, runID, "wfa-one-2", 1, workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	canceled, err := CancelRunWithAttempts(ctx, repo, runID)
	if err != nil {
		t.Fatalf("CancelRunWithAttempts: %v", err)
	}
	if len(canceled) != 1 {
		t.Fatalf("canceled attempts = %d, want exactly 1: %+v", len(canceled), canceled)
	}
	if canceled[0].AttemptID != "wfa-one-1" || canceled[0].Status != workflowledger.AttemptStatusCanceled {
		t.Fatalf("canceled attempt = %+v, want wfa-one-1 canceled", canceled[0])
	}
	if canceled[0].ErrorRef == "" {
		t.Fatal("canceled attempt ErrorRef is empty, want operator-cancel detail")
	}
	raw, err := repo.LoadContent(ctx, canceled[0].ErrorRef)
	if err != nil {
		t.Fatalf("LoadContent: %v", err)
	}
	if !strings.Contains(string(raw), "canceled by operator") {
		t.Fatalf("error ref content = %q, want text containing 'canceled by operator'", raw)
	}
	storedRun, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRun.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want canceled", storedRun.Status)
	}
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if attempts[0].Status != workflowledger.AttemptStatusCanceled || attempts[1].Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("stored attempts = %+v, want canceled and untouched succeeded", attempts)
	}
}

// TestCancelRunMarksInFlightAttemptWithErrorRef verifies that CancelRun
// completes an in-flight attempt as canceled and persists an operator-cancel
// detail ref on the attempt.
func TestCancelRunMarksInFlightAttemptWithErrorRef(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	runID := "wfr-cancel-errorref"
	run := workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending, ActiveStepID: "one"}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{AttemptID: "wfa-one-1", RunID: runID, StepID: "one", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := CancelRun(ctx, repo, runID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusCanceled {
		t.Fatalf("attempts = %+v, want one canceled attempt", attempts)
	}
	if attempts[0].ErrorRef == "" {
		t.Fatal("attempt ErrorRef is empty, want operator-cancel detail")
	}
	raw, err := repo.LoadContent(ctx, attempts[0].ErrorRef)
	if err != nil {
		t.Fatalf("LoadContent: %v", err)
	}
	if !strings.Contains(string(raw), "canceled by operator") {
		t.Fatalf("error ref content = %q, want text containing 'canceled by operator'", raw)
	}
}
