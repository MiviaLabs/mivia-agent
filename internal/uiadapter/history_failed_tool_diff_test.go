package uiadapter_test

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestHistory_FailedFileToolCarriesNoDiffAndKeepsError pins defect A
// (finding 2): a failed file tool call must not be handed a synthetic
// zero-hunk Diff on History() replay. parseToolDiff has no ok/failure
// gate of its own - it happily builds a Diff from whatever path it can
// extract from a failed tool's "error: ..." output - and a non-nil
// zero-hunk Diff makes the renderer replace the error body with nothing
// (internal/ui/component/transcript/values.go -> internal/ui/render/
// diff_split.go's SplitDiffLines returning nil for zero hunks), losing
// the diagnostic on session resume. The live path (event_kind.go's
// translateToolEnd) already gates diff computation on ok; History()
// must gate the same way.
func TestHistory_FailedFileToolCarriesNoDiffAndKeepsError(t *testing.T) {
	tc := provider.ToolCall{
		ID:   "call_failed_1",
		Type: "function",
	}
	tc.Function.Name = "replace_file_content"
	tc.Function.Arguments = `{"path": "foo/bar.go"}`

	const errBody = "error: permission denied writing foo/bar.go"

	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "edit foo"},
		{
			Role:      provider.RoleAssistant,
			Content:   "editing...",
			ToolCalls: []provider.ToolCall{tc},
		},
		{
			Role:       provider.RoleTool,
			ToolCallID: "call_failed_1",
			Content:    errBody,
		},
	}
	comp := &scriptedCompleter{}
	conv := newTestConversation(t, comp, msgs...)
	hist := conv.History()
	if len(hist) < 2 {
		t.Fatalf("expected at least 2 history messages, got %d", len(hist))
	}
	assistantMsg := hist[1]
	if len(assistantMsg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(assistantMsg.ToolCalls))
	}
	got := assistantMsg.ToolCalls[0]
	if got.Output != errBody {
		t.Errorf("tool call output = %q, want the preserved error body %q", got.Output, errBody)
	}
	if got.Diff != nil {
		t.Errorf("tool call Diff = %+v, want nil for a failed tool call (losing the error body)", got.Diff)
	}
}
