package uiadapter_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestSubagentThreads_RegisterAndLookup(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	if _, ok := threads.Thread("nonexistent"); ok {
		t.Error("expected ok=false for nonexistent thread")
	}

	modelInfo := ports.ModelInfo{Name: "m1", Provider: "p1"}
	history := []ports.Message{
		{Role: "user", Text: "do task", At: time.Now()},
		{Role: "assistant", Text: "task done", At: time.Now()},
	}
	conv := uiadapter.NewSubagentTranscriptConversation("subagent-1", modelInfo, history)

	threads.RegisterThread("call-123", conv)

	gotConv, ok := threads.Thread("call-123")
	if !ok || gotConv == nil {
		t.Fatalf("expected thread for call-123, got ok=%v", ok)
	}

	if gotConv.Title() != "subagent-1" {
		t.Errorf("got Title()=%q, want %q", gotConv.Title(), "subagent-1")
	}
	if gotConv.ID() != "subagent-1" {
		t.Errorf("got ID()=%q, want %q", gotConv.ID(), "subagent-1")
	}
	if gotConv.Model().Name != "m1" {
		t.Errorf("got Model()=%+v, want name m1", gotConv.Model())
	}
	if len(gotConv.History()) != 2 {
		t.Errorf("got History() len=%d, want 2", len(gotConv.History()))
	}
	_ = gotConv.ContextUsage()

	// Send on subagent conversation
	h, err := gotConv.Send(context.Background(), intent.Send{Text: "continue"})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if h.ID() == "" {
		t.Error("expected non-empty turn ID")
	}
	conv.RecordEvent(uievent.Event{
		Kind: uievent.KindTextDelta,
		Body: uievent.TextDeltaBody{Text: "response chunk"},
	})
	select {
	case ev := <-h.Events():
		if ev.Kind != uievent.KindTextDelta {
			t.Errorf("got event kind %v, want KindTextDelta", ev.Kind)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timed out waiting for event on listener")
	}
	h.Cancel()
	if len(gotConv.History()) != 4 {
		t.Errorf("expected 4 history items after send and reply, got %d", len(gotConv.History()))
	}
}

func TestSubagentTranscriptConversation_ActiveTurn(t *testing.T) {
	conv := uiadapter.NewSubagentTranscriptConversation("worker", ports.ModelInfo{}, nil)

	// Initially inactive
	if _, active := conv.ActiveTurn(); active {
		t.Fatal("expected ActiveTurn=false before any events")
	}

	// Active on receiving an event
	conv.RecordEvent(uievent.Event{
		Kind: uievent.KindReasoning,
		Body: uievent.ReasoningDeltaBody{Text: "thinking..."},
	})
	h, active := conv.ActiveTurn()
	if !active || h == nil {
		t.Fatal("expected ActiveTurn=true while streaming")
	}

	// Receives streamed events
	conv.RecordEvent(uievent.Event{
		Kind: uievent.KindTextDelta,
		Body: uievent.TextDeltaBody{Text: "hello from worker"},
	})
	select {
	case ev := <-h.Events():
		if ev.Kind != uievent.KindTextDelta {
			t.Errorf("got %v, want KindTextDelta", ev.Kind)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for text delta")
	}

	// Completed via subagent done notice
	conv.RecordEvent(uievent.Event{
		Kind: uievent.KindNotice,
		Body: uievent.NoticeBody{Text: "subagent done: worker"},
	})

	if _, active := conv.ActiveTurn(); active {
		t.Error("expected ActiveTurn=false after subagent done notice")
	}
}

// TestSubagentTranscriptConversation_StragglerAfterDoneStaysInactive guards
// against a straggler event (a late tool_end, a delayed forwarded delta from
// a salvage/cleanup window) arriving after a thread's own terminal event has
// already fired. RecordEvent used to set active=true unconditionally at the
// top for every event, with no check for a prior terminal state, so the
// straggler resurrected active on a thread that will never produce another
// terminal event - ActiveTurn() would then hand out a live subscription that
// never closes, and any UI code waiting on it as the done signal hangs
// forever.
func TestSubagentTranscriptConversation_StragglerAfterDoneStaysInactive(t *testing.T) {
	conv := uiadapter.NewSubagentTranscriptConversation("worker", ports.ModelInfo{}, nil)

	conv.RecordEvent(uievent.Event{
		Kind: uievent.KindNotice,
		Body: uievent.NoticeBody{Text: "subagent done: worker"},
	})
	if _, active := conv.ActiveTurn(); active {
		t.Fatal("expected ActiveTurn=false immediately after subagent done notice")
	}

	// Straggler: a tool_end arriving after the thread's own terminal event.
	conv.RecordEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "late-1", Result: "late result"},
	})

	if _, active := conv.ActiveTurn(); active {
		t.Error("expected ActiveTurn=false to stay false after a straggler event post-done")
	}
}

