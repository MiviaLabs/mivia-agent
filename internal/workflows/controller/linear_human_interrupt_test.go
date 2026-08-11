package controller

import (
	"context"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestHumanGateInterruptedAttemptWithPendingApprovalResumesAndReParks pins the
// DC-1/DC-4 crash-resume contract for a human gate: a gate attempt that a
// crashed/abandoned executor left Interrupted (the artifact
// localengine.Engine.Interrupt's markOpenAttemptsInterrupted writes) must NOT
// fail the run on resume. Advance must re-park the run at waiting_approval
// with a fresh attempt (No+1) and a fresh pending approval, so the operator's
// approval stays reachable. Before the fix, reconcileWaitingApproval failed
// the run with "human_gate step ... has terminal attempt with pending
// approval" — a transient crash condition reaching a terminal state with no
// return edge.
func TestHumanGateInterruptedAttemptWithPendingApprovalResumesAndReParks(t *testing.T) {
	ctx := context.Background()
	wf := humanOnlyWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-human-interrupt-repark", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(ctx)
	if err != nil || got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run = %+v, err = %v, want waiting_approval", got, err)
	}
	// Reproduce the crash-abandon artifact: the gate attempt parked at attempt
	// 1 is left Interrupted by markOpenAttemptsInterrupted (version CAS).
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil || len(attempts) != 1 || attempts[0].AttemptNo != 1 {
		t.Fatalf("attempts = %+v, err = %v, want exactly attempt 1", attempts, err)
	}
	if err := repo.CompleteStepAttempt(ctx, ctrl.RunID, attempts[0].AttemptID, attempts[0].Version, workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusInterrupted}); err != nil {
		t.Fatal(err)
	}
	// Resume: Advance must re-park, not fail.
	got, done, err := ctrl.Advance(ctx)
	if err != nil || !done || got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("advance = %+v done=%v err=%v, want re-parked waiting_approval", got, done, err)
	}
	attempts, err = repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts = %d, err = %v, want 2 (interrupted No 1 + fresh No 2)", len(attempts), err)
	}
	if attempts[0].Status != workflowledger.AttemptStatusInterrupted || attempts[0].AttemptNo != 1 {
		t.Fatalf("stale attempt = %+v, want interrupted No 1", attempts[0])
	}
	if attempts[1].AttemptNo != 2 || attempts[1].Status != workflowledger.AttemptStatusRunning {
		t.Fatalf("fresh attempt = %+v, want No 2 Running", attempts[1])
	}
	approvals, err := repo.ListApprovals(ctx, ctrl.RunID)
	if err != nil || len(approvals) != 2 {
		t.Fatalf("approvals = %+v, err = %v, want 2 (stale No 1 + fresh No 2)", approvals, err)
	}
	fresh := PendingApprovalID("approve_me", 2)
	foundFresh := false
	for _, a := range approvals {
		if a.ApprovalID == fresh {
			foundFresh = true
			if a.Status != "pending" {
				t.Fatalf("fresh approval = %+v, want pending", a)
			}
		}
	}
	if !foundFresh {
		t.Fatalf("fresh approval %q missing: %+v", fresh, approvals)
	}
	// Stale replay of the interrupted attempt's approval must be refused and
	// must leave the re-parked gate untouched (finishHumanResolutionForAttempt
	// newer-attempt guard).
	if err := ctrl.Approve(ctx, PendingApprovalID("approve_me", 1), "operator"); err == nil {
		t.Fatal("stale approval for interrupted attempt 1 was accepted")
	}
	run, _ := repo.GetRun(ctx, ctrl.RunID)
	if run.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("status after stale approve = %q, want waiting_approval", run.Status)
	}
	attempts, _ = repo.ListStepAttempts(ctx, ctrl.RunID)
	if attempts[1].Status != workflowledger.AttemptStatusRunning {
		t.Fatalf("fresh attempt after stale approve = %+v, want still Running", attempts[1])
	}
	// The current approval drives the run to success.
	if err := ctrl.Approve(ctx, fresh, "operator"); err != nil {
		t.Fatal(err)
	}
	got, err = ctrl.Run(ctx)
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("final run = %+v, err = %v, want succeeded", got, err)
	}
}

