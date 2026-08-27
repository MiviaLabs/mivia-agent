// Tests for the partial-text-on-steered-stop carrier (item 1 / v4).
//
// The SDK cancels the in-flight Completer.Chat wholesale on Trigger,
// so the bytes emitted inside the canceled call are lost; what
// survives in `history` are the assistant messages the loop already
// appended before the cancel. The dispatcher must walk history and
// return the last non-empty assistant as the partial reply, mirroring
// the legacy `lastText` contract at loop.go:143-179. Without this
// fallback the dispatcher hard-returns "", turning a partial reply
// into an interrupted-and-empty response (regression of the legacy
// contract).
//
// The current-turn boundary is located by user-Content match (not by
// an index) because prepareSDKHistory may compact the pre-turn prefix
// when a PreparationManager is wired, so a stale index lands past the
// start of the SDK's history and the partial walk would silently
// return empty. Content match is robust against that prefix shape
// change.
package agent

import (
	"strings"
	"testing"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// TestSDKSteeredStopPartialReturnsLastAssistantContent pins the bug
// fix: when sdk.Result{Stop: StopSteered, History: [...]} arrives
// with a current-turn user message whose Content matches what the
// caller passed in, sdkSteeredStopPartial must return the most recent
// in-scope non-empty assistant Content along with errSteerInterrupt.
func TestSDKSteeredStopPartialReturnsLastAssistantContent(t *testing.T) {
	history := []sdkshape.Message{
		{Role: sdkshape.RoleUser, Content: "do the work"},
		{Role: sdkshape.RoleAssistant, Content: "first-step output"},
		{Role: sdkshape.RoleUser, Content: "follow up"},
		{Role: sdkshape.RoleAssistant, Content: "partial-prefix"}, // most recent
	}
	text, err := sdkSteeredStopPartial(history, "follow up")
	if err != errSteerInterrupt {
		t.Fatalf("err = %v, want errSteerInterrupt", err)
	}
	if text != "partial-prefix" {
		t.Fatalf("text = %q, want %q (last non-empty assistant from current turn)", text, "partial-prefix")
	}
}

// TestSDKSteeredStopPartialScoping pins the contract that the walk
// does NOT cross a turn boundary: a prior-turn assistant must not
// surface as a steered-stop partial. The user-Content match anchors
// the boundary at the second user message, so the prior-turn
// assistant (before that message) is out of scope.
// TestSDKSteeredStopPartialNoMatchingUserTreatsWholeHistoryAsCurrent
// pins the boundary handling when the SDK history lacks a user
// message matching userText at all (defensive only - the SDK loop
// appends the user message before any completions, so this is the
// truly-empty state). The whole history is treated as current-turn
// and any assistant content surfaces.
func TestSDKSteeredStopPartialNoMatchingUserTreatsWholeHistoryAsCurrent(t *testing.T) {
	history := []sdkshape.Message{
		{Role: sdkshape.RoleAssistant, Content: "stale assistant"},
	}
	text, err := sdkSteeredStopPartial(history, "no-such-user-message")
	if err != errSteerInterrupt {
		t.Fatalf("err = %v, want errSteerInterrupt", err)
	}
	if text != "stale assistant" {
		t.Fatalf("text = %q, want %q (whole history in scope when no match)", text, "stale assistant")
	}
}

// TestSDKSteeredStopPartialAllEmpty pins the truly-empty case: the
// helper still returns errSteerInterrupt, callers distinguish it from
// the partial case by the empty text.
func TestSDKSteeredStopPartialAllEmpty(t *testing.T) {
	history := []sdkshape.Message{
		{Role: sdkshape.RoleUser, Content: "do work"},
		{Role: sdkshape.RoleTool, Content: "tool output"},
	}
	text, err := sdkSteeredStopPartial(history, "do work")
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
	if err != errSteerInterrupt {
		t.Fatalf("err = %v, want errSteerInterrupt", err)
	}
}

// TestSDKSteeredStopPartialWhitespaceOnlySkipped pins the trim
// semantics: an assistant message containing only whitespace (a
// tool-calling turn that produced no model text) must not surface as
// the partial. The walk continues to the next older assistant.
func TestSDKSteeredStopPartialWhitespaceOnlySkipped(t *testing.T) {
	history := []sdkshape.Message{
		{Role: sdkshape.RoleUser, Content: "work"},
		{Role: sdkshape.RoleAssistant, Content: "visible"},
		{Role: sdkshape.RoleTool, Content: "tool result"},
		{Role: sdkshape.RoleAssistant, Content: "   \n\t"}, // whitespace-only
		{Role: sdkshape.RoleUser, Content: "new turn"},     // boundary
		{Role: sdkshape.RoleAssistant, Content: "after"},   // current turn's last
	}
	text, err := sdkSteeredStopPartial(history, "new turn")
	if err != errSteerInterrupt {
		t.Fatalf("err = %v, want errSteerInterrupt", err)
	}
	if text != "after" {
		t.Fatalf("text = %q, want %q (boundary at new-turn user; skip whitespace-only assistant inside current turn)", text, "after")
	}
}

// TestSDKSteeredStopPartialUserAtHistoryTail pins the load-bearing
// boundary edge case: when the user-Content match lands at
// res.History[len-1], startIdx == len(history), and the backward walk
// correctly yields empty text (no in-scope assistant). A future
// off-by-one edit to startIdx could flip this to a stale-leak rather
// than empty, so the test pins both fields explicitly.
func TestSDKSteeredStopPartialUserAtHistoryTail(t *testing.T) {
	history := []sdkshape.Message{
		{Role: sdkshape.RoleAssistant, Content: "earlier answer"},
		{Role: sdkshape.RoleUser, Content: "tail"}, // most recent
	}
	text, err := sdkSteeredStopPartial(history, "tail")
	if err != errSteerInterrupt {
		t.Fatalf("err = %v, want errSteerInterrupt", err)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty (user-Content at history tail leaves no in-scope assistant)", text)
	}
}

// TestSDKSteeredStopPartialScoping pins the contract that the walk
// does NOT cross a turn boundary: a prior-turn assistant must not
// surface as a steered-stop partial. The user-Content match anchors
// the boundary at the second user message, so the prior-turn
// assistant (before that message) is out of scope; the in-scope
// assistant at the tail is the partial.
func TestSDKSteeredStopPartialScopingAssertion(t *testing.T) {
	history := []sdkshape.Message{
		{Role: sdkshape.RoleUser, Content: "earlier turn"},
		{Role: sdkshape.RoleAssistant, Content: "earlier-turn-answer"}, // out of scope
		{Role: sdkshape.RoleUser, Content: "current turn"},
		{Role: sdkshape.RoleAssistant, Content: "current-turn-answer"}, // in-scope partial
	}
	text, err := sdkSteeredStopPartial(history, "current turn")
	if err != errSteerInterrupt {
		t.Fatalf("err = %v, want errSteerInterrupt", err)
	}
	if text != "current-turn-answer" {
		t.Fatalf("text = %q, want %q (in-scope assistant surfaced, prior-turn NOT surfaced)", text, "current-turn-answer")
	}
	if strings.Contains(text, "earlier-turn-answer") {
		t.Fatalf("text %q leaked the prior-turn answer", text)
	}
}

// use of sdkagentloop here silences an unused import when the test
// file is reduced; without this line `go vet` rejects the package
// build.
var _ = sdkagentloop.StopSteered
