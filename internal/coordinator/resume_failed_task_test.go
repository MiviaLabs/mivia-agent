package coordinator

// TestResumeFailInterruptedRunWithFailedTaskSettlesNotInvalidTransition pins a
// live workflow finding: a panel member run (RunPolicy FailInterrupted, so
// requeuePersistedFailures never runs) whose task already sits at failed was
// re-dispatched on every rejoin. terminalTaskResult treated only
// completed/canceled/blocked as terminal, so the failed task entered the
// dispatchable list and the DAG's failed -> running CAS hit
// ErrInvalidTransition - deterministically, on every review_panel reentry,
// until the step's retry budget exhausted and the workflow run died. A task
// still failed/timed_out after the requeue pass can never legally run again:
// resume must surface it as a recovered terminal result instead.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func TestResumeFailInterruptedRunWithFailedTaskSettlesNotInvalidTransition(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ctx := context.Background()
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r", Status: ledger.RunStatusRunning, Policy: ledger.RunPolicy{NoRetry: true, FailInterrupted: true}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, lifecycleTask("t1", string(ledger.TaskStatusQueued))); err != nil {
		t.Fatal(err)
	}
	// The live shape: the member's task was RUNNING when its executor died.
	// The fail-interrupted resume marks it failed (markInterruptedTasks) and
	// - because FailInterrupted skips requeuePersistedFailures - must then
	// surface it as a terminal result, not re-dispatch it.
	if err := repo.CompareAndSetTaskStatus(ctx, "r", "t1", 1, string(ledger.TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1})).(*coordinator)

	h, err := c.ResumeInterruptedRun(ctx, "r")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	_, joinErr := c.Join(ctx, h)
	if joinErr != nil && strings.Contains(joinErr.Error(), "invalid state transition") {
		t.Fatalf("join error = %v; a still-failed task must be recovered as a terminal result, never re-dispatched into failed -> running", joinErr)
	}
	// The failed task must stay failed - never mutated by the rejoin.
	snap, err := repo.GetTask(ctx, "r", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusFailed) {
		t.Fatalf("task status = %q, want failed (untouched by the rejoin)", snap.Status)
	}
}
