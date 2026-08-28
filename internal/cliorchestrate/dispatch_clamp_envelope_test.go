package cliorchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestDispatchTasks_FailsFastOnExpiredCallerContext pins the BUG-B fix: a
// batch dispatched under an already-expired caller context must fail closed
// with a structured body and spawn NOTHING (no side-effecting subagents for
// a caller that can never consume results), instead of the old behavior of
// honoring multi-hour requested budgets.
func TestDispatchTasks_FailsFastOnExpiredCallerContext(t *testing.T) {
	t.Parallel()
	tool := &dispatchTasksTool{cfg: config.SubagentConfig{}}
	ctx, cancel := context.WithTimeout(context.Background(), -1*time.Second) // already expired
	defer cancel()

	out, err := tool.Execute(ctx, json.RawMessage(`{"tasks":[{"id":"t1","prompt":"side effects"}]}`))
	if err != nil {
		t.Fatalf("Execute returned Go error %v; expired-ctx is a modeled outcome, not an error", err)
	}
	if !strings.Contains(out, "caller context already expired") {
		t.Errorf("body = %s, want expired-context explanation", out)
	}
	if !strings.Contains(out, "canceled") {
		t.Errorf("body = %s, want canceled status", out)
	}
}

// TestEncodeSyncRunResult_ErrorEnvelopesCarryRunID pins the BUG-C fix: every
// run-level failure branch includes run_id (the handle was registered before
// Join) plus an inspect/cancel hint, so the model can recover the run after
// a cancel/timeout instead of losing its only pointer to it.
func TestEncodeSyncRunResult_ErrorEnvelopesCarryRunID(t *testing.T) {
	t.Parallel()
	tool := &dispatchTasksTool{cfg: config.SubagentConfig{}}
	snap := ledger.RunSnapshot{RunID: "run-deadbeef"}

	canceled := tool.encodeSyncRunResult(snap, nil, context.Canceled)
	for _, want := range []string{"run-deadbeef", `"status":"canceled"`, "inspect_agents"} {
		if !strings.Contains(canceled, want) {
			t.Errorf("cancel envelope missing %q: %s", want, canceled)
		}
	}

	runErr := errors.New("join failed")
	fromResult := tool.encodeSyncRunResult(snap, &coordinator.RunResult{Err: runErr}, nil)
	for _, want := range []string{"run-deadbeef", "join failed", "cancel_run"} {
		if !strings.Contains(fromResult, want) {
			t.Errorf("run-result-error envelope missing %q: %s", want, fromResult)
		}
	}
}

// TestEncodeSyncRunResult_ResultsOmitHint keeps the success shape stable:
// when per-task results exist, the bare array is returned with no injected
// run-level fields (model payloads stay backward compatible).
func TestEncodeSyncRunResult_ResultsOmitHint(t *testing.T) {
	t.Parallel()
	tool := &dispatchTasksTool{cfg: config.SubagentConfig{}}
	snap := ledger.RunSnapshot{RunID: "run-ok"}
	result := &coordinator.RunResult{
		Snapshot: snap,
		Results:  []subagents.Result{{TaskID: "t1", Status: "completed"}},
	}
	got := tool.encodeSyncRunResult(snap, result, nil)
	if strings.Contains(got, "hint") || strings.Contains(got, "inspect_agents") {
		t.Errorf("success path must not carry recovery hint fields: %s", got)
	}
}

// TestDispatchTasks_RejectsTaskMissingID pins the fix for a batch member the
// model left unnamed: it used to fall through to
// namespacedTaskID(namespace, "") (short-circuits to ""), leaving
// subagents.Task.ID empty all the way to coordinator.createTask, which then
// minted an UNRELATED random ID via newTaskID() ("anonymous task" support -
// meant for a single-task spawn, not a dispatch_tasks batch member). The
// UI's sidebar row for that task is built independently from the model's own
// JSON args and has no way to learn that random ID, so the row never
// receives a matching progress/heartbeat/done event again and sits frozen at
// Step 0 until the stall-display badge fires - a real, reproduced fleet
// symptom ("one agent out of every batch is always stuck stalled at step
// 0"), not a display bug. buildTasks must now refuse the whole call up
// front with an actionable error instead of silently spawning an
// unattributable task.
func TestDispatchTasks_RejectsTaskMissingID(t *testing.T) {
	t.Parallel()
	tool := &dispatchTasksTool{cfg: config.SubagentConfig{}}
	ctx := context.Background()

	_, err := tool.Execute(ctx, json.RawMessage(`{"tasks":[
		{"id":"reviewer-a","prompt":"review a"},
		{"id":"reviewer-b","prompt":"review b"},
		{"prompt":"review c missing its id"},
		{"id":"reviewer-d","prompt":"review d"}
	]}`))
	if err == nil {
		t.Fatal("Execute returned nil error for a batch with a task missing id, want a validation error")
	}
	if !strings.Contains(err.Error(), "id is required") {
		t.Errorf("error = %v, want it to name the missing id", err)
	}
}