// TestSubagentTranscriptConversation_StragglerHistoryStillAppended is a
// regression check: guarding active=false against stragglers must not change
// the existing history-append side effect of a straggler event - its content
// still lands in history the same as before this fix.
func TestSubagentTranscriptConversation_StragglerHistoryStillAppended(t *testing.T) {
	conv := uiadapter.NewSubagentTranscriptConversation("worker", ports.ModelInfo{}, nil)

	conv.RecordEvent(uievent.Event{
		Kind: uievent.KindReasoning,
		Body: uievent.ReasoningDeltaBody{Text: "thinking..."},
	})
	conv.RecordEvent(uievent.Event{
		Kind: uievent.KindNotice,
		Body: uievent.NoticeBody{Text: "subagent done: worker"},
	})

	before := len(conv.History())

	conv.RecordEvent(uievent.Event{
		Kind: uievent.KindTextDelta,
		Body: uievent.TextDeltaBody{Text: "straggler text"},
	})

	hist := conv.History()
	if len(hist) != before {
		t.Fatalf("expected straggler to append into existing assistant message, history len got %d, want %d", len(hist), before)
	}
	if !strings.Contains(hist[len(hist)-1].Text, "straggler text") {
		t.Errorf("expected straggler content to still be appended to history, got %+v", hist[len(hist)-1])
	}
}

// TestSubagentTranscriptConversation_NewTurnAfterDoneResetsDone guards the
// object-reuse path: getOrCreate (internal/uiadapter/subagent.go) returns the
// SAME *SubagentTranscriptConversation for a later event sharing any of its
// registration keys (TaskID, ToolCallID, or bare Agent name), so a genuine
// new turn can legitimately land on an already-done conversation object. A
// KindTurnStart in that situation is a real restart, not a straggler, and
// must reactivate the thread.
func TestSubagentTranscriptConversation_NewTurnAfterDoneResetsDone(t *testing.T) {
	conv := uiadapter.NewSubagentTranscriptConversation("worker", ports.ModelInfo{}, nil)

	conv.RecordEvent(uievent.Event{
		Kind: uievent.KindNotice,
		Body: uievent.NoticeBody{Text: "subagent done: worker"},
	})
	if _, active := conv.ActiveTurn(); active {
		t.Fatal("expected ActiveTurn=false after subagent done notice")
	}

	conv.RecordEvent(uievent.Event{
		Kind: uievent.KindTurnStart,
		Body: uievent.TurnStartBody{Input: "second run"},
	})

	if _, active := conv.ActiveTurn(); !active {
		t.Error("expected a genuine new KindTurnStart to reactivate a done thread")
	}
}

func TestSubagentTranscriptConversation_EmptyTitle(t *testing.T) {
	conv := uiadapter.NewSubagentTranscriptConversation("", ports.ModelInfo{}, nil)
	if conv.Title() != "Subagent Thread" {
		t.Errorf("got %q, want 'Subagent Thread'", conv.Title())
	}
}

