package coordinator

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// onTaskDone is installed on the subagent pool as Pool.OnTaskDone (types.go
// New). It runs on the pool worker goroutine immediately after a task's
// handler returns, while the rest of the pool may still be executing.
//
// The coordinator uses it to finalize terminal tasks EARLY — CAS the ledger
// status, mark the mailbox terminal, and decline parked asks — instead of
// waiting for the whole pool to finish before recordRunResults applies the
// finalize fence (plan R9). A parked asker (post_message kind=ask,
// wait_seconds=N) whose target's handler returns without answering is
// therefore unblocked with the decline sentinel as soon as the handler
// returns, not after the pool drains (which can exceed the asker's wait and
// burn the full timeout: live repro parked 30s, responder finished in 880ms,
// asker got no_answer/timed_out).
//
// The callback is deliberately side-effect-light and fail-safe: every guard
// that cannot confirm it should act returns without doing anything, leaving
// recordRunResults (which always runs at run end) to finalize. Output and
// attempt persistence are NOT done here — recordRunResults does that once at
// run end with the run's canonical result set, and appends the single terminal
// event.
func (c *coordinator) onTaskDone(ctx context.Context, t subagents.Task, r subagents.Result) {
	if c == nil || c.repo == nil {
		return
	}
	id, ok := runtime.TaskIdentityFrom(ctx)
	if !ok || id.RunID == "" || id.TaskID == "" {
		// Not a coordinator-run task (no stamped identity): nothing to fence.
		return
	}
	// The locking accessor, never a raw map read: handlesByRun is guarded.
	h := c.HandleForRun(id.RunID)
	if h == nil {
		return
	}
	// Compare on the MAPPED status (ledger vocabulary), never raw r.Status.
	status := mapStatus(r)
	if !IsTaskTerminal(status) {
		// Still queued/running/awaiting_input/retry_pending etc: no early fence.
		return
	}
	// Cancel guards: a canceled run must not get an early terminal CAS —
	// recordRunResults applies the cancel override at run end. Read the
	// current snapshot so a cancel that already claimed the task (running →
	// cancel_requested/canceled) is never raced into an invalid final status.
	// poolContext() is the locking accessor: referral-task pool runs execute
	// concurrently with executeResumedRun's rewrite of h.poolCtx, so a raw
	// field read here would race.
	if h.poolContext().Err() != nil {
		return
	}
	snap, err := c.repo.GetTask(ctx, id.RunID, id.TaskID)
	if err != nil {
		return // fail safe: recordRunResults finalizes at run end
	}
	if snap.Status == string(ledger.TaskStatusCancelRequested) || snap.Status == string(ledger.TaskStatusCanceled) {
		return
	}
	// Retry guard: with a retry-enabled policy, failed/timed_out outcomes are
	// the retry scheduler's decision (processResults transitions them to
	// retry_pending and re-dispatches; a retried task may run again and
	// answer), so the early CAS must not terminalize them. Production wires
	// NoRetry (orchestration_state.go), so this only affects retry-enabled
	// library/test paths, where failed/timed_out keep the pre-existing
	// late-fence behavior.
	if c.retryPolicyLocked().MaxRetries > 0 &&
		(status == string(ledger.TaskStatusFailed) || status == string(ledger.TaskStatusTimedOut)) {
		return
	}
	// Silent status CAS: no event append here — recordRunResults appends the
	// single terminal event for the task. A lost CAS (version moved under us:
	// cancel, retry, or recordRunResults) fails safe to the run-end finalize.
	if err := c.repo.CompareAndSetTaskStatus(ctx, id.RunID, id.TaskID, snap.Version, status); err != nil {
		return // fail safe: recordRunResults finalizes at run end
	}
	// Terminal mailbox fence: drains the mailbox, marks it terminal, and runs
	// declineAsksForTerminalTask. The gate re-reads the ledger — now terminal
	// — so every ask targeting this task declines its parked asker instead of
	// leaving it to burn the full wait_seconds. h is non-nil here; the call is
	// itself nil-safe.
	h.MarkTaskMailboxTerminal(id.TaskID)
}
