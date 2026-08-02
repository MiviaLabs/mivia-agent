package coordinator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestCancelDagWithQueuedDependent is a regression test for F4: cancelling a
// DAG run while a dependent task is still queued (not yet dispatched) must not
// join a spurious "invalid state transition" into the run error, and the
// dependent task must surface as canceled on BOTH the run result set and the
// ledger snapshot.
func TestCancelDagWithQueuedDependent(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	_ = d.Register(runtime.Subagent, "slow", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "wave1", Name: "slow"},
		{ID: "wave2", Name: "slow", DependsOn: []string{"wave1"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for wave 1 to start running. wave 2 depends on wave 1, which blocks
	// until the context is canceled, so wave 2 is guaranteed to still be queued
	// (never dispatched) when we cancel.
	<-started

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

	// Clean cancel: the run error must not be polluted by a spurious
	// invalid-state-transition from trying to mark a queued task "completed".
	if result.Err != nil && strings.Contains(result.Err.Error(), "invalid state transition") {
		t.Fatalf("clean cancel polluted run error with invalid transition: %v", result.Err)
	}

	// The dependent task must appear in the run result set as canceled, not
	// "missing" (which recordRunResults would otherwise map to "completed").
	if got := statusForTaskID(result.Results, "wave2"); got != "canceled" {
		t.Fatalf("wave2 result status = %q, want %q", got, "canceled")
	}

	// The ledger snapshot captured at run completion must agree with the
	// result surface.
	if got := statusForSnapshotTask(result.Snapshot, "wave2"); got != string(ledger.TaskStatusCanceled) {
		t.Fatalf("ledger wave2 status = %q, want %q", got, string(ledger.TaskStatusCanceled))
	}

	// Inspect after Cancel returns (post finalize-reconciliation) must also
	// agree.
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusForSnapshotTask(snap, "wave2"); got != string(ledger.TaskStatusCanceled) {
		t.Fatalf("Inspect wave2 status = %q, want %q", got, string(ledger.TaskStatusCanceled))
	}
}

// statusForTaskID returns the status of the first result matching id.
func statusForTaskID(results []subagents.Result, id string) string {
	for _, r := range results {
		if r.TaskID == id {
			return r.Status
		}
	}
	return ""
}

// statusForSnapshotTask returns the status of the first task matching id.
func statusForSnapshotTask(snap ledger.RunSnapshot, id string) string {
	for _, ts := range snap.Tasks {
		if ts.TaskID == id {
			return ts.Status
		}
	}
	return ""
}
