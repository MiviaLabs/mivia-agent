package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestRunWithCancelReconciliationRetryRetriesUntilResolved is the core
// regression test for Wave 8 audit finding #2: Run's driver loop used to
// conflate "cancel_pending, not yet all-terminal" with the Wave-4
// members-complete sentinel, both surfacing as ErrPanelMembersComplete -
// which isNonTerminalWorkflowStop treats as a silent no-op, stranding a
// legitimately still-canceling run with no automatic retry. This proves
// RunWithCancelReconciliationRetry keeps calling run while it reports
// ErrCancelReconciliationPending and returns as soon as run reports
// something else.
func TestRunWithCancelReconciliationRetryRetriesUntilResolved(t *testing.T) {
	calls := 0
	want := workflowledger.RunSnapshot{RunID: "wf-retry", Status: workflowledger.RunStatusCanceled}
	run := func(context.Context) (workflowledger.RunSnapshot, error) {
		calls++
		if calls < 3 {
			return workflowledger.RunSnapshot{}, ErrCancelReconciliationPending
		}
		return want, nil
	}
	got, err := RunWithCancelReconciliationRetry(context.Background(), run)
	if err != nil {
		t.Fatalf("RunWithCancelReconciliationRetry() error = %v", err)
	}
	if got.RunID != want.RunID || got.Status != want.Status {
		t.Fatalf("RunWithCancelReconciliationRetry() = %+v, want %+v", got, want)
	}
	if calls != 3 {
		t.Fatalf("run called %d times, want exactly 3 (2 pending retries + 1 resolved)", calls)
	}
}

// TestRunWithCancelReconciliationRetryPassesThroughOtherErrors proves the
// retry loop does not swallow or delay any other error, including the true
// Wave-4 ErrPanelMembersComplete sentinel - only ErrCancelReconciliationPending
// triggers a retry.
func TestRunWithCancelReconciliationRetryPassesThroughOtherErrors(t *testing.T) {
	calls := 0
	sentinel := ErrPanelMembersComplete
	run := func(context.Context) (workflowledger.RunSnapshot, error) {
		calls++
		return workflowledger.RunSnapshot{}, sentinel
	}
	_, err := RunWithCancelReconciliationRetry(context.Background(), run)
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunWithCancelReconciliationRetry() error = %v, want %v", err, sentinel)
	}
	if calls != 1 {
		t.Fatalf("run called %d times for a non-retryable error, want exactly 1", calls)
	}
}

// TestRunWithCancelReconciliationRetryGivesUpAfterLimit proves the retry
// loop is bounded: a genuinely stuck cancel_pending reconciliation (e.g. an
// ambiguous claim that never resolves) must not retry forever - it returns
// ErrCancelReconciliationPending after cancelReconciliationRetryLimit
// retries for a later operator cancel or resume to reconcile instead.
func TestRunWithCancelReconciliationRetryGivesUpAfterLimit(t *testing.T) {
	calls := 0
	run := func(context.Context) (workflowledger.RunSnapshot, error) {
		calls++
		return workflowledger.RunSnapshot{}, ErrCancelReconciliationPending
	}
	start := time.Now()
	_, err := RunWithCancelReconciliationRetry(context.Background(), run)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrCancelReconciliationPending) {
		t.Fatalf("RunWithCancelReconciliationRetry() error = %v, want ErrCancelReconciliationPending", err)
	}
	wantCalls := cancelReconciliationRetryLimit + 1
	if calls != wantCalls {
		t.Fatalf("run called %d times, want exactly %d (initial + %d retries)", calls, wantCalls, cancelReconciliationRetryLimit)
	}
	wantMinElapsed := time.Duration(cancelReconciliationRetryLimit) * cancelReconciliationRetryDelay
	if elapsed < wantMinElapsed {
		t.Fatalf("elapsed = %v, want at least %v (retry backoff was not honored)", elapsed, wantMinElapsed)
	}
}

// TestRunWithCancelReconciliationRetryStopsOnContextCancel proves a canceled
// ctx interrupts the retry backoff instead of waiting out the full delay.
func TestRunWithCancelReconciliationRetryStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	run := func(context.Context) (workflowledger.RunSnapshot, error) {
		calls++
		if calls == 1 {
			cancel()
		}
		return workflowledger.RunSnapshot{}, ErrCancelReconciliationPending
	}
	start := time.Now()
	_, err := RunWithCancelReconciliationRetry(ctx, run)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrCancelReconciliationPending) {
		t.Fatalf("RunWithCancelReconciliationRetry() error = %v, want ErrCancelReconciliationPending", err)
	}
	if calls != 1 {
		t.Fatalf("run called %d times, want exactly 1 (ctx canceled before any retry)", calls)
	}
	if elapsed >= cancelReconciliationRetryDelay {
		t.Fatalf("elapsed = %v, want well under the retry delay %v (ctx cancel should short-circuit the wait)", elapsed, cancelReconciliationRetryDelay)
	}
}
