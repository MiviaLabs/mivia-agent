package coordinator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestCancelDuringRetryBackoff is the D1 regression test: cancelling a run
// while a task is sitting in the retry queue (retry_pending in the ledger,
// waiting out its backoff) must not:
//   - leave the ledger task stuck in retry_pending forever, nor
//   - pollute the run error with an "invalid state transition" from a
//     retry_pending -> failed CAS that the ledger forbids.
//
// The task must surface as canceled on BOTH the run result set and the ledger
// snapshot, and the run must be terminal.
func TestCancelDuringRetryBackoff(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "alwaysfail", staticHandler{err: errors.New("always fail")})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	// A long backoff keeps the task in retry_pending for a comfortable window
	// so we can reliably observe it and cancel during the retry backoff.
	c := New(repo, p).WithRetryPolicy(RetryPolicy{
		MaxRetries:     3,
		BaseBackoff:    30 * time.Second,
		MaxBackoff:     30 * time.Second,
		BackoffFactor:  2.0,
		JitterFraction: 0,
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "alwaysfail"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// Wait until the task has entered retry_pending (sitting in the retry queue).
	waitForTaskStatus(t, c, h, string(ledger.TaskStatusRetryPending))

	cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Cancel(cancelCtx, h); err != nil {
		t.Fatal(err)
	}

	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Clean cancel: the run error must not be polluted by an
	// invalid-state-transition from a retry_pending -> failed CAS.
	if result.Err != nil && strings.Contains(result.Err.Error(), "invalid state transition") {
		t.Fatalf("cancel during retry backoff polluted run error: %v", result.Err)
	}

	// The task must surface as canceled in the result set.
	if got := statusForTaskID(result.Results, "t1"); got != "canceled" {
		t.Fatalf("task result status = %q, want %q", got, "canceled")
	}

	// The ledger snapshot captured at run completion must agree: task canceled,
	// not stuck in retry_pending.
	if len(result.Snapshot.Tasks) != 1 || statusForSnapshotTask(result.Snapshot, "t1") != string(ledger.TaskStatusCanceled) {
		t.Fatalf("ledger task status = %+v, want canceled", result.Snapshot.Tasks)
	}

	// The run must be terminal (canceled here, since the only task was canceled).
	if result.Snapshot.Status != ledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want %q", result.Snapshot.Status, ledger.RunStatusCanceled)
	}
}

// waitForTaskStatus polls Inspect until a single task reaches status.
func waitForTaskStatus(t *testing.T, c Coordinator, h *RunHandle, status string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, err := c.Inspect(context.Background(), h)
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Tasks) == 1 && snap.Tasks[0].Status == status {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; snapshot = %+v", status, snap)
		}
		select {
		case <-time.After(5 * time.Millisecond):
		}
	}
}
