// tool_cancel_guard_test.go isolates each operand of the three guard
// clauses in tool_cancel.go (registerSubagentToolCanceler,
// RegisterSubagentToolCanceler, CancelSubagentToolCall), one at a time,
// beyond what cancel_subagent_tool_call_test.go's end-to-end isolation
// test already proves. Mirrors internal/uiadapter's
// turnhandle_cancel_tool_call_internal_test.go: store a nil func value
// directly to prove the nil-canceler branch, not just the
// never-registered branch.
package coordinator

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func validTestCanceler(string) bool { return true }

// TestRegisterSubagentToolCanceler_NilHandleReceiverIsNoop isolates the
// `h == nil` leg of registerSubagentToolCanceler's guard: a nil *RunHandle
// with a non-empty taskID and a non-nil canceler must not panic.
func TestRegisterSubagentToolCanceler_NilHandleReceiverIsNoop(t *testing.T) {
	var h *RunHandle
	h.registerSubagentToolCanceler("task-1", agent.ToolCanceler(validTestCanceler)) // must not panic
}

// TestRegisterSubagentToolCanceler_EmptyTaskIDIsNoop isolates the
// `taskID == ""` leg: a non-nil handle and non-nil canceler, but a blank
// taskID, must leave the handle's canceler map untouched.
func TestRegisterSubagentToolCanceler_EmptyTaskIDIsNoop(t *testing.T) {
	h := &RunHandle{}
	h.registerSubagentToolCanceler("", agent.ToolCanceler(validTestCanceler))
	if n := len(h.subagentToolCancelers); n != 0 {
		t.Fatalf("subagentToolCancelers has %d entries after an empty-taskID call, want 0", n)
	}
}

// TestRegisterSubagentToolCanceler_NilCancelerIsNoop isolates the
// `canceler == nil` leg: a non-nil handle and non-empty taskID, but a nil
// canceler, must leave the handle's canceler map untouched.
func TestRegisterSubagentToolCanceler_NilCancelerIsNoop(t *testing.T) {
	h := &RunHandle{}
	h.registerSubagentToolCanceler("task-1", nil)
	if n := len(h.subagentToolCancelers); n != 0 {
		t.Fatalf("subagentToolCancelers has %d entries after a nil-canceler call, want 0", n)
	}
}

// TestRegisterSubagentToolCanceler_ValidInputsRegisters is the positive
// control: with all three operands satisfied, the canceler actually lands
// in the map, proving the three no-op tests above isolate real guard
// branches rather than a registerSubagentToolCanceler that never does
// anything.
func TestRegisterSubagentToolCanceler_ValidInputsRegisters(t *testing.T) {
	h := &RunHandle{}
	h.registerSubagentToolCanceler("task-1", agent.ToolCanceler(validTestCanceler))
	got, ok := h.subagentToolCanceler("task-1")
	if !ok {
		t.Fatal("subagentToolCanceler(\"task-1\") ok = false, want true after a valid registration")
	}
	if got == nil {
		t.Fatal("subagentToolCanceler(\"task-1\") returned a nil canceler, want the one registered")
	}
}

// TestCoordinatorRegisterSubagentToolCanceler_NilReceiverIsNoop isolates
// the `c == nil` leg of (*coordinator).RegisterSubagentToolCanceler's
// guard: calling it on a nil *coordinator must not panic.
func TestCoordinatorRegisterSubagentToolCanceler_NilReceiverIsNoop(t *testing.T) {
	var c *coordinator
	c.RegisterSubagentToolCanceler("run-1", "task-1", agent.ToolCanceler(validTestCanceler)) // must not panic
}

// TestCoordinatorRegisterSubagentToolCanceler_EmptyRunIDIsNoop isolates
// the `runID == ""` leg. HandleForRun("") already returns nil on its own
// (its own internal guard), so a blank runID with an ordinary
// handlesByRun map would behave identically whether this guard's `||` is
// mutated to `&&` or not - both paths end in "nothing registered". To make
// the two branches actually diverge, this test seeds handlesByRun[""]
// with a live handle directly (a shape no production caller can produce -
// Spawn/EnsureRun never key a run by the empty string): with the guard
// intact, the blank-runID check short-circuits before HandleForRun is
// ever consulted, so that seeded handle is never touched; under the `&&`
// mutant, the guard would fall through, HandleForRun("") would return the
// seeded handle, and the registration would land in it instead of being
// dropped.
func TestCoordinatorRegisterSubagentToolCanceler_EmptyRunIDIsNoop(t *testing.T) {
	c := &coordinator{handlesByRun: map[string]*RunHandle{}}
	seeded := &RunHandle{runID: "", owner: c}
	c.handlesByRun[""] = seeded

	c.RegisterSubagentToolCanceler("", "task-1", agent.ToolCanceler(validTestCanceler))

	if n := len(seeded.subagentToolCancelers); n != 0 {
		t.Fatalf("seeded handle has %d registered cancelers after an empty-runID call, want 0 (guard must short-circuit before HandleForRun)", n)
	}
}

// TestCoordinatorRegisterSubagentToolCanceler_ValidInputsRegisters is the
// positive control for the coordinator-level guard: a non-nil coordinator
// and a non-empty runID that resolves to a live handle actually registers
// the canceler on that handle.
func TestCoordinatorRegisterSubagentToolCanceler_ValidInputsRegisters(t *testing.T) {
	c := &coordinator{handlesByRun: map[string]*RunHandle{}}
	h := &RunHandle{runID: "run-1", owner: c}
	c.handlesByRun["run-1"] = h

	c.RegisterSubagentToolCanceler("run-1", "task-1", agent.ToolCanceler(validTestCanceler))

	got, ok := h.subagentToolCanceler("task-1")
	if !ok || got == nil {
		t.Fatalf("handle for run-1 has no registered canceler for task-1 after a valid call: ok=%v got=%v", ok, got)
	}
}

// TestCancelSubagentToolCall_RegisteredNilCancelerIsSafeNoop isolates the
// `canceler == nil` leg of CancelSubagentToolCall's guard, distinct from
// "never registered at all" (already covered by
// TestCancelSubagentToolCall_UnknownIsSafeNoop's unknown-call-ID case):
// this stores a nil agent.ToolCanceler value directly under a known
// taskID/callID pairing (bypassing registerSubagentToolCanceler, which
// itself refuses to store a nil canceler), so `ok` is true but the stored
// value is nil. Mirrors internal/uiadapter's
// TestTurnHandleCancelToolCall_StoredNilCancelerIsNoop.
func TestCancelSubagentToolCall_RegisteredNilCancelerIsSafeNoop(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	pool := subagents.New(nil, subagents.Policy{Workers: 1})
	c := New(repo, pool).(*coordinator)

	h := &RunHandle{runID: "run-1", owner: c, done: make(chan struct{}), cancelDone: make(chan struct{})}
	h.subagentToolCancelMu.Lock()
	h.subagentToolCancelers = map[string]agent.ToolCanceler{"task-1": nil}
	h.subagentToolCancelMu.Unlock()

	ok, err := c.CancelSubagentToolCall(context.Background(), h, "task-1", "call-1")
	if err != nil {
		t.Fatalf("CancelSubagentToolCall with a registered-but-nil canceler: got error %v, want nil", err)
	}
	if ok {
		t.Fatal("CancelSubagentToolCall with a registered-but-nil canceler returned true, want false")
	}
}