// TestSubagentThreads_LookupByToolCallIDAndTaskID pins lookup by TaskID
// and ToolCallID, both always-safe identity keys (a ToolCallID belongs to
// exactly one dispatch; a TaskID is the caller's own unique task id).
// Agent name is deliberately NOT asserted as a third alias here when a
// TaskID is present - see
// TestSubagentThreads_LookupByAgentNameOnlyWhenTaskIDAbsent and
// TestSubagentThreads_SameAgentDifferentTasksDoNotShareAThread for why:
// two different tasks routed to the same named agent (e.g.
// "general-purpose") must never be folded into one conversation object.
func TestSubagentThreads_LookupByToolCallIDAndTaskID(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	threads.HandleEvent(agent.Event{
		Kind:       agent.EventToolStart,
		ToolCallID: "call_abc",
		Origin:     agent.EventOrigin{TaskID: "task-123", Agent: "researcher"},
	}, uiadapter.TranslateOptions{})

	byTask, ok1 := threads.Thread("task-123")
	byCall, ok2 := threads.Thread("call_abc")

	if !ok1 || byTask == nil {
		t.Errorf("expected thread found by TaskID")
	}
	if !ok2 || byCall == nil {
		t.Errorf("expected thread found by ToolCallID")
	}
	if byTask != byCall {
		t.Errorf("expected same conversation instance across TaskID and ToolCallID")
	}
}

// TestSubagentThreads_LookupByAgentNameOnlyWhenTaskIDAbsent pins the
// fallback: when a caller has nothing else to key on, Agent name still
// works as a last resort (the reason the key exists at all, 371c35d5).
func TestSubagentThreads_LookupByAgentNameOnlyWhenTaskIDAbsent(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	threads.HandleEvent(agent.Event{
		Kind:   agent.EventToolStart,
		Origin: agent.EventOrigin{Agent: "researcher"},
	}, uiadapter.TranslateOptions{})

	byAgent, ok := threads.Thread("researcher")
	if !ok || byAgent == nil {
		t.Errorf("expected thread found by Agent when no TaskID/ToolCallID is available")
	}
}

func TestPopulateFromToolCalls_DispatchTasks(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					// Real wire shape from dispatchTasksTool.encodeResults
					// (internal/cliorchestrate/dispatch.go): a bare JSON array
					// keyed "task_id", not {"tasks":[{"id":...}]}.
					ID:        "call_dispatch_1",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"task-audit","prompt":"check for leaks","agent":"bug-auditor"},{"id":"task-plan","prompt":"design architecture","agent":"planner"}]}`,
					Output:    `[{"task_id":"task-audit","status":"completed","output":"no leaks found"},{"task_id":"task-plan","status":"completed","output":"architecture approved"}]`,
				},
			},
		},
	}

	uiadapter.PopulateFromToolCalls(threads, msgs)

	auditConv, ok := threads.Thread("task-audit")
	if !ok || auditConv == nil {
		t.Fatalf("expected thread for task-audit")
	}
	if len(auditConv.History()) != 2 {
		t.Fatalf("expected 2 history messages for task-audit, got %d", len(auditConv.History()))
	}
	if auditConv.History()[0].Text != "check for leaks" {
		t.Errorf("task-audit prompt: got %q, want 'check for leaks'", auditConv.History()[0].Text)
	}
	if auditConv.History()[1].Text != "no leaks found" {
		t.Errorf("task-audit output: got %q, want 'no leaks found'", auditConv.History()[1].Text)
	}

	planConv, ok := threads.Thread("task-plan")
	if !ok || planConv == nil {
		t.Fatalf("expected thread for task-plan")
	}
	if planConv.History()[1].Text != "architecture approved" {
		t.Errorf("task-plan output: got %q, want 'architecture approved'", planConv.History()[1].Text)
	}
}

// TestPopulateFromToolCalls_DispatchTasksMissingIDFallbackIsFriendly pins
// the fix for a raw provider tool_call_id ("call_xxxxxxxxxxxx") leaking
// into a visible sidebar row: a task the model forgot to give an "id"
// used to fall back to "{callID}-{index}", so the row's identity (and
// therefore its rendered label, since the panel displays IDs directly)
// exposed the raw call id verbatim. The fallback must never embed it.
func TestPopulateFromToolCalls_DispatchTasksMissingIDFallbackIsFriendly(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_95bcae0ca204bc76",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"prompt":"tidy the docs","agent":"docs-writer"}]}`,
					Output:    `{"tasks":[{"status":"completed","output":"done"}]}`,
				},
			},
		},
	}

	uiadapter.PopulateFromToolCalls(threads, msgs)

	if _, ok := threads.Thread("call_95bcae0ca204bc76-1"); ok {
		t.Fatal("the fallback task id must not embed the raw provider call id")
	}
	fallback, ok := threads.Thread("task-1")
	if !ok || fallback == nil {
		t.Fatalf("expected a friendly fallback thread id (task-1)")
	}
}

