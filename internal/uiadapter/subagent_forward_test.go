// subagent_forward_test.go pins the fix for a real user-reported bug: a
// subagent's own completion never reached the sidebar panel/row until the
// WHOLE enclosing dispatch_tasks batch finished, so a genuinely-finished
// subagent looked permanently stalled until a user opened its dialog
// (which replays LoadHistory directly, bypassing the broken turn-stream
// path). newTurnHandler diverted every non-zero-Origin agent.Event to
// SubagentThreads.HandleEvent ONLY, omitting it from the turn stream the
// panel/row status consumes. These tests pin the fix: only the
// status/lifecycle uievent Kind(s) that drive panel/row state (today:
// KindToolOutput, produced by translateSubagentDone's Progress entry) are
// ALSO forwarded onto the turn stream, while transcript CONTENT
// (assistant text, reasoning, nested tool-call deltas) stays diverted
// exactly as before.
package uiadapter_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// startBlockedSubagentTurn starts one turn on a fresh conversation whose
// completer is blocked on the returned unblock func, so the turn cannot
// finish (and close handle.Events()) before the test has injected
// synthetic Origin-stamped events through sess.OnAgentEvent - the same
// seam registerCapturingSubagentProgress exercises in
// conversation_test.go, reached directly via the exported field instead
// of the SubagentProgressRegistrar side channel.
func startBlockedSubagentTurn(t *testing.T) (handle ports.TurnHandle, sess *chat.Session, threads *uiadapter.SubagentThreads, unblock func()) {
	t.Helper()
	block := make(chan struct{})
	comp := &scriptedCompleter{block: block, turns: []provider.Response{assistantResponse("done")}}
	conv := newTestConversation(t, comp)
	threads = uiadapter.NewSubagentThreads()
	conv.SetSubagents(threads)

	h, err := conv.Send(context.Background(), intent.Send{Text: "run"})
	if err != nil {
		t.Fatalf("Send err=%v", err)
	}
	s := conv.Session()
	if s.OnAgentEvent == nil {
		t.Fatal("OnAgentEvent not installed after Send")
	}
	return h, s, threads, func() { close(block) }
}

// TestNewTurnHandler_SubagentDone_ForwardsProgressToTurnStream is RED
// test A: a completion signal for one subagent task must reach the turn
// stream as a KindToolOutput progress update (what filespanel.observeAgent
// keys the sidebar row on), and the "subagent done" advisory notice line
// must NOT also leak onto the root turn stream - only the subagent's own
// thread should see that.
func TestNewTurnHandler_SubagentDone_ForwardsProgressToTurnStream(t *testing.T) {
	handle, sess, _, unblock := startBlockedSubagentTurn(t)

	sess.OnAgentEvent(agent.Event{
		Kind: agent.EventSubagentDone,
		Origin: agent.EventOrigin{
			TaskID: "task-1", Agent: "auditor", TaskDescription: "audit the diff",
		},
	})
	unblock()

	events := drainUntilClose(t, handle.Events(), 5*time.Second)

	var progress []uievent.Event
	for _, e := range events {
		if e.Kind == uievent.KindToolOutput {
			progress = append(progress, e)
		}
	}
	if len(progress) != 1 {
		t.Fatalf("turn stream KindToolOutput count=%d, want 1: %+v", len(progress), events)
	}
	body, ok := progress[0].Body.(uievent.ToolOutputBody)
	if !ok {
		t.Fatalf("body type=%T, want ToolOutputBody", progress[0].Body)
	}
	if body.ToolCallID != "task-1" {
		t.Errorf("ToolCallID=%q, want task-1", body.ToolCallID)
	}
	if body.Progress == nil || body.Progress.Status != "completed" {
		t.Fatalf("Progress=%+v, want Status=completed", body.Progress)
	}

	for _, e := range events {
		if e.Kind != uievent.KindNotice {
			continue
		}
		if b, ok := e.Body.(uievent.NoticeBody); ok && strings.Contains(b.Text, "subagent done") {
			t.Errorf("root turn stream leaked the subagent-done advisory notice: %+v", e)
		}
	}
}

