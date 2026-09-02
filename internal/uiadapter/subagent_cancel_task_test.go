package uiadapter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

type invoker func(ctx context.Context, req runtime.Request) (json.RawMessage, error)

func (f invoker) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	return f(ctx, req)
}

// newTestCoordinatorRun spawns a real coordinator run with one task that
// blocks on ctx.Done(), returning the coordinator, the run handle, and the
// task's own ID. A real coordinator.Coordinator (in-memory ledger, no test
// double for the interface) is used because uiadapter has no existing
// coordinator-faking convention of its own (there is nothing else in this
// package to follow) and the interface is large enough that a hand-written
// fake would drift from its real contract.
func newTestCoordinatorRun(t *testing.T) (coordinator.Coordinator, *coordinator.RunHandle, string, <-chan struct{}) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	if err := d.Register(runtime.Subagent, "slow", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})); err != nil {
		t.Fatal(err)
	}
	pool := subagents.New(d, subagents.Policy{Workers: 1})
	c := coordinator.New(repo, pool)

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "slow"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	return c, h, "t1", started
}

// TestSubagentThreads_CancelSubagentTask_ForwardsToCoordinator proves
// CancelSubagentTask resolves a registered callID to its coordinator
// run/task identity and forwards to coordinator.Coordinator.CancelTask,
// leaving the task canceled in the ledger.
func TestSubagentThreads_CancelSubagentTask_ForwardsToCoordinator(t *testing.T) {
	c, h, taskID, started := newTestCoordinatorRun(t)
	<-started

	threads := NewSubagentThreads()
	threads.RegisterTaskRoute(c, "call-1", h.RunID(), taskID)

	ok, err := threads.CancelSubagentTask("call-1")
	if err != nil {
		t.Fatalf("CancelSubagentTask: %v", err)
	}
	if !ok {
		t.Fatal("CancelSubagentTask reported ok=false for a registered route")
	}

	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	var status string
	for _, ts := range snap.Tasks {
		if ts.TaskID == taskID {
			status = ts.Status
		}
	}
	if status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("task status = %q, want %q", status, ledger.TaskStatusCanceled)
	}
}

// TestSubagentThreads_CancelSubagentTask_UnregisteredCallIDIsMiss proves an
// unregistered callID (no route ever recorded — e.g. a reconstruction from
// persisted history, which carries no live coordinator identity) is a clean
// no-op, not an error.
func TestSubagentThreads_CancelSubagentTask_UnregisteredCallIDIsMiss(t *testing.T) {
	threads := NewSubagentThreads()
	ok, err := threads.CancelSubagentTask("never-registered")
	if err != nil {
		t.Fatalf("expected no error for an unregistered callID, got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for an unregistered callID")
	}
}

// TestSubagentThreads_CancelSubagentTask_NoCoordinatorErrors proves a
// registered route whose recorded coordinator is nil reports a clear
// error instead of silently no-oping.
func TestSubagentThreads_CancelSubagentTask_NoCoordinatorErrors(t *testing.T) {
	threads := NewSubagentThreads()
	threads.RegisterTaskRoute(nil, "call-1", "run-x", "task-x")
	_, err := threads.CancelSubagentTask("call-1")
	if err == nil {
		t.Fatal("expected an error when no coordinator is wired")
	}
}

// TestSubagentThreads_RegisterTaskRoute_BlankFieldsAreRejected isolates each
// of RegisterTaskRoute's three blank-field guards: a call
// with any one of callID/runID/taskID empty must record no route at all, so
// a later CancelSubagentTask on that callID reports ok=false rather than
// resolving to a partially-blank route. A coordinator is deliberately never
// wired here: if a route were wrongly registered, CancelSubagentTask would
// fail with "no coordinator wired" (ok=false, err!=nil) instead of the
// clean ok=false/err=nil a genuine no-route miss produces, so the error
// check below is as load-bearing as the ok check.
func TestSubagentThreads_RegisterTaskRoute_BlankFieldsAreRejected(t *testing.T) {
	cases := []struct {
		name             string
		callID, run, tsk string
	}{
		{"blank callID", "", "run-x", "task-x"},
		{"blank runID", "call-1", "", "task-x"},
		{"blank taskID", "call-1", "run-x", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			threads := NewSubagentThreads()
			threads.RegisterTaskRoute(nil, tc.callID, tc.run, tc.tsk)

			lookupID := tc.callID
			if lookupID == "" {
				lookupID = "call-1" // the same id a non-blank-callID call would have used
			}
			ok, err := threads.CancelSubagentTask(lookupID)
			if err != nil {
				t.Fatalf("%s: CancelSubagentTask returned an error, want a clean no-route miss: %v", tc.name, err)
			}
			if ok {
				t.Fatalf("%s: CancelSubagentTask reported ok=true; RegisterTaskRoute must not have recorded a route with a blank field", tc.name)
			}
		})
	}
}
