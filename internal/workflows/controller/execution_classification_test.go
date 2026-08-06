package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestScalarOutputRoutesOnStatusOnly pins E1: a child that completes with
// non-object output (scalar/array) still routes on a status-only transition
// instead of failing the whole run.
func TestScalarOutputRoutesOnStatusOnly(t *testing.T) {
	runner := &errorRunner{results: map[string]AgentStepResult{
		"one": {Output: json.RawMessage(`42`), Status: "completed"},
	}}
	ctrl, repo := newErrorController(t, runner, "wfr-scalar-output")
	if _, err := ctrl.Run(context.Background()); err != nil {
		t.Fatalf("run with scalar output must succeed: %v", err)
	}
	run, err := repo.GetRun(context.Background(), "wfr-scalar-output")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", run.Status)
	}
}

// TestChildStatusCanceledUnderParentDeadline pins E2: when the PARENT (run)
// deadline expires mid-step, joinWithCancellation cancels the child, so the
// child reports "canceled" while the parent join returns a deadline error.
// That is a run TIMEOUT, not a cancel: the run deadline must settle timed_out
// even though the child was canceled as a side effect.
func TestChildStatusCanceledUnderParentDeadline(t *testing.T) {
	runner := &errorRunner{results: map[string]AgentStepResult{
		"one": {Output: nil, Status: "canceled"},
	}, errors: map[string]error{
		"one": context.DeadlineExceeded,
	}}
	ctrl, repo := newErrorController(t, runner, "wfr-child-status")
	if _, err := ctrl.Run(context.Background()); err != nil {
		// Run settles failed/canceled with an error; the status matters.
		_ = err
	}
	attempts, err := repo.ListStepAttempts(context.Background(), "wfr-child-status")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Status != workflowledger.AttemptStatusTimedOut {
		t.Fatalf("attempt status = %q, want timed_out (parent deadline is a run timeout)", attempts[0].Status)
	}
}

// TestChildFailedWinsOverParentError pins E3: a child that genuinely failed
// just before the parent deadline raced the join boundary still classifies as
// failed — the child's terminal failure is more truthful than the racing
// parent error.
func TestChildFailedWinsOverParentError(t *testing.T) {
	runner := &errorRunner{results: map[string]AgentStepResult{
		"one": {Output: nil, Status: "failed"},
	}, errors: map[string]error{
		"one": context.DeadlineExceeded,
	}}
	ctrl, repo := newErrorController(t, runner, "wfr-child-failed")
	if _, err := ctrl.Run(context.Background()); err != nil {
		_ = err
	}
	attempts, err := repo.ListStepAttempts(context.Background(), "wfr-child-failed")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("attempt status = %q, want failed (child failure wins over racing parent error)", attempts[0].Status)
	}
}

// TestChildCompletedWinsOverParentError pins E4: a child whose work completed
// while the parent ctx raced to expiry must not have its result discarded as
// a timeout — the step succeeded.
func TestChildCompletedWinsOverParentError(t *testing.T) {
	runner := &errorRunner{results: map[string]AgentStepResult{
		"one": {Output: json.RawMessage(`{"ok":true}`), Status: "completed"},
	}, errors: map[string]error{
		"one": context.DeadlineExceeded,
	}}
	ctrl, repo := newErrorController(t, runner, "wfr-child-completed")
	if _, err := ctrl.Run(context.Background()); err != nil {
		_ = err
	}
	attempts, err := repo.ListStepAttempts(context.Background(), "wfr-child-completed")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("attempt status = %q, want succeeded (completed child wins)", attempts[0].Status)
	}
}

// The deadline helper keeps the compile-time clock reference stable.
var _ = time.Second