// TestHumanGateInterruptedAttemptWithoutApprovalResumes pins the crash window
// between attempt create and approval create (an Interrupted attempt with no
// approval record — the exact state markOpenAttemptsInterrupted leaves when
// the crash happens before pauseHumanGate created the approval). Resume must
// re-admit a fresh attempt and approval instead of failing the run with
// "human_gate step ... was interrupted".
func TestHumanGateInterruptedAttemptWithoutApprovalResumes(t *testing.T) {
	ctx := context.Background()
	wf := humanOnlyWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-human-interrupt-noapproval", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		AttemptID: "wfa-approve_me-1", RunID: ctrl.RunID, StepID: "approve_me", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetStepAttempt(ctx, ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStepAttempt(ctx, ctrl.RunID, attempt.AttemptID, stored.Version, workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusInterrupted}); err != nil {
		t.Fatal(err)
	}
	// Resume: Advance must re-park, not fail.
	got, done, err := ctrl.Advance(ctx)
	if err != nil || !done || got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("advance = %+v done=%v err=%v, want re-parked waiting_approval", got, done, err)
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts = %d, err = %v, want 2 (interrupted No 1 + fresh No 2)", len(attempts), err)
	}
	if attempts[0].Status != workflowledger.AttemptStatusInterrupted || attempts[0].AttemptNo != 1 {
		t.Fatalf("stale attempt = %+v, want interrupted No 1", attempts[0])
	}
	if attempts[1].AttemptNo != 2 || attempts[1].Status != workflowledger.AttemptStatusRunning {
		t.Fatalf("fresh attempt = %+v, want No 2 Running", attempts[1])
	}
	approvals, err := repo.ListApprovals(ctx, ctrl.RunID)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals = %+v, err = %v, want exactly one", approvals, err)
	}
	if approvals[0].ApprovalID != PendingApprovalID("approve_me", 2) || approvals[0].Status != "pending" {
		t.Fatalf("approval = %+v, want fresh pending No 2", approvals[0])
	}
	// The fresh approval still drives the run to success.
	if err := ctrl.Approve(ctx, PendingApprovalID("approve_me", 2), "operator"); err != nil {
		t.Fatal(err)
	}
	got, err = ctrl.Run(ctx)
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("final run = %+v, err = %v, want succeeded", got, err)
	}
}

// TestHumanGateFailedAttemptWithPendingApprovalStillFailsClosed is the
// fail-closed regression guard for the re-entry rule: ONLY an Interrupted
// attempt (a crash artifact) re-admits on resume. Every other terminal status
// with a pending approval — Failed here, the representative non-interrupted
// terminal status — must still fail the run closed.
func TestHumanGateFailedAttemptWithPendingApprovalStillFailsClosed(t *testing.T) {
	ctx := context.Background()
	wf := humanOnlyWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-human-failclosed", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(ctx)
	if err != nil || got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run = %+v, err = %v, want waiting_approval", got, err)
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %+v, err = %v, want exactly attempt 1", attempts, err)
	}
	if err := repo.CompleteStepAttempt(ctx, ctrl.RunID, attempts[0].AttemptID, attempts[0].Version, workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusFailed}); err != nil {
		t.Fatal(err)
	}
	got, done, err := ctrl.Advance(ctx)
	if !done || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("advance = %+v done=%v err=%v, want failed terminal", got, done, err)
	}
	if err == nil || !strings.Contains(err.Error(), "terminal attempt with pending approval") {
		t.Fatalf("err = %v, want fail-closed 'terminal attempt with pending approval'", err)
	}
	attempts, _ = repo.ListStepAttempts(ctx, ctrl.RunID)
	if len(attempts) != 1 {
		t.Fatalf("attempts = %+v, want exactly 1 (no new attempt)", attempts)
	}
}
