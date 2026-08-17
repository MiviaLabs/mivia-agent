package cli

// Regression tests for the terminal "canceled" chunk status (audit finding
// R3). A chunk canceled because its dependency failed keeps a failed run row
// and can even keep a merged branch. Before the fix the reconciler's terminal
// short-circuit named only merged/failed/skipped, so a canceled chunk fell to
// the reopen arm, returned to "reopened" - an admissible-pre status - and the
// next drive pass re-ran a chunk that was deliberately given up on.

import (
	"context"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// TestReconcileTaskLeavesCanceledChunkWithFailedRun pins that a canceled
// chunk whose run failed is left alone, never reopened.
func TestReconcileTaskLeavesCanceledChunkWithFailedRun(t *testing.T) {
	for _, runStatus := range []string{runStatusFailed, runStatusCanceled, runStatusTimedOut, runStatusDeliveryFailed} {
		task := tasks.Task{ID: "c2", Status: stackStatusCanceled, Deps: []string{"c1"}}
		act := reconcileCase(t, task, RunInfo{Present: true, Status: runStatus}, false, false, stackMaxChunkAttempts)
		if act.Action != stackActionLeave || act.NewStatus != "" {
			t.Fatalf("run %s: action = %q status = %q, want leave with no transition", runStatus, act.Action, act.NewStatus)
		}
	}
}

// TestReconcileTaskDoesNotMarkCanceledChunkMerged pins that git merge
// evidence never resurrects a canceled chunk as merged: the chunk was given
// up on, and marking it merged would unblock its dependents.
func TestReconcileTaskDoesNotMarkCanceledChunkMerged(t *testing.T) {
	task := tasks.Task{ID: "c2", Status: stackStatusCanceled, Deps: []string{"c1"}}
	act := reconcileCase(t, task, RunInfo{Present: true, Status: runStatusSucceeded}, true, true, stackMaxChunkAttempts)
	if act.Action != stackActionLeave || act.NewStatus != "" {
		t.Fatalf("action = %q status = %q, want leave with no transition", act.Action, act.NewStatus)
	}
}

// TestReconcileStackKeepsCanceledChunkOutOfTheNextWave is the end-to-end
// guard: one reconcile pass over a canceled chunk with a failed run row must
// leave it canceled, and the admission wave must not offer it.
func TestReconcileStackKeepsCanceledChunkOutOfTheNextWave(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ledger := tasks.NewMemoryStore()

	stackID := "stack-canceled-not-readmitted"
	seedStackTask(t, ledger, stackID, "c1")
	if err := ledger.TransitionTask(stackID, "c1", stackStatusFailed); err != nil {
		t.Fatal(err)
	}
	if err := ledger.CreateTask(tasks.Task{
		ID: "c2", PlanRef: stackID, Scope: stackScope(stackID),
		Status: stackStatusRunning, Deps: []string{"c1"},
	}); err != nil {
		t.Fatal(err)
	}
	seedFailedChunkRun(t, repo, stackID, "c2")
	if err := cancelStackDependents(ledger, stackID); err != nil {
		t.Fatalf("cancelStackDependents: %v", err)
	}
	if got := mustTaskStatus(t, ledger, stackID, "c2"); got != stackStatusCanceled {
		t.Fatalf("c2 status after cancel = %q, want %q", got, stackStatusCanceled)
	}

	if _, err := reconcileStack(ctx, ledger, repo, neverMergedChecker{}, stackID, stackMaxChunkAttempts); err != nil {
		t.Fatalf("reconcileStack: %v", err)
	}
	if got := mustTaskStatus(t, ledger, stackID, "c2"); got != stackStatusCanceled {
		t.Fatalf("c2 status after reconcile = %q, want it to stay %q", got, stackStatusCanceled)
	}

	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		t.Fatal(err)
	}
	wave := nextAdmissionWave(byID, stackMergedSet(byID), []string{"c1", "c2"})
	if len(wave) != 0 {
		t.Fatalf("admission wave = %v, want empty: a canceled chunk must never be re-admitted", wave)
	}
}

// TestStackAwaitsGrantOnlyIgnoresCanceledChunk pins that a canceled chunk
// does not disable the durable grant pause: it is terminal and dead, exactly
// like a merged one.
func TestStackAwaitsGrantOnlyIgnoresCanceledChunk(t *testing.T) {
	byID := map[string]tasks.Task{
		"c1": {ID: "c1", Status: stackStatusReviewed},
		"c2": {ID: "c2", Status: stackStatusCanceled, Deps: []string{"c3"}},
	}
	if !stackAwaitsGrantOnly(byID) {
		t.Fatal("stackAwaitsGrantOnly = false with a canceled chunk; want true (pause instead of polling)")
	}
}

// TestHaltStackForFailedChunkMessages pins the halt error text for both
// paths: with the reconcile mark-failed note and without it (the
// resumed-durable-failure path).
func TestHaltStackForFailedChunkMessages(t *testing.T) {
	ledger := tasks.NewMemoryStore()
	stackID := "stack-halt-message"
	seedStackTask(t, ledger, stackID, "c1")
	if err := ledger.TransitionTask(stackID, "c1", stackStatusFailed); err != nil {
		t.Fatal(err)
	}
	err := haltStackForFailedChunk(ledger, stackID, "c1", "run failed after 3 attempts")
	if err == nil || !strings.Contains(err.Error(), "run failed after 3 attempts") {
		t.Fatalf("halt error with a note = %v, want it to carry the note", err)
	}
	err = haltStackForFailedChunk(ledger, stackID, "c1", "")
	if err == nil || !strings.Contains(err.Error(), "chunk c1 failed terminally") {
		t.Fatalf("halt error without a note = %v, want the plain halt message", err)
	}
}

// mustTaskStatus reads one chunk task status.
func mustTaskStatus(t *testing.T, ledger *tasks.Store, stackID, chunkID string) string {
	t.Helper()
	task, err := ledger.GetTask(stackID, chunkID)
	if err != nil {
		t.Fatalf("read task %s: %v", chunkID, err)
	}
	return task.Status
}

// seedFailedChunkRun admits a chunk run row and settles it to failed.
func seedFailedChunkRun(t *testing.T, repo workflowledger.Repository, stackID, chunkID string) {
	t.Helper()
	ctx := context.Background()
	key, err := stackAdmissionKey(stackID, chunkID)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: map[string]string{"task": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-" + chunkID, InvocationKey: key, WorkflowName: "mini-stack",
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, run, snap); err != nil {
		t.Fatal(err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusFailed} {
		stored, err := repo.GetRun(ctx, run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, next, nil); err != nil {
			t.Fatal(err)
		}
	}
}
