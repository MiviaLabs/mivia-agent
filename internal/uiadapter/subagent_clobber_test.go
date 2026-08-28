package uiadapter_test

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// startLiveThread registers a live streaming conversation the way the
// runtime does - HandleEvent with a subagent-originated event - and then
// streams one text delta into it, so the thread carries content only the
// live path has.
func startLiveThread(t *testing.T, threads *uiadapter.SubagentThreads, taskID, callID, agentName string) ports.Conversation {
	t.Helper()
	threads.HandleEvent(agent.Event{
		Kind:       agent.EventToolStart,
		ToolCallID: callID,
		Origin:     agent.EventOrigin{TaskID: taskID, Agent: agentName},
	}, uiadapter.TranslateOptions{})
	conv, ok := threads.Thread(taskID)
	if !ok || conv == nil {
		t.Fatalf("expected live thread for %s", taskID)
	}
	stc, ok := conv.(*uiadapter.SubagentTranscriptConversation)
	if !ok {
		t.Fatalf("expected *SubagentTranscriptConversation, got %T", conv)
	}
	stc.RecordEvent(uievent.Event{
		Kind: uievent.KindTextDelta,
		Body: uievent.TextDeltaBody{Text: "live streamed content"},
	})
	return conv
}

func dispatchMsg(callID, taskID, agentName, output string) []ports.Message {
	return []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        callID,
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"` + taskID + `","prompt":"do the work","agent":"` + agentName + `"}]}`,
					Output:    output,
				},
			},
		},
	}
}

// TestPopulateFromToolCalls_DoesNotClobberInFlightLiveThread guards the
// dispatch-in-flight trace: the assistant message carrying the dispatch_tasks
// call is persisted before its RoleTool result, so a History() replay
// (screen construction, session switch, ClearTranscript, SetSubagents) runs
// PopulateFromToolCalls with tc.Output == "" while the subagent is still
// streaming. The reconstruction - a prompt-only stub - must NOT displace the
// live streaming conversation under any of its keys, or the open dialog
// freezes on an orphaned conversation and a reopened dialog shows nothing.
func TestPopulateFromToolCalls_DoesNotClobberInFlightLiveThread(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	live := startLiveThread(t, threads, "t1", "call_d1", "builder")

	uiadapter.PopulateFromToolCalls(threads, dispatchMsg("call_d1", "t1", "builder", ""))

	// Agent name ("builder") is deliberately not checked here: neither the
	// live path nor the reconstruction path registers it as a key once a
	// TaskID exists, so two unrelated tasks sharing an agent name never
	// collide - see TestSubagentThreads_SameAgentDifferentTasksDoNotShareAThread.
	for _, key := range []string{"t1", "call_d1"} {
		got, ok := threads.Thread(key)
		if !ok {
			t.Fatalf("expected a thread under %q", key)
		}
		if got != live {
			t.Errorf("key %q: reconstruction displaced the live conversation", key)
			continue
		}
	}
	hist := live.History()
	var found bool
	for _, m := range hist {
		if strings.Contains(m.Text, "live streamed content") {
			found = true
		}
	}
	if !found {
		t.Errorf("live streamed content lost; history=%+v", hist)
	}
}

// TestPopulateFromToolCalls_DoesNotClobberCompletedLiveThread guards the
// post-run variant: once a subagent finished streaming, its live
// conversation holds the full reasoning/tool detail. A later History()
// replay reconstructs a 2-message prompt+output summary from the persisted
// result JSON; that summary must not silently replace the richer live
// conversation.
func TestPopulateFromToolCalls_DoesNotClobberCompletedLiveThread(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	live := startLiveThread(t, threads, "t1", "call_d1", "builder")
	if stc, ok := live.(*uiadapter.SubagentTranscriptConversation); ok {
		stc.RecordEvent(uievent.Event{
			Kind: uievent.KindNotice,
			Body: uievent.NoticeBody{Text: "subagent done: builder"},
		})
	}

	output := `[{"task_id":"t1","status":"completed","output":"summary only"}]`
	uiadapter.PopulateFromToolCalls(threads, dispatchMsg("call_d1", "t1", "builder", output))

	got, ok := threads.Thread("t1")
	if !ok {
		t.Fatalf("expected a thread under t1")
	}
	if got != live {
		t.Fatalf("reconstruction displaced the completed live conversation")
	}
}

// TestPopulateFromToolCalls_ReconstructionDoesNotStealLiveAgentNameKey
// guards a shared agent name across a live thread and a reconstructed one
// for a DIFFERENT task: names like "builder" are not unique across
// dispatches, so neither the live path (HandleEvent) nor the
// reconstruction path (registerDispatchedTask) registers by bare agent
// name once a TaskID exists - "builder" resolves to nothing, and each
// task stays reachable only under its own task id, never merged.
func TestPopulateFromToolCalls_ReconstructionDoesNotStealLiveAgentNameKey(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	live := startLiveThread(t, threads, "t-live", "call_live", "builder")

	output := `[{"task_id":"t-old","status":"completed","output":"earlier run"}]`
	uiadapter.PopulateFromToolCalls(threads, dispatchMsg("call_old", "t-old", "builder", output))

	if _, ok := threads.Thread("builder"); ok {
		t.Error("agent name must not resolve to any thread once a TaskID exists on both sides")
	}
	if got, ok := threads.Thread("t-live"); !ok || got != live {
		t.Errorf("expected the live conversation still reachable under its own task id")
	}

	// The reconstruction itself must still exist under its own task key.
	old, ok := threads.Thread("t-old")
	if !ok || old == live {
		t.Errorf("expected a distinct reconstructed thread under t-old")
	}
}

// TestPopulateFromToolCalls_ResumeStillRegistersAndRefreshes guards the
// genuine resume path (no live conversation at all): reconstruction must
// still register, and a later PopulateFromToolCalls run over richer
// persisted messages must refresh an earlier reconstruction (replacing a
// reconstruction with a reconstruction is an idempotent refresh).
func TestPopulateFromToolCalls_ResumeStillRegistersAndRefreshes(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()

	// First replay: dispatch persisted mid-flight, no output yet.
	uiadapter.PopulateFromToolCalls(threads, dispatchMsg("call_d1", "t1", "builder", ""))
	conv, ok := threads.Thread("t1")
	if !ok || conv == nil {
		t.Fatalf("expected reconstructed thread for t1 on resume")
	}
	if len(conv.History()) != 1 {
		t.Fatalf("expected prompt-only reconstruction, got %+v", conv.History())
	}

	// Second replay: the result landed in persisted history.
	output := `[{"task_id":"t1","status":"completed","output":"final answer"}]`
	uiadapter.PopulateFromToolCalls(threads, dispatchMsg("call_d1", "t1", "builder", output))
	conv, ok = threads.Thread("t1")
	if !ok || conv == nil {
		t.Fatalf("expected refreshed thread for t1")
	}
	hist := conv.History()
	if len(hist) != 2 || hist[1].Text != "final answer" {
		t.Errorf("expected refreshed reconstruction with the final output, got %+v", hist)
	}
}

// TestPopulateFromToolCalls_OutputAndToolCallsPairFromSameRow guards the
// pairing rule shared by matchTaskOutputs and matchTaskToolCalls: when a
// task's ID matches a result row, BOTH its output text and its tool calls
// must come from that row - even when the ID-matched row's text is empty
// (an ID match with empty text means the task genuinely produced no text,
// not that a positional row is a better source). Before the fix,
// matchTaskOutputs fell back positionally on empty TEXT while
// matchTaskToolCalls fell back only on an ABSENT id, so t1 could render
// t2's output text above t1's own tool calls.
func TestPopulateFromToolCalls_OutputAndToolCallsPairFromSameRow(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_pair",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"t1","prompt":"first","agent":"a"},{"id":"t2","prompt":"second","agent":"b"}]}`,
					// Results arrive out of order: position 0 is t2's row
					// (with text and tool calls), t1's own row has an ID
					// match but empty output text plus its own tool calls.
					Output: `[` +
						`{"task_id":"t2","status":"completed","output":"t2 text","tool_calls":[{"tool_call_id":"tc-t2","name":"grep","output":"t2 grep"}]},` +
						`{"task_id":"t1","status":"completed","output":"","tool_calls":[{"tool_call_id":"tc-t1","name":"read","output":"t1 read"}]}` +
						`]`,
				},
			},
		},
	}

	uiadapter.PopulateFromToolCalls(threads, msgs)

	conv, ok := threads.Thread("t1")
	if !ok || conv == nil {
		t.Fatalf("expected thread for t1")
	}
	hist := conv.History()
	if len(hist) != 2 {
		t.Fatalf("expected prompt + assistant message for t1, got %+v", hist)
	}
	out := hist[1]
	if out.Text != "" {
		t.Errorf("t1 text must come from its ID-matched row (empty), got %q", out.Text)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "tc-t1" {
		t.Errorf("t1 tool calls must come from its ID-matched row, got %+v", out.ToolCalls)
	}

	conv2, ok := threads.Thread("t2")
	if !ok || conv2 == nil {
		t.Fatalf("expected thread for t2")
	}
	hist2 := conv2.History()
	if len(hist2) != 2 || hist2[1].Text != "t2 text" {
		t.Errorf("t2 must keep its own ID-matched text, got %+v", hist2)
	}
	if len(hist2[1].ToolCalls) != 1 || hist2[1].ToolCalls[0].ID != "tc-t2" {
		t.Errorf("t2 tool calls mismatch: %+v", hist2[1].ToolCalls)
	}
}
