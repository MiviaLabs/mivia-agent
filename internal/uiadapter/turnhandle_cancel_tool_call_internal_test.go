// turnhandle_cancel_tool_call_internal_test.go proves turnHandle.CancelToolCall's
// own guard clauses directly, one branch at a time, by constructing a
// turnHandle by hand rather than driving a full turn (as
// conversation_cancel_tool_call_test.go does). It lives in package
// uiadapter, not uiadapter_test, because toolCanceler is unexported and
// these cases need to set it (or deliberately leave it unset) precisely.
package uiadapter

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

// TestTurnHandleCancelToolCall_NilReceiverIsNoOp proves calling
// CancelToolCall on a nil *turnHandle returns false without panicking.
// Go permits calling a method on a nil pointer receiver as long as the
// method body itself nil-checks before dereferencing, which this one
// does as its very first condition.
func TestTurnHandleCancelToolCall_NilReceiverIsNoOp(t *testing.T) {
	var h *turnHandle
	if got := h.CancelToolCall("call-1"); got {
		t.Fatal("CancelToolCall on a nil *turnHandle returned true, want false")
	}
}

// TestTurnHandleCancelToolCall_EmptyCallIDIsNoOp proves a non-nil handle
// with a blank callID returns false, isolating the `callID == ""` leg of
// the guard from the nil-receiver leg.
func TestTurnHandleCancelToolCall_EmptyCallIDIsNoOp(t *testing.T) {
	h := &turnHandle{}
	if got := h.CancelToolCall(""); got {
		t.Fatal("CancelToolCall with an empty callID returned true, want false")
	}
}

// TestTurnHandleCancelToolCall_NoCancelerRegisteredIsNoOp proves a non-nil
// handle whose toolCanceler was never Store-d (Load returns a nil
// *agent.ToolCanceler) returns false rather than panicking on a nil
// dereference. This is the pre-SDK-registry window described in
// turnHandle's doc comment, and the steady state for a legacy
// (non-SDK) run.
func TestTurnHandleCancelToolCall_NoCancelerRegisteredIsNoOp(t *testing.T) {
	h := &turnHandle{}
	if got := h.CancelToolCall("call-1"); got {
		t.Fatal("CancelToolCall with no toolCanceler stored returned true, want false")
	}
}

// TestTurnHandleCancelToolCall_StoredNilCancelerIsNoOp proves a handle
// whose toolCanceler slot WAS stored, but points at a nil
// agent.ToolCanceler func value, returns false rather than panicking when
// it would otherwise invoke a nil func. Isolates the `*p == nil` leg of
// the guard from the `p == nil` leg.
func TestTurnHandleCancelToolCall_StoredNilCancelerIsNoOp(t *testing.T) {
	h := &turnHandle{}
	var nilCanceler agent.ToolCanceler
	h.toolCanceler.Store(&nilCanceler)

	if got := h.CancelToolCall("call-1"); got {
		t.Fatal("CancelToolCall with a stored-but-nil canceler returned true, want false")
	}
}

// TestTurnHandleCancelToolCall_ForwardsToStoredCanceler proves that once a
// real (non-nil) canceler is stored, CancelToolCall forwards the callID to
// it and returns its result, rather than always returning false - a
// positive control for the four no-op cases above.
func TestTurnHandleCancelToolCall_ForwardsToStoredCanceler(t *testing.T) {
	h := &turnHandle{}
	var gotID string
	canceler := agent.ToolCanceler(func(callID string) bool {
		gotID = callID
		return true
	})
	h.toolCanceler.Store(&canceler)

	if got := h.CancelToolCall("call-42"); !got {
		t.Fatal("CancelToolCall did not forward to the stored canceler's return value")
	}
	if gotID != "call-42" {
		t.Fatalf("stored canceler received callID = %q, want \"call-42\"", gotID)
	}
}
