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

	for _, key := range []string{"t1", "call_d1", "builder"} {
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
// guards the agent-name key: names like "builder" are not unique across
// dispatches, and the live path's getOrCreate uses Origin.Agent as a
// fallback lookup key. A reconstruction for a DIFFERENT task that happens
// to use the same agent name must not re-aim that shared key away from the
// live conversation.
func TestPopulateFromToolCalls_ReconstructionDoesNotStealLiveAgentNameKey(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	live := startLiveThread(t, threads, "t-live", "call_live", "builder")

	output := `[{"task_id":"t-old","status":"completed","output":"earlier run"}]`
	uiadapter.PopulateFromToolCalls(threads, dispatchMsg("call_old", "t-old", "builder", output))

	got, ok := threads.Thread("builder")
	if !ok {
		t.Fatalf("expected a thread under builder")
	}
	if got != live {
		t.Errorf("reconstruction stole the live agent-name key")
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