func TestPopulateFromToolCalls_Delegate(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_delegate_1",
					Name:      "delegate",
					Arguments: `{"task":"research sqlite persistence","agent":"researcher"}`,
					Output:    `{"status":"completed","output":"sqlite is persistent across restarts"}`,
				},
			},
		},
	}

	uiadapter.PopulateFromToolCalls(threads, msgs)

	delConv, ok := threads.Thread("call_delegate_1")
	if !ok || delConv == nil {
		t.Fatalf("expected thread for call_delegate_1")
	}
	if delConv.History()[0].Text != "research sqlite persistence" {
		t.Errorf("delegate prompt mismatch: got %q", delConv.History()[0].Text)
	}
	if delConv.History()[1].Text != "sqlite is persistent across restarts" {
		t.Errorf("delegate output mismatch: got %q", delConv.History()[1].Text)
	}
}

func TestPopulateFromToolCalls_SpawnAgent(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					// Real wire shape from spawnAgentTool.Execute
					// (internal/cliorchestrate/orchestrate.go): "tasks" array
					// in, {"task_results":[{"task_id":...,"output":...}]} out.
					ID:        "call_spawn_1",
					Name:      "spawn_agent",
					Arguments: `{"tasks":[{"id":"task-security","prompt":"review security policies","agent":"security-reviewer"}],"wait":"run"}`,
					Output:    `{"run_id":"r1","status":"completed","task_results":[{"task_id":"task-security","status":"completed","output":"security verified"}]}`,
				},
			},
		},
	}

	uiadapter.PopulateFromToolCalls(threads, msgs)

	spawnConv, ok := threads.Thread("task-security")
	if !ok || spawnConv == nil {
		t.Fatalf("expected thread for task-security")
	}
	if spawnConv.History()[0].Text != "review security policies" {
		t.Errorf("spawn_agent prompt mismatch: got %q", spawnConv.History()[0].Text)
	}
	if spawnConv.History()[1].Text != "security verified" {
		t.Errorf("spawn_agent output mismatch: got %q", spawnConv.History()[1].Text)
	}
}

// TestPopulateFromToolCalls_DispatchTasksNestedObjectOutput guards a
// dispatch_tasks task whose own raw output is itself a JSON object (a common
// shape: ModelVisibleOutput in internal/cliorchestrate/synopsis.go embeds
// the subagent's raw bytes as-is when they are valid JSON) rather than a
// plain string - the reconstruction must unwrap the nested "output" key
// instead of stringifying the whole object or losing the text.
func TestPopulateFromToolCalls_DispatchTasksNestedObjectOutput(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_dispatch_nested",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"plan-review-1","prompt":"review the plan","agent":"reviewer"}]}`,
					Output:    `[{"task_id":"plan-review-1","status":"completed","output":{"output":"## Plan\n\n1. Do the thing"}}]`,
				},
			},
		},
	}

	uiadapter.PopulateFromToolCalls(threads, msgs)

	conv, ok := threads.Thread("plan-review-1")
	if !ok || conv == nil {
		t.Fatalf("expected thread for plan-review-1")
	}
	hist := conv.History()
	if len(hist) != 2 || hist[1].Text != "## Plan\n\n1. Do the thing" {
		t.Errorf("expected the nested output text to be unwrapped, got history=%+v", hist)
	}
}

