// cancel_task_dispatch_window_test.go covers the dispatch window a per-task
// cancel used to fall straight through: startReady (dag.go) CASes EVERY ready
// task queued -> running before pool.Run, but a worker only reaches a task
// later. In that window the task has no registered CancelFunc, so CancelTask
// took its "still queued, the DAG will never dispatch it" path, wrote a
// terminal canceled row, and reported success - while the worker went on to
// run the handler to completion, doing real work whose output was then
// silently discarded at run end.
//
// The fence is Pool.ShouldSkipTask -> coordinator.shouldSkipCanceledTask
// (task_start.go), consulted on the worker goroutine immediately before the
// handler would be invoked.
package coordinator

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// dispatchWindowRun is the fixture for the tests below: two identical tasks
// and a single worker, so exactly one task executes and the other waits
// inside the dispatched batch with its ledger row already at running.
type dispatchWindowRun struct {
	repo    ledger.LedgerRepository
	coord   *coordinator
	h       *RunHandle
	release chan struct{}
	mu      *sync.Mutex
	entered *[]string
}

// executed returns the task IDs whose handler actually ran, in entry order.
func (r dispatchWindowRun) executed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(*r.entered))
	copy(out, *r.entered)
	return out
}

// spawnDispatchWindowRun starts tasks "t1" and "t2" against ONE worker. Both
// share a handler that reports its own task ID (read from the stamped task
// identity) the moment it is entered, then blocks until release is closed.
// It returns once the first task is inside the handler; the other task is
// then durably running in the ledger but has never reached a worker.
func spawnDispatchWindowRun(t *testing.T) (dispatchWindowRun, string) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})

	var mu sync.Mutex
	entered := []string{}
	started := make(chan string, 2)
	release := make(chan struct{})
	_ = d.Register(runtime.Subagent, "work", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		id, _ := runtime.TaskIdentityFrom(ctx)
		mu.Lock()
		entered = append(entered, id.TaskID)
		mu.Unlock()
		started <- id.TaskID
		<-release
		return json.RawMessage(`"done"`), nil
	}))

	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "work"},
		{ID: "t2", Name: "work"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	first := <-started
	return dispatchWindowRun{
		repo: repo, coord: c.(*coordinator), h: h, release: release,
		mu: &mu, entered: &entered,
	}, first
}

// otherTaskID returns the task of the pair that is not id.
func otherTaskID(id string) string {
	if id == "t1" {
		return "t2"
	}
	return "t1"
}

// TestCancelTaskInDispatchWindowNeverRunsHandler is the Root A regression:
// a task canceled while it sits inside a dispatched batch, waiting for a
// worker, must never execute its handler and must settle as canceled.
//
// Before the fence this test failed on the side-effect assertion: CancelTask
// returned nil, the ledger read "canceled", and the victim's handler ran to
// completion anyway.
func TestCancelTaskInDispatchWindowNeverRunsHandler(t *testing.T) {
	fx, first := spawnDispatchWindowRun(t)
	victim := otherTaskID(first)

	// The victim really is in the dispatch window: durably running, with no
	// per-task CancelFunc for CancelTask to invoke.
	snap, err := fx.repo.GetTask(context.Background(), fx.h.runID, victim)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusRunning) {
		t.Fatalf("setup: victim %q status = %q, want running (inside the dispatch window)", victim, snap.Status)
	}
	if _, _, ok := fx.h.taskCancelFunc(victim); ok {
		t.Fatalf("setup: victim %q already has a registered CancelFunc; this is not the dispatch window", victim)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := fx.coord.CancelTask(ctx, fx.h, victim); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	close(fx.release)
	result, err := fx.coord.Join(context.Background(), fx.h)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range fx.executed() {
		if id == victim {
			t.Fatalf("victim %q executed its handler after a successful CancelTask; executed = %v", victim, fx.executed())
		}
	}
	if got := statusForTaskID(result.Results, victim); got != "canceled" {
		t.Fatalf("victim %q result status = %q, want canceled", victim, got)
	}
	if got := statusForTaskID(result.Results, first); got != "completed" {
		t.Fatalf("sibling %q result status = %q, want completed", first, got)
	}
}

// TestCancelTaskInDispatchWindowLeavesLedgerCanceled proves the fenced task
// is durably terminal as canceled, not left stranded at cancel_requested by
// the skip path.
func TestCancelTaskInDispatchWindowLeavesLedgerCanceled(t *testing.T) {
	fx, first := spawnDispatchWindowRun(t)
	victim := otherTaskID(first)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := fx.coord.CancelTask(ctx, fx.h, victim); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	close(fx.release)
	if _, err := fx.coord.Join(context.Background(), fx.h); err != nil {
		t.Fatal(err)
	}

	snap, err := fx.repo.GetTask(context.Background(), fx.h.runID, victim)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("victim %q ledger status = %q, want canceled", victim, snap.Status)
	}
}

// TestUncanceledTaskInDispatchWindowStillRuns is the counterweight: the
// fence must only ever skip a task the ledger shows as claimed for
// cancellation. With no cancel at all, BOTH tasks run.
func TestUncanceledTaskInDispatchWindowStillRuns(t *testing.T) {
	fx, _ := spawnDispatchWindowRun(t)
	close(fx.release)
	result, err := fx.coord.Join(context.Background(), fx.h)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(fx.executed()); got != 2 {
		t.Fatalf("executed %d handlers (%v), want both tasks to run", got, fx.executed())
	}
	for _, id := range []string{"t1", "t2"} {
		if got := statusForTaskID(result.Results, id); got != "completed" {
			t.Fatalf("task %q result status = %q, want completed", id, got)
		}
	}
}