// TestNewTurnHandler_SubagentHeartbeat_ProducesNoTurnStreamEvent is RED
// test B, adjusted to the codebase's actual, independently tested
// contract: EventSubagentHeartbeat is an explicit droppedKinds entry
// (event.go) - TranslateEventWithOptions returns nil for it today (see
// event_test.go's "subagent_heartbeat_dropped" case), so there is no
// heartbeat-translated uievent for this fix to forward. This test pins
// that a heartbeat with a non-zero Origin still produces zero events on
// the turn stream after the fix (no accidental new leak), matching
// pre-fix behavior exactly - forwarding stays a pure filter over
// whatever TranslateEventWithOptions already produces, it does not
// invent new translation output.
func TestNewTurnHandler_SubagentHeartbeat_ProducesNoTurnStreamEvent(t *testing.T) {
	handle, sess, _, unblock := startBlockedSubagentTurn(t)

	sess.OnAgentEvent(agent.Event{
		Kind:   agent.EventSubagentHeartbeat,
		Detail: "elapsed=30s steps=2",
		Origin: agent.EventOrigin{TaskID: "task-hb", Agent: "auditor"},
	})
	unblock()

	events := drainUntilClose(t, handle.Events(), 5*time.Second)
	for _, e := range events {
		if e.Kind != uievent.KindTurnStart && e.Kind != uievent.KindTextEnd && e.Kind != uievent.KindTurnEnd {
			t.Errorf("unexpected event on turn stream from a dropped-kind subagent event: %+v", e)
		}
	}
}

// TestNewTurnHandler_SubagentContentEvents_NotForwardedToTurnStream is RED
// test C: assistant text, reasoning, and nested tool-call deltas from a
// subagent must never reach the root turn stream, proving the fix does
// not reopen the content-leak the Origin-based divert exists to prevent.
func TestNewTurnHandler_SubagentContentEvents_NotForwardedToTurnStream(t *testing.T) {
	handle, sess, _, unblock := startBlockedSubagentTurn(t)

	origin := agent.EventOrigin{TaskID: "task-2", Agent: "auditor"}
	sess.OnAgentEvent(agent.Event{Kind: agent.EventAssistant, Content: "leaked assistant text", Origin: origin})
	sess.OnAgentEvent(agent.Event{Kind: agent.EventThinking, Content: "leaked reasoning", Origin: origin})
	sess.OnAgentEvent(agent.Event{Kind: agent.EventToolStart, ToolCallID: "nested-1", Name: "read_file", Origin: origin})
	sess.OnAgentEvent(agent.Event{Kind: agent.EventToolEnd, ToolCallID: "nested-1", Name: "read_file", Detail: "completed", Output: "ok", Origin: origin})
	unblock()

	events := drainUntilClose(t, handle.Events(), 5*time.Second)
	for _, e := range events {
		switch e.Kind {
		case uievent.KindTextDelta, uievent.KindTextEnd, uievent.KindReasoning:
			if b, ok := e.Body.(uievent.TextDeltaBody); ok && strings.Contains(b.Text, "leaked") {
				t.Errorf("subagent assistant text leaked onto root turn stream: %+v", e)
			}
			if b, ok := e.Body.(uievent.TextEndBody); ok && strings.Contains(b.Text, "leaked") {
				t.Errorf("subagent assistant text leaked onto root turn stream: %+v", e)
			}
			if b, ok := e.Body.(uievent.ReasoningDeltaBody); ok && strings.Contains(b.Text, "leaked") {
				t.Errorf("subagent reasoning leaked onto root turn stream: %+v", e)
			}
		case uievent.KindToolStart:
			if b, ok := e.Body.(uievent.ToolStartBody); ok && b.ToolCallID == "nested-1" {
				t.Errorf("nested subagent tool.start leaked onto root turn stream: %+v", e)
			}
		case uievent.KindToolEnd:
			if b, ok := e.Body.(uievent.ToolEndBody); ok && b.ToolCallID == "nested-1" {
				t.Errorf("nested subagent tool.end leaked onto root turn stream: %+v", e)
			}
		}
	}
}

// TestNewTurnHandler_SubagentEvents_StillRecordedInSubagentThread is RED
// test D: SubagentThreads.HandleEvent must still be invoked for every
// non-zero-Origin event regardless of kind - the registration/history
// path a subagent's own dialog replays from (thread.LoadHistory) must
// stay fully intact; this fix is additive, not a replacement.
func TestNewTurnHandler_SubagentEvents_StillRecordedInSubagentThread(t *testing.T) {
	handle, sess, threads, unblock := startBlockedSubagentTurn(t)

	origin := agent.EventOrigin{TaskID: "task-3", Agent: "auditor"}
	sess.OnAgentEvent(agent.Event{Kind: agent.EventAssistant, Content: "recorded in subagent thread", Origin: origin})
	sess.OnAgentEvent(agent.Event{Kind: agent.EventSubagentDone, Origin: origin})
	unblock()
	drainUntilClose(t, handle.Events(), 5*time.Second)

	thread, ok := threads.Thread("task-3")
	if !ok {
		t.Fatal("expected a subagent thread registered for task-3")
	}
	hist := thread.History()
	found := false
	for _, m := range hist {
		if strings.Contains(m.Text, "recorded in subagent thread") {
			found = true
		}
	}
	if !found {
		t.Errorf("subagent thread history missing the recorded assistant text: %+v", hist)
	}
}
