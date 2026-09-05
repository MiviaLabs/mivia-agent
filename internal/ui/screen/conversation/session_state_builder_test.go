package conversation

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// newSessionStateHistoryConv builds a backgroundTestConversation (the
// existing multi-session fixture, defined in multi_session_test.go) seeded
// with a fixed History() so newSessionState's replay loop has real
// branches to walk: a user turn, a reasoning-only assistant turn, a
// tool-call assistant turn, and a text-bearing assistant turn that is also
// the last message in the slice.
func newSessionStateHistoryConv() *backgroundTestConversation {
	return &backgroundTestConversation{
		id:    "history-session",
		title: "History Session",
		history: []ports.Message{
			// case "user": exercises history.Push and the TurnStart event.
			{Role: "user", Text: "what is the plan?", At: time.Unix(1, 0)},
			// default branch, reasoning only, not the last message: hits
			// the m.Reasoning != "" block and the isLastMsg==false path
			// (reason == "end_turn"), while leaving the tool-call loop and
			// the text block untaken (m.Text == "").
			{Role: "assistant", Reasoning: "thinking it over", At: time.Unix(2, 0)},
			// default branch, tool calls only, not the last message: hits
			// the tool-call loop body (ToolStart + ToolEnd per call).
			{
				Role: "assistant",
				At:   time.Unix(3, 0),
				ToolCalls: []ports.ToolCall{
					{ID: "call-1", Name: "run_command", Output: "ok"},
					{ID: "call-2", Name: "read_file", Output: "contents"},
				},
			},
			// default branch, text only, and the LAST message: hits the
			// m.Text != "" block and the isLastMsg==true path (reason ==
			// "completed").
			{Role: "assistant", Text: "here is the answer", At: time.Unix(4, 0)},
		},
	}
}

// TestNewSessionState_ReplaysHistoryIntoTranscriptAndComposer drives
// Screen.newSessionState through the real public surface used in
// production (mount.go's handleSessionMountedMsg calls the same
// unexported method) with a conversation whose History() is non-empty,
// covering the previously-untested replay loop: the per-message
// isLastMsg computation, the user/default role switch, the reasoning
// event, the tool-call event pair, the text event pair, and both branches
// of the turn-end reason (end_turn vs completed).
func TestNewSessionState_ReplaysHistoryIntoTranscriptAndComposer(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := newSessionStateHistoryConv()

	s := New(dark, theme.TierTrueColor, themes, conv, nil, 100, func() time.Time {
		return time.Unix(1000, 0)
	})

	st := s.newSessionState(conv)
	if st == nil {
		t.Fatal("newSessionState returned nil")
	}

	// history.Push was called exactly once, for the single "user" message.
	if got := st.history.Len(); got != 1 {
		t.Errorf("history.Len() = %d, want 1 (only the user message pushes)", got)
	}

	view := st.transcript.View()
	if view == "" {
		t.Fatal("expected transcript to render replayed history, got empty view")
	}
	// The reasoning and trailing text turns render collapsed/folded
	// summaries rather than their literal body, so assert on what the
	// transcript actually surfaces: the user prompt, the reasoning
	// marker, both replayed tool calls, and the end_turn reason emitted
	// for every non-last message.
	for _, want := range []string{"what is the plan?", "reasoning", "run_command", "read_file", "end_turn"} {
		if !strings.Contains(view, want) {
			t.Errorf("transcript view missing %q; view=%q", want, view)
		}
	}
}

// TestNewSessionState_NilConversationSkipsReplay documents that a nil
// conversation - the guard on line 35 that this suite doesn't otherwise
// exercise from this file - produces a session state with no replayed
// history rather than panicking on conv.History().
func TestNewSessionState_NilConversationSkipsReplay(t *testing.T) {
	dark, _, themes := themePair(t)
	s := New(dark, theme.TierTrueColor, themes, newSessionStateHistoryConv(), nil, 100, func() time.Time {
		return time.Unix(1000, 0)
	})

	st := s.newSessionState(nil)
	if st == nil {
		t.Fatal("newSessionState returned nil")
	}
	if got := st.history.Len(); got != 0 {
		t.Errorf("history.Len() = %d, want 0 for a nil conversation", got)
	}
}
