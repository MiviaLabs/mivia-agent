// dag_wave_test.go is the regression for a production incident: startReady
// (dag.go) CASed EVERY ready task queued -> running before pool.Run
// dispatched them, so a batch bigger than the pool's worker capacity showed
// MORE tasks "running" in the ledger (and to inspect_agents/the TUI) than
// the pool would ever actually execute concurrently. The excess tasks sat
// queued inside the pool's own internal channel, falsely reporting
// "running" with zero progress until a worker freed up - sometimes minutes
// later, well past the point an operator would judge the task stalled.
package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestDAGRespectsWorkerCapacityForRunningStatus dispatches 6 independent
// tasks against a 3-worker pool and asserts the ledger never records more
// tasks "running" than the pool can actually execute concurrently.
func TestDAGRespectsWorkerCapacityForRunningStatus(t *testing.T) {
	const workers = 3
	const taskCount = 6

	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})

	release := make(chan struct{})
	entered := make(chan string, taskCount)
	_ = d.Register(runtime.Subagent, "block", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		id, _ := runtime.TaskIdentityFrom(ctx)
		entered <- id.TaskID
		<-release
		return json.RawMessage(`"done"`), nil
	}))

	p := subagents.New(d, subagents.Policy{Workers: workers})
	c := New(repo, p)

	tasks := make([]subagents.Task, taskCount)
	for i := range tasks {
		tasks[i] = subagents.Task{ID: fmt.Sprintf("t%d", i), Name: "block"}
	}
	h, err := c.Spawn(context.Background(), tasks, "")
	if err != nil {
		t.Fatal(err)
	}

	// Wait until exactly `workers` tasks have entered the handler - the
	// pool's own concurrency ceiling.
	for i := 0; i < workers; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for workers to enter the handler")
		}
	}
	// Give any excess (bugged) dispatch a moment to happen and settle.
	time.Sleep(200 * time.Millisecond)

	running := 0
	for i := range tasks {
		snap, err := repo.GetTask(context.Background(), h.runID, tasks[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Status == string(ledger.TaskStatusRunning) {
			running++
		}
	}
	if running > workers {
		t.Fatalf("ledger shows %d tasks running, want at most %d (the pool's worker capacity); "+
			"the DAG dispatched a batch larger than the pool can execute concurrently",
			running, workers)
	}

	close(release)
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
}

// TestDAGWaveDoesNotStarveRemainingReadyTasks proves the fix does not lose
// tasks: with capacity below the ready count, every task still eventually
// runs and completes across successive waves.
func TestDAGWaveDoesNotStarveRemainingReadyTasks(t *testing.T) {
	const workers = 2
	const taskCount = 5

	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "instant", invoker(func(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`"done"`), nil
	}))

	p := subagents.New(d, subagents.Policy{Workers: workers})
	c := New(repo, p)

	tasks := make([]subagents.Task, taskCount)
	for i := range tasks {
		tasks[i] = subagents.Task{ID: fmt.Sprintf("t%d", i), Name: "instant"}
	}
	h, err := c.Spawn(context.Background(), tasks, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result.Err != nil {
		t.Fatalf("run completed with error: %v", result.Err)
	}
	if len(result.Results) != taskCount {
		t.Fatalf("got %d results, want %d", len(result.Results), taskCount)
	}
	for _, r := range result.Results {
		if r.Status != "completed" {
			t.Errorf("task %q status = %q, want completed", r.TaskID, r.Status)
		}
	}
}

// TestCapReadyToPoolCapacityWithNilPoolAppliesNoCap pins the nil-pool guard
// of capReadyToPoolCapacity. A cancel-only coordinator carries a nil pool
// (see cancel_nil_pool_test.go), so the wave cap must return the ready slice
// untouched instead of dereferencing the pool for its worker count.
func TestCapReadyToPoolCapacityWithNilPoolAppliesNoCap(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), nil)
	ready := []subagents.Task{{ID: "t0", Name: "n"}, {ID: "t1", Name: "n"}, {ID: "t2", Name: "n"}}

	got := c.capReadyToPoolCapacity(ready)
	if len(got) != len(ready) {
		t.Fatalf("capped ready = %d tasks, want all %d uncapped with a nil pool", len(got), len(ready))
	}
	for i := range got {
		if got[i].ID != ready[i].ID {
			t.Fatalf("task %d = %q, want %q (order must be preserved)", i, got[i].ID, ready[i].ID)
		}
	}
}
