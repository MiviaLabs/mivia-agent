package coordinator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestCoordinator_PermanentFailureDoesNotRetry is the RED test for the
// transient-error gate: shouldRetryTask must not spend retry budget on a
// task whose failure is not classified transient (provider.IsTransient),
// even when the run's RetryPolicy allows retries. A permanent failure -
// bad auth, a schema violation, a genuine bug in the task - fails exactly
// the same way on every attempt, so retrying it only delays the terminal
// result the caller could have had immediately.
func TestCoordinator_PermanentFailureDoesNotRetry(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	fixedTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return fixedTime })
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "permanent", staticHandler{err: errors.New("permission denied")})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p).WithRetryPolicy(RetryPolicy{
		MaxRetries:     3,
		BaseBackoff:    1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
		BackoffFactor:  2.0,
		JitterFraction: 0,
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "permanent"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	events, err := repo.ListEvents(context.Background(), h.runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, evt := range events {
		if evt.Kind == "task_retry_pending" {
			t.Fatalf("got a task_retry_pending event for a permanent (non-transient) failure, want zero retries")
		}
	}

	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Tasks[0].Status != string(ledger.TaskStatusFailed) {
		t.Fatalf("task status = %q, want %q", snap.Tasks[0].Status, ledger.TaskStatusFailed)
	}
}
