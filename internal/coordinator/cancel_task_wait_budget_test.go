// cancel_task_wait_budget_test.go covers Root B of the per-task cancel
// contract: CancelTask must not report success, and must not write a terminal
// canceled ledger row, when it cannot show the task actually stopped.
//
// Before the fix, taskCancelWaitBudget expiry fell through to
// finalizeSingleTaskCancel: the ledger read "canceled", a task_canceled
// lifecycle event was appended, and the caller got a nil error - while the
// handler kept running and doing real work.
package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// stubbornRun is the fixture for the wait-budget tests: one task whose
// handler deliberately IGNORES its context and only returns when release is
// closed - the shape of a real tool that does not honour cancellation.
type stubbornRun struct {
	repo    ledger.LedgerRepository
	coord   *coordinator
	h       *RunHandle
	release chan struct{}
	running *atomic.Bool
}

func spawnStubbornRun(t *testing.T) stubbornRun {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})

	started := make(chan struct{})
	release := make(chan struct{})
	var running atomic.Bool
	_ = d.Register(runtime.Subagent, "stubborn", invoker(func(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
		running.Store(true)
		close(started)
		<-release // deliberately not a select on ctx.Done()
		running.Store(false)
		return json.RawMessage(`"done"`), nil
	}))

	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "stubborn"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	return stubbornRun{repo: repo, coord: c.(*coordinator), h: h, release: release, running: &running}
}

// TestCancelTaskWaitBudgetExpiryReportsStillRunning is the Root B
// regression. It takes taskCancelWaitBudget (5s) to run by construction: the
// budget is what is under test.
//
// Before the fix this failed twice over - CancelTask returned nil, and the
// ledger row read "canceled" for a task that was still executing.
func TestCancelTaskWaitBudgetExpiryReportsStillRunning(t *testing.T) {
	fx := spawnStubbornRun(t)
	defer func() {
		close(fx.release)
		_, _ = fx.coord.Join(context.Background(), fx.h)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := fx.coord.CancelTask(ctx, fx.h, "t1")
	if err == nil {
		t.Fatal("CancelTask reported success for a task that never stopped")
	}
	if !errors.Is(err, ErrTaskCancelNotStopped) {
		t.Fatalf("CancelTask error = %v, want one wrapping ErrTaskCancelNotStopped", err)
	}

	if !fx.running.Load() {
		t.Fatal("setup: the handler was expected to still be running when the budget expired")
	}
	snap, snapErr := fx.repo.GetTask(context.Background(), fx.h.runID, "t1")
	if snapErr != nil {
		t.Fatal(snapErr)
	}
	if snap.Status != string(ledger.TaskStatusCancelRequested) {
		t.Fatalf("t1 ledger status = %q, want %q: a task that did not stop must not be recorded terminal",
			snap.Status, ledger.TaskStatusCancelRequested)
	}
}

// TestCancelTaskWaitBudgetExpiryAppendsNoCanceledEvent proves the other half
// of the same contract: no terminal task_canceled lifecycle event is emitted
// for a task that did not stop. A consumer of the event stream must not be
// told the task ended.
func TestCancelTaskWaitBudgetExpiryAppendsNoCanceledEvent(t *testing.T) {
	fx := spawnStubbornRun(t)
	defer func() {
		close(fx.release)
		_, _ = fx.coord.Join(context.Background(), fx.h)
	}()

	var canceledEvents atomic.Int64
	stop := fx.coord.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		if evt.Kind == "task_"+string(ledger.TaskStatusCanceled) && evt.TaskID == "t1" {
			canceledEvents.Add(1)
		}
	})
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := fx.coord.CancelTask(ctx, fx.h, "t1"); !errors.Is(err, ErrTaskCancelNotStopped) {
		t.Fatalf("CancelTask error = %v, want one wrapping ErrTaskCancelNotStopped", err)
	}
	if got := canceledEvents.Load(); got != 0 {
		t.Fatalf("task_canceled events emitted = %d, want 0 for a task that never stopped", got)
	}
}

// TestCancelTaskStoppingTaskStillFinalizes is the counterweight: the success
// path is unchanged. A task that DOES honour its context still gets a nil
// error and a terminal canceled ledger row.
func TestCancelTaskStoppingTaskStillFinalizes(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	_ = d.Register(runtime.Subagent, "polite", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "polite"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started

	coord := c.(*coordinator)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := coord.CancelTask(ctx, h, "t1"); err != nil {
		t.Fatalf("CancelTask on a task that stops: %v", err)
	}
	snap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("t1 ledger status = %q, want canceled on the success path", snap.Status)
	}
	if _, err := coord.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
}