// TestPopulateFromToolCalls_DispatchTasksRunLevelError guards the
// run-level-failure envelope dispatchTasksTool.Execute returns when the
// whole run errors before any per-task result exists
// ({"error":...,"status":...}, no "tasks"/array shape at all): the
// reconstruction must surface a readable status, not a raw JSON dump, and
// must not silently produce an empty/missing thread.
func TestPopulateFromToolCalls_DispatchTasksRunLevelError(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_dispatch_err",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"plan-review-1","prompt":"review the plan","agent":"reviewer"}]}`,
					Output:    `{"error":"context canceled","status":"canceled"}`,
				},
			},
		},
	}

	uiadapter.PopulateFromToolCalls(threads, msgs)

	conv, ok := threads.Thread("plan-review-1")
	if !ok || conv == nil {
		t.Fatalf("expected thread for plan-review-1")
	}
	hist := conv.History()
	if len(hist) != 2 || hist[1].Text != "context canceled (status: canceled)" {
		t.Errorf("expected a readable run-level error message, got history=%+v", hist)
	}
}

// TestPopulateFromToolCalls_DispatchTasksRunLevelErrorMultiTask guards the
// same run-level-failure envelope as the single-task case above, but for a
// multi-task dispatch: dispatchTasksTool.Execute returns the bare
// {"error":...,"status":...} envelope whenever the run fails before
// finalizeDAG produces any per-task result, regardless of how many tasks
// were dispatched (internal/cliorchestrate/dispatch.go's own comment: "the
// empty-results fallback underneath stays reachable"). Every dispatched
// task must still get the readable error text, not silent empty output.
func TestPopulateFromToolCalls_DispatchTasksRunLevelErrorMultiTask(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_dispatch_err2",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"task-a","prompt":"do a","agent":"a"},{"id":"task-b","prompt":"do b","agent":"b"}]}`,
					Output:    `{"error":"context canceled","status":"canceled"}`,
				},
			},
		},
	}

	uiadapter.PopulateFromToolCalls(threads, msgs)

	for _, id := range []string{"task-a", "task-b"} {
		conv, ok := threads.Thread(id)
		if !ok || conv == nil {
			t.Fatalf("expected thread for %s", id)
		}
		hist := conv.History()
		if len(hist) != 2 || hist[1].Text != "context canceled (status: canceled)" {
			t.Errorf("%s: expected a readable run-level error message, got history=%+v", id, hist)
		}
	}
}

// TestPopulateFromToolCalls_DispatchTasksWrappedEnvelope verifies that
// dispatch_tasks tool calls returning the async wrapped envelope
// ({"run_id":"r1","status":"completed","task_results":[{"task_id":"task-async","status":"completed","output":"async done"}]})
// correctly reconstruct subagent tasks in threads.
func TestPopulateFromToolCalls_DispatchTasksWrappedEnvelope(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_dispatch_wrapped",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"task-async","prompt":"execute async work","agent":"worker"}],"wait":"task","wait_task_id":"task-async"}`,
					Output:    `{"run_id":"r1","status":"completed","task_results":[{"task_id":"task-async","status":"completed","output":"async done"}]}`,
				},
			},
		},
	}

	uiadapter.PopulateFromToolCalls(threads, msgs)

	conv, ok := threads.Thread("task-async")
	if !ok || conv == nil {
		t.Fatalf("expected thread for task-async")
	}
	hist := conv.History()
	if len(hist) != 2 {
		t.Fatalf("expected 2 history items, got %d (history=%+v)", len(hist), hist)
	}
	if hist[0].Text != "execute async work" {
		t.Errorf("prompt mismatch: got %q", hist[0].Text)
	}
	if hist[1].Text != "async done" {
		t.Errorf("output mismatch: got %q", hist[1].Text)
	}
}

