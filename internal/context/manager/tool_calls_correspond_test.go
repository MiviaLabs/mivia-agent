package manager

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestToolCallsCorrespondMismatchedElements pins the element-level branch
// of the structural identity check: equal-length lists whose elements
// differ are NOT the same identity (a retention walk that forgave them
// would silently accept a rewritten tool call), while identical lists and
// the nil-vs-empty representation shift still correspond.
func TestToolCallsCorrespondMismatchedElements(t *testing.T) {
	mk := func(args string) []provider.ToolCall {
		tc := provider.ToolCall{ID: "call-1", Type: "function"}
		tc.Function.Name = "write"
		tc.Function.Arguments = args
		return []provider.ToolCall{tc}
	}
	input, altered := mk(`{"path":"a"}`), mk(`{"path":"b"}`)
	if toolCallsCorrespond(input, altered) {
		t.Fatal("equal-length lists with differing elements must not correspond")
	}
	if !toolCallsCorrespond(input, append([]provider.ToolCall(nil), input...)) {
		t.Fatal("identical lists must correspond")
	}
	if toolCallsCorrespond(input, nil) {
		t.Fatal("length mismatch must not correspond")
	}
}
