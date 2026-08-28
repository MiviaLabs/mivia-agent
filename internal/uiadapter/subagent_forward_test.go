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

// TestNewTurnHandler_SubagentHeartbeat_ProducesKeyedProgressOnTurnStream
// pins the post-fix contract: a heartbeat with a TaskID translates to
// exactly one keyed tool.output progress event on the turn stream (that is
// the channel dispatch-task rows consume to show live liveness), carrying
// NO notice/text content - the root stream must still stay free of subagent
// transcript content, only keyed progress rides it. Heartbeats without an
// owning TaskID must produce nothing at all.
func TestNewTurnHandler_SubagentHeartbeat_ProducesKeyedProgressOnTurnStream(t *testing.T) {
	handle, sess, _, unblock := startBlockedSubagentTurn(t)

	sess.OnAgentEvent(agent.Event{
		Kind:   agent.EventSubagentHeartbeat,
		Detail: "elapsed=30s steps=2",
		Origin: agent.EventOrigin{TaskID: "task-hb", Agent: "auditor"},
	})
	sess.OnAgentEvent(agent.Event{
		Kind:   agent.EventSubagentHeartbeat,
		Detail: "ownerless tick",
	})
	unblock()

	events := drainUntilClose(t, handle.Events(), 5*time.Second)
	progress := 0
	for _, e := range events {
		switch {
		case e.Kind == uievent.KindToolOutput:
			progress++
			b, ok := e.Body.(uievent.ToolOutputBody)
			if !ok {
				t.Fatalf("tool.output body type = %T, want ToolOutputBody", e.Body)
			}
			if b.ToolCallID != "task-hb" {
				t.Errorf("progress ToolCallID = %q, want task-hb (keyed by Origin.TaskID)", b.ToolCallID)
			}
			if b.Progress == nil || b.Progress.Status != "running" || len(b.Progress.Log) != 1 || b.Progress.Log[0] != "elapsed=30s steps=2" {
				t.Errorf("progress payload = %+v, want running + last detail", b.Progress)
			}
		case e.Kind == uievent.KindTurnStart, e.Kind == uievent.KindTextEnd, e.Kind == uievent.KindTurnEnd:
			// expected turn lifecycle events
		default:
			t.Errorf("unexpected %s on turn stream from a subagent heartbeat: %+v", e.Kind, e)
		}
		if b, ok := e.Body.(uievent.NoticeBody); ok && strings.Contains(b.Text, "tick") {
			t.Errorf("heartbeat detail leaked as a notice line: %+v", e)
		}
	}
	if progress != 1 {
		t.Errorf("got %d keyed progress events, want exactly 1 (TaskID-less heartbeat must not emit)", progress)
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

// TestSubagentThreads_SameAgentDifferentTasksDoNotShareAThread is the
// regression for a real user-reported bug: a subagent's own thread stopped
// recording new history well before the run actually finished, while its
// sidebar row (Elapsed/Tools/Step) kept updating correctly.
//
// getOrCreate (subagent.go) registers a conversation under THREE keys:
// TaskID, ToolCallID, and the bare Agent name (unconditionally, since
// 371c35d5). Two dispatch_tasks entries commonly route to the SAME named
// agent (e.g. "general-purpose" is the default). The first task to look
// itself up finds nothing and creates a fresh conversation registered
// under all three keys, including the shared Agent name; the SECOND task's
// first event then finds that Agent-name key already claimed and is handed
// the FIRST task's conversation object instead of its own. Whichever task
// finishes first fires EventSubagentDone, which sets
// SubagentTranscriptConversation.done = true on the SHARED object - and
// RecordEvent's `if !c.done { c.active = true }` guard then silently
// drops every later event for the OTHER, still-running task: its final
// report looks frozen mid-run while dispatch_tasks/emitHeartbeat panel
// events (keyed independently, by TaskID, in filespanel.observeAgent)
// keep climbing Step/Tools/Elapsed as normal, exactly the confusing split
// symptom reported.
func TestSubagentThreads_SameAgentDifferentTasksDoNotShareAThread(t *testing.T) {
	handle, sess, threads, unblock := startBlockedSubagentTurn(t)

	originA := agent.EventOrigin{TaskID: "task-a", Agent: "general-purpose"}
	originB := agent.EventOrigin{TaskID: "task-b", Agent: "general-purpose"}

	// task-a starts and finishes first.
	sess.OnAgentEvent(agent.Event{Kind: agent.EventAssistant, Content: "task a final report", Origin: originA})
	sess.OnAgentEvent(agent.Event{Kind: agent.EventSubagentDone, Origin: originA})

	// task-b, routed to the SAME agent, is still running - its content
	// arrives AFTER task-a's Done event.
	sess.OnAgentEvent(agent.Event{Kind: agent.EventAssistant, Content: "task b still working", Origin: originB})
	unblock()
	drainUntilClose(t, handle.Events(), 5*time.Second)

	threadA, ok := threads.Thread("task-a")
	if !ok {
		t.Fatal("expected a thread for task-a")
	}
	threadB, ok := threads.Thread("task-b")
	if !ok {
		t.Fatal("expected a thread for task-b")
	}
	if threadA == threadB {
		t.Fatal("task-a and task-b share an agent name but must not share one conversation object")
	}

	found := false
	for _, m := range threadB.History() {
		if strings.Contains(m.Text, "task b still working") {
			found = true
		}
	}
	if !found {
		t.Errorf("task-b's history is missing content recorded after task-a's Done event fired (sealed by the shared-agent-name collision): %+v", threadB.History())
	}
}