// TestPopulateFromToolCalls_DispatchTasksToolCallsReconstructed guards the
// Part B wiring: dispatch_tasks result envelopes now carry a pre-merged,
// one-row-per-call "tool_calls" array (cliorchestrate's loadToolCallSummaries,
// chunk 5) alongside the usual "output". Reconstruction must attach those
// rows onto the SAME output message's ToolCalls slice (matching the shape
// subagent.go's KindToolStart/KindToolEnd build for a live session), leaving
// the message's Text (from resultText/synopsis) untouched. A call marked
// "incomplete" (a genuinely unfinished tool call, never a cap artifact per
// chunk 5) must reconstruct with an empty Output - not be dropped or
// special-cased.
func TestPopulateFromToolCalls_DispatchTasksToolCallsReconstructed(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_dispatch_tc",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"task-tc","prompt":"grep and read","agent":"researcher"}]}`,
					Output: `[{"task_id":"task-tc","status":"completed","output":"done grepping","tool_calls":[` +
						`{"tool_call_id":"tc1","name":"grep","input":"{\"pattern\":\"foo\"}","output":"3 matches"},` +
						`{"tool_call_id":"tc2","name":"read","input":"{\"path\":\"a.go\"}","output":"file contents"},` +
						`{"tool_call_id":"tc3","name":"bash","input":"{\"cmd\":\"go build\"}","incomplete":true}` +
						`]}]`,
				},
			},
		},
	}

	uiadapter.PopulateFromToolCalls(threads, msgs)

	conv, ok := threads.Thread("task-tc")
	if !ok || conv == nil {
		t.Fatalf("expected thread for task-tc")
	}
	hist := conv.History()
	if len(hist) != 2 {
		t.Fatalf("expected 2 history messages (prompt + output), got %d: %+v", len(hist), hist)
	}
	if hist[0].Text != "grep and read" {
		t.Errorf("prompt mismatch: got %q", hist[0].Text)
	}
	out := hist[1]
	if out.Text != "done grepping" {
		t.Errorf("output text mismatch: got %q, want %q (must be unchanged by tool_calls presence)", out.Text, "done grepping")
	}
	if len(out.ToolCalls) != 3 {
		t.Fatalf("expected 3 reconstructed tool calls, got %d: %+v", len(out.ToolCalls), out.ToolCalls)
	}

	want := []ports.ToolCall{
		{ID: "tc1", Name: "grep", Arguments: `{"pattern":"foo"}`, Output: "3 matches"},
		{ID: "tc2", Name: "read", Arguments: `{"path":"a.go"}`, Output: "file contents"},
		{ID: "tc3", Name: "bash", Arguments: `{"cmd":"go build"}`, Output: ""},
	}
	for i, w := range want {
		got := out.ToolCalls[i]
		if got.ID != w.ID || got.Name != w.Name || got.Arguments != w.Arguments || got.Output != w.Output {
			t.Errorf("tool call %d mismatch: got %+v, want %+v", i, got, w)
		}
	}
}

// TestPopulateFromToolCalls_DispatchTasksByReferenceOutput guards
// dispatch_tasks' output-by-reference result shape
// (internal/cliorchestrate/dispatch_encode.go's setOutputFields): once a
// task's real output exceeds the inline threshold, the tool omits "output"
// entirely and reports "synopsis"/"output_ref" instead. Before this,
// stringifyTaskOutput only ever looked at "output", so any task whose
// result went by-reference reconstructed with NO assistant message at
// all - a resumed session showed the dispatched prompt and nothing else,
// as if the subagent produced no output.
func TestPopulateFromToolCalls_DispatchTasksByReferenceOutput(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_dispatch_ref",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"deepdive-security","prompt":"deep dive into security","agent":"auditor"}]}`,
					Output:    `[{"task_id":"deepdive-security","status":"completed","synopsis":"Reviewed secretpath, redact, miviaauth; no findings.","output_ref":"ref:output:abc123def456"}]`,
				},
			},
		},
	}

	uiadapter.PopulateFromToolCalls(threads, msgs)

	conv, ok := threads.Thread("deepdive-security")
	if !ok || conv == nil {
		t.Fatalf("expected thread for deepdive-security")
	}
	hist := conv.History()
	if len(hist) != 2 {
		t.Fatalf("expected 2 history items (prompt + synopsis), got %d (history=%+v)", len(hist), hist)
	}
	if hist[0].Text != "deep dive into security" {
		t.Errorf("prompt mismatch: got %q", hist[0].Text)
	}
	if !strings.Contains(hist[1].Text, "Reviewed secretpath, redact, miviaauth; no findings.") {
		t.Errorf("expected the synopsis text, got %q", hist[1].Text)
	}
}

