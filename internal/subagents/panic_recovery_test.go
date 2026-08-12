package subagents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// TestPoolRecoversPanickingTaskWithoutCrashingSiblings is the RED test for
// worker-pool panic recovery: a handler that panics must surface as a
// failed Result for its own task, and must not prevent sibling tasks in the
// same batch from completing (a goroutine-level recover would end the
// worker's loop early and could starve siblings still queued behind it).
func TestPoolRecoversPanickingTaskWithoutCrashingSiblings(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "boom", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		panic("simulated handler panic")
	}))
	_ = d.Register(runtime.Subagent, "ok", h{})

	p := New(d, Policy{Workers: 1})
	got, err := p.Run(context.Background(), []Task{
		{ID: "panicking", Name: "boom"},
		{ID: "fine", Name: "ok"},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(got), got)
	}
	var panicResult, okResult *Result
	for i := range got {
		switch got[i].TaskID {
		case "panicking":
			panicResult = &got[i]
		case "fine":
			okResult = &got[i]
		}
	}
	if panicResult == nil || okResult == nil {
		t.Fatalf("missing expected task results: %+v", got)
	}
	if panicResult.Status != "failed" {
		t.Fatalf("panicking task Status = %q, want %q", panicResult.Status, "failed")
	}
	if panicResult.Err == nil || !strings.Contains(panicResult.Err.Error(), "simulated handler panic") {
		t.Fatalf("panicking task Err = %v, want it to mention the panic value", panicResult.Err)
	}
	if okResult.Status != "completed" {
		t.Fatalf("sibling task Status = %q, want %q (a panic must not take down the batch)", okResult.Status, "completed")
	}
}

// TestPoolOnTaskDonePanicDoesNotCorruptSuccessfulResult is the RED test for a
// bug found in bug-audit review: safeExecuteOne's recover originally spanned
// the OnTaskDone callback too, so a panic inside OnTaskDone (coordinator
// finalization logic - ledger CAS, mailbox termination) silently overwrote an
// already-successful Result with a synthetic "failed" one, violating
// OnTaskDone's documented contract that it "must not change the result
// returned to the caller".
func TestPoolOnTaskDonePanicDoesNotCorruptSuccessfulResult(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "ok", h{})

	p := New(d, Policy{Workers: 1})
	p.OnTaskDone = func(context.Context, Task, Result) {
		panic("simulated OnTaskDone panic")
	}
	got, err := p.Run(context.Background(), []Task{{ID: "t1", Name: "ok"}})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(got), got)
	}
	if got[0].Status != "completed" {
		t.Fatalf("Status = %q, want %q - an OnTaskDone panic must not corrupt the already-computed result", got[0].Status, "completed")
	}
	if got[0].Err != nil {
		t.Fatalf("Err = %v, want nil - an OnTaskDone panic must not corrupt the already-computed result", got[0].Err)
	}
}

// TestPoolRecoversPanicInContextForTask is a bug-audit coverage gap
// (test-coverage lens): safeExecuteOne's recover wraps the whole of
// executeOne, but the only panic sources previously exercised were inside
// the dispatched handler and inside OnTaskDone. ContextForTask runs earlier
// in executeOne, before the dispatcher is ever invoked - this proves the
// same recovery net catches a panic there too, rather than that code path
// being accidentally uncovered dead-code-for-panics.
func TestPoolRecoversPanicInContextForTask(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "ok", h{})

	p := New(d, Policy{Workers: 1})
	p.ContextForTask = func(context.Context, string) context.Context {
		panic("simulated ContextForTask panic")
	}
	got, err := p.Run(context.Background(), []Task{{ID: "t1", Name: "ok"}})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(got), got)
	}
	if got[0].Status != "failed" {
		t.Fatalf("Status = %q, want %q", got[0].Status, "failed")
	}
	if got[0].Err == nil || !strings.Contains(got[0].Err.Error(), "simulated ContextForTask panic") {
		t.Fatalf("Err = %v, want it to mention the panic value", got[0].Err)
	}
}
