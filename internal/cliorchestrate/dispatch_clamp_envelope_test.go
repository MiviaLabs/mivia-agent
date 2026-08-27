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
