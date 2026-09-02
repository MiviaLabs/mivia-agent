package uiadapter

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

// TestSubagentThreads_CancelSubagentToolCall_ForwardsToCoordinator proves
// CancelSubagentToolCall resolves a registered callID to its coordinator
// run/task identity and forwards to
// coordinator.Coordinator.CancelSubagentToolCall, invoking the exact
// ToolCanceler the task registered for that (runID, taskID) pair - and NOT
// canceling the task itself: the ledger status stays untouched by this
// call. The task's own OnToolCancelReady hook firing end to end (against a
// real SDK-backed subagents.MultiStepHandler loop) is covered by
// internal/coordinator's TestCancelSubagentToolCall_* tests; this test's
// job is the uiadapter routing layer alone, so it seeds the coordinator's
// registry directly via RegisterSubagentToolCanceler rather than re-running
// a full nested loop.
func TestSubagentThreads_CancelSubagentToolCall_ForwardsToCoordinator(t *testing.T) {
	c, h, taskID, started := newTestCoordinatorRun(t)
	<-started

	var gotCallID string
	c.RegisterSubagentToolCanceler(h.RunID(), taskID, func(callID string) bool {
		gotCallID = callID
		return callID == "tool-call-1"
	})

	threads := NewSubagentThreads()
	threads.RegisterTaskRoute(c, "call-1", h.RunID(), taskID)

	ok, err := threads.CancelSubagentToolCall("call-1", "tool-call-1")
	if err != nil {
		t.Fatalf("CancelSubagentToolCall: %v", err)
	}
	if !ok {
		t.Fatal("CancelSubagentToolCall reported ok=false for a registered canceler")
	}
	if gotCallID != "tool-call-1" {
		t.Fatalf("ToolCanceler invoked with callID = %q, want %q", gotCallID, "tool-call-1")
	}

	// The task itself must be untouched: still running, not canceled.
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range snap.Tasks {
		if ts.TaskID == taskID && ts.Status != "running" {
			t.Fatalf("task status = %q, want running (CancelSubagentToolCall must not touch task lifecycle)", ts.Status)
		}
	}

	// Clean up the still-blocked task.
	_ = c.Cancel(context.Background(), h)
}

// TestSubagentThreads_CancelSubagentToolCall_UnknownCallIDIsMiss proves the
// registered ToolCanceler's own false return (an unknown/already-finished
// tool call ID) propagates as ok=false with no error.
func TestSubagentThreads_CancelSubagentToolCall_UnknownCallIDIsMiss(t *testing.T) {
	c, h, taskID, started := newTestCoordinatorRun(t)
	<-started

	c.RegisterSubagentToolCanceler(h.RunID(), taskID, func(string) bool { return false })

	threads := NewSubagentThreads()
	threads.RegisterTaskRoute(c, "call-1", h.RunID(), taskID)

	ok, err := threads.CancelSubagentToolCall("call-1", "never-existed")
	if err != nil {
		t.Fatalf("expected no error for an unknown tool call ID, got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for an unknown tool call ID")
	}

	_ = c.Cancel(context.Background(), h)
}

// TestSubagentThreads_CancelSubagentToolCall_UnregisteredCallIDIsMiss
// mirrors CancelSubagentTask's own no-route contract: an unregistered
// callID (this dialog's own key, never wired via RegisterTaskRoute) is a
// clean no-op, not an error.
func TestSubagentThreads_CancelSubagentToolCall_UnregisteredCallIDIsMiss(t *testing.T) {
	threads := NewSubagentThreads()
	ok, err := threads.CancelSubagentToolCall("never-registered", "tool-call-1")
	if err != nil {
		t.Fatalf("expected no error for an unregistered callID, got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for an unregistered callID")
	}
}

// TestSubagentThreads_CancelSubagentToolCall_NoCoordinatorErrors proves a
// registered route whose recorded coordinator is nil reports a clear error instead
// of silently no-oping - the same split CancelSubagentTask uses.
func TestSubagentThreads_CancelSubagentToolCall_NoCoordinatorErrors(t *testing.T) {
	threads := NewSubagentThreads()
	threads.RegisterTaskRoute(nil, "call-1", "run-x", "task-x")
	_, err := threads.CancelSubagentToolCall("call-1", "tool-call-1")
	if err == nil {
		t.Fatal("expected an error when no coordinator is wired")
	}
}

// TestSubagentThreads_CancelSubagentToolCall_NoCancelerRegisteredIsMiss
// proves a route that resolves to a real, live task - but one whose nested
// loop never published a ToolCanceler at all (e.g. it has not reached the
// point where OnToolCancelReady fires yet, or uses a backend that never
// calls it) - is a safe no-op rather than a panic or false claim.
func TestSubagentThreads_CancelSubagentToolCall_NoCancelerRegisteredIsMiss(t *testing.T) {
	c, h, taskID, started := newTestCoordinatorRun(t)
	<-started

	threads := NewSubagentThreads()
	threads.RegisterTaskRoute(c, "call-1", h.RunID(), taskID)

	ok, err := threads.CancelSubagentToolCall("call-1", "tool-call-1")
	if err != nil {
		t.Fatalf("expected no error when no canceler was ever registered, got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when no canceler was ever registered for this task")
	}

	_ = c.Cancel(context.Background(), h)
}

// compile-time sanity: agent.ToolCanceler is the exact func signature this
// file's fakes hand to RegisterSubagentToolCanceler.
var _ agent.ToolCanceler = func(string) bool { return false }
