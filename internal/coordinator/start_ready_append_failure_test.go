package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// startReadyAppendFailingRepo fails AppendEvent only for Kind "task_running".
// run_created and task_created must pass so Spawn works; the failing
// task_running append simulates the transient persistence failure that follows
// a successful queued -> running CAS in transitionTask (coordinator.go). All
// other events are delegated to the embedded repository.
type startReadyAppendFailingRepo struct {
	ledger.LedgerRepository
}

func (r *startReadyAppendFailingRepo) AppendEvent(ctx context.Context, evt ledger.LifecycleEvent) error {
	if evt.Kind == "task_running" {
		return errors.New("simulated transient task_running append failure")
	}
	return r.LedgerRepository.AppendEvent(ctx, evt)
}

// TestStartReadyTaskRunningAppendFailureStillDispatches is the coord-1
// regression test: a transient AppendEvent(task_running) failure after the
// queued -> running CAS succeeds must NOT record the never-executed task as
// failed. The CAS won, so the task is durably running and must still be
// dispatched: the handler runs exactly once, the final ledger status is
// completed (never failed), the result-set status is completed, and the append
// failure is still surfaced on the run error (observability preserved).
//
// Negative path: the injected transient append failure itself exercises the
// recovery branch. The genuine-failure branches (dispatch CAS lost / status not
// running) stay pinned by TestCancelRacingStartReady and
// dag_retry_coverage_test.go, which must remain green.
func TestStartReadyTaskRunningAppendFailureStillDispatches(t *testing.T) {
	repo := &startReadyAppendFailingRepo{LedgerRepository: ledger.NewMemoryLedgerRepository()}
	d := runtime.New(runtime.Policy{})
	var invocations atomic.Int32
	if err := d.Register(runtime.Subagent, "worker", invoker(func(context.Context, runtime.Request) (json.RawMessage, error) {
		invocations.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "worker"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// (1) The task was dispatched despite the append failure: the handler ran
	// exactly once (0 invocations pre-fix, when the task was dropped).
	if got := invocations.Load(); got != 1 {
		t.Fatalf("handler invoked %d times, want 1 (task must be dispatched despite the task_running append failure)", got)
	}
	// (4) Observability preserved: the transient append failure is surfaced on
	// the run error.
	if result.Err == nil || !strings.Contains(result.Err.Error(), "task_running append failure") {
		t.Fatalf("run error = %v, want the task_running append failure surfaced", result.Err)
	}
	// (3) Result-set status is completed, never failed.
	for _, r := range result.Results {
		if r.TaskID == "t1" && r.Status != "completed" {
			t.Fatalf("result status = %q, want completed (a never-executed task must not be terminalized as failed)", r.Status)
		}
	}
	// (2) Final ledger task status is completed, never failed.
	snap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusCompleted) {
		t.Fatalf("ledger task status = %q, want %q", snap.Status, ledger.TaskStatusCompleted)
	}
}