// TestPopulateFromToolCalls_PreFeatureJSONShapeFallsBackCleanly guards the
// compatibility case chunk 6 (the ToolCalls reconstruction, see
// TestPopulateFromToolCalls_DispatchTasksToolCallsReconstructed above) must
// never regress: every session persisted before the tool-call-history
// feature shipped has a dispatch_tasks output JSON with NO "tool_calls" key
// at all (the pre-chunk-5 dispatchTaskResult/modelTaskResult wire shape).
// json.Unmarshal leaves that struct field as a nil slice, indistinguishable
// in Go from an explicit "tool_calls":[] - this test proves both shapes
// reconstruct byte-for-byte identically to today's pre-feature behavior: a
// prompt message plus one output message carrying only .Text, no
// reconstructed .ToolCalls entries, no panic.
func TestPopulateFromToolCalls_PreFeatureJSONShapeFallsBackCleanly(t *testing.T) {
	buildMsgs := func(output string) []ports.Message {
		return []ports.Message{
			{
				Role: "assistant",
				At:   time.Now(),
				ToolCalls: []ports.ToolCall{
					{
						ID:        "call_dispatch_legacy",
						Name:      "dispatch_tasks",
						Arguments: `{"tasks":[{"id":"task-legacy","prompt":"legacy prompt","agent":"researcher"}]}`,
						Output:    output,
					},
				},
			},
		}
	}

	reconstruct := func(t *testing.T, output string) ports.Message {
		t.Helper()
		threads := uiadapter.NewSubagentThreads()
		uiadapter.PopulateFromToolCalls(threads, buildMsgs(output))

		conv, ok := threads.Thread("task-legacy")
		if !ok || conv == nil {
			t.Fatalf("expected thread for task-legacy")
		}
		hist := conv.History()
		if len(hist) != 2 {
			t.Fatalf("expected 2 history messages (prompt + output), got %d: %+v", len(hist), hist)
		}
		if hist[0].Text != "legacy prompt" {
			t.Errorf("prompt mismatch: got %q", hist[0].Text)
		}
		return hist[1]
	}

	t.Run("key absent (pre-feature wire shape)", func(t *testing.T) {
		out := reconstruct(t, `[{"task_id":"task-legacy","status":"completed","output":"legacy output"}]`)
		if out.Text != "legacy output" {
			t.Errorf("output text mismatch: got %q, want %q", out.Text, "legacy output")
		}
		if len(out.ToolCalls) != 0 {
			t.Errorf("expected no reconstructed tool calls, got %+v", out.ToolCalls)
		}
	})

	t.Run("key present but empty array", func(t *testing.T) {
		out := reconstruct(t, `[{"task_id":"task-legacy","status":"completed","output":"legacy output","tool_calls":[]}]`)
		if out.Text != "legacy output" {
			t.Errorf("output text mismatch: got %q, want %q", out.Text, "legacy output")
		}
		if len(out.ToolCalls) != 0 {
			t.Errorf("expected no reconstructed tool calls, got %+v", out.ToolCalls)
		}
	})

	absent := reconstruct(t, `[{"task_id":"task-legacy","status":"completed","output":"legacy output"}]`)
	empty := reconstruct(t, `[{"task_id":"task-legacy","status":"completed","output":"legacy output","tool_calls":[]}]`)
	if absent.Text != empty.Text {
		t.Errorf("key-absent and explicit-empty-array Text differ: %q vs %q", absent.Text, empty.Text)
	}
	if len(absent.ToolCalls) != len(empty.ToolCalls) {
		t.Errorf("key-absent and explicit-empty-array ToolCalls length differ: %d vs %d", len(absent.ToolCalls), len(empty.ToolCalls))
	}
}
