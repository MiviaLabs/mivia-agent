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

// TestChildStatusWinsOverParentError pins E2: when the child reports canceled
// while the parent join returns a deadline error, the attempt and run are
// classified canceled, not timed out.
func TestChildStatusWinsOverParentError(t *testing.T) {
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
	if attempts[0].Status != workflowledger.AttemptStatusCanceled {
		t.Fatalf("attempt status = %q, want canceled (child status must win over parent error)", attempts[0].Status)
	}
}

// The deadline helper keeps the compile-time clock reference stable.
var _ = time.Second
