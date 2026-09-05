package coordinator

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// LoadTaskToolCalls is the authoritative source the workflow evidence gate
// reads: the host's own record of what a child agent executed, as opposed to
// the run-message blackboard the child writes itself. Every guard below is
// therefore a security boundary, not a convenience - a wrong answer here
// either credits a command that never ran or rejects one that did.

// traceLoader is the same narrow optional interface the workflow controller
// type-asserts. New returns the Coordinator interface, which deliberately does
// not carry this accessor, so the test reaches it the way its real caller
// does rather than by widening the exported surface.
type traceLoader interface {
	LoadTaskToolCalls(ctx context.Context, runID, taskID string) ([]subagents.ToolCallStep, error)
}

func mustTraceLoader(t *testing.T, c Coordinator) traceLoader {
	t.Helper()
	loader, ok := c.(traceLoader)
	if !ok {
		t.Fatal("the coordinator does not expose LoadTaskToolCalls; the workflow evidence gate resolves it by exactly this assertion")
	}
	return loader
}

// TestLoadTaskToolCallsReturnsRecordedSteps drives the real dispatch path:
// a handler emits steps through the sink the coordinator installs on the task
// context, and the accessor must resolve them back.
func TestLoadTaskToolCallsReturnsRecordedSteps(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "toolcaller", toolCallEmittingHandler{toolName: "run_command"}); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "toolcaller"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	steps, err := mustTraceLoader(t, c).LoadTaskToolCalls(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatalf("LoadTaskToolCalls: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want the recorded start and end: %+v", len(steps), steps)
	}
	if steps[0].Kind != "start" || steps[0].Name != "run_command" {
		t.Fatalf("steps[0] = %+v, want a run_command start", steps[0])
	}
	if steps[1].Kind != "end" || steps[1].ToolCallID != steps[0].ToolCallID {
		t.Fatalf("steps[1] = %+v, want the matching end for %q", steps[1], steps[0].ToolCallID)
	}
}

// TestLoadTaskToolCallsRequiresBothIdentifiers pins the argument guard. A
// blank run or task id cannot address a trace, and answering "no executions"
// for one would silently hand the evidence gate an empty history - which reads
// as "nothing was proven" and fails a truthful report.
func TestLoadTaskToolCallsRequiresBothIdentifiers(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	cases := []struct{ runID, taskID string }{
		{"", "t1"},
		{"run-1", ""},
		{"", ""},
	}
	for _, tc := range cases {
		steps, err := mustTraceLoader(t, c).LoadTaskToolCalls(context.Background(), tc.runID, tc.taskID)
		if err == nil {
			t.Errorf("LoadTaskToolCalls(%q, %q) = %v, nil; want an error", tc.runID, tc.taskID, steps)
		}
		if steps != nil {
			t.Errorf("LoadTaskToolCalls(%q, %q) returned steps alongside its error", tc.runID, tc.taskID)
		}
	}
}

// TestLoadTaskToolCallsUnknownTaskIsEmptyNotAnError separates "this task made
// no recorded calls" from "the lookup broke". An unknown task is the former:
// the gate must treat it as no proven executions and fail the claim closed,
// rather than erroring the whole step.
func TestLoadTaskToolCallsUnknownTaskIsEmptyNotAnError(t *testing.T) {
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	steps, err := mustTraceLoader(t, c).LoadTaskToolCalls(context.Background(), "run-absent", "task-absent")
	if err != nil {
		t.Fatalf("LoadTaskToolCalls on an unknown task = %v, want no error", err)
	}
	if len(steps) != 0 {
		t.Fatalf("steps = %+v, want none", steps)
	}
}

// TestLoadTaskToolCallsWithoutTraceIsEmpty covers a task that completed before
// it made any tool call, or before the trace field existed: no ref, no steps,
// no error.
func TestLoadTaskToolCallsWithoutTraceIsEmpty(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "quiet", quietHandler{}); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "quiet"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	snap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.ToolCallsRef != "" {
		t.Fatalf("fixture recorded a trace (%q); it must record none for this test to mean anything", snap.ToolCallsRef)
	}

	steps, err := mustTraceLoader(t, c).LoadTaskToolCalls(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatalf("LoadTaskToolCalls with no recorded trace = %v, want no error", err)
	}
	if len(steps) != 0 {
		t.Fatalf("steps = %+v, want none", steps)
	}
}

// TestLoadTaskToolCallsCorruptTraceIsAnError is the one shape that must NOT be
// swallowed. Unreadable bytes at a recorded ref mean the record is damaged;
// reporting "no executions" there would quietly turn a corrupt trace into a
// failed evidence claim, hiding the real fault.
func TestLoadTaskToolCallsCorruptTraceIsAnError(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "toolcaller", toolCallEmittingHandler{toolName: "run_command"}); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "toolcaller"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	snap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.StoreContent(context.Background(), snap.ToolCallsRef, []byte("{not json")); err != nil {
		t.Fatalf("overwrite trace content: %v", err)
	}

	steps, err := mustTraceLoader(t, c).LoadTaskToolCalls(context.Background(), h.runID, "t1")
	if err == nil {
		t.Fatalf("LoadTaskToolCalls on a corrupt trace = %v, nil; want a decode error", steps)
	}
	if !strings.Contains(err.Error(), "t1") {
		t.Errorf("error %q should name the task whose trace failed to decode", err)
	}
}

// TestLoadTaskToolCallsMissingTraceContentIsEmpty covers a RECORDED ref whose
// content is gone - a trace the ledger still points at but no longer holds,
// which content GC or a partial restore can produce.
//
// It must read as "no proven executions" (empty, no error), exactly like a task
// that never recorded a trace. The distinction matters: a not-found ref is a
// missing record, while any OTHER load failure is a broken store and must
// surface. Both arms sit on one equality test, so each needs its own case.
func TestLoadTaskToolCallsMissingTraceContentIsEmpty(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "toolcaller", toolCallEmittingHandler{toolName: "run_command"}); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 1}))

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "toolcaller"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	// Point the task at a trace ref nothing ever stored.
	dangling := "ref:toolcalls:" + strings.Repeat("a", 64)
	if err := repo.SetTaskOutput(context.Background(), h.runID, "t1", "", "", dangling); err != nil {
		t.Fatalf("SetTaskOutput with a dangling trace ref: %v", err)
	}
	snap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.ToolCallsRef == "" {
		t.Fatal("fixture cleared the trace ref; it must stay set for this test to mean anything")
	}

	steps, err := mustTraceLoader(t, c).LoadTaskToolCalls(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatalf("LoadTaskToolCalls on a missing trace = %v, want it read as no executions", err)
	}
	if len(steps) != 0 {
		t.Fatalf("steps = %+v, want none", steps)
	}
}
