package uiadapter_test

import (
	"context"
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

func TestSubagentTranscriptConversation_EmptyTitle(t *testing.T) {
	conv := uiadapter.NewSubagentTranscriptConversation("", ports.ModelInfo{}, nil)
	if conv.Title() != "Subagent Thread" {
		t.Errorf("got %q, want 'Subagent Thread'", conv.Title())
	}
}

func TestSubagentThreads_LookupByToolCallIDAndTaskID(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	threads.HandleEvent(agent.Event{
		Kind:       agent.EventToolStart,
		ToolCallID: "call_abc",
		Origin:     agent.EventOrigin{TaskID: "task-123", Agent: "researcher"},
	}, uiadapter.TranslateOptions{})

	byTask, ok1 := threads.Thread("task-123")
	byCall, ok2 := threads.Thread("call_abc")
	byAgent, ok3 := threads.Thread("researcher")

	if !ok1 || byTask == nil {
		t.Errorf("expected thread found by TaskID")
	}
	if !ok2 || byCall == nil {
		t.Errorf("expected thread found by ToolCallID")
	}
	if !ok3 || byAgent == nil {
		t.Errorf("expected thread found by Agent")
	}

	if byTask != byCall || byCall != byAgent {
		t.Errorf("expected same conversation instance across all lookup keys")
	}
}

func TestPopulateFromToolCalls_DispatchedAndDelegatedTasks(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()

	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_dispatch_1",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"task-audit","prompt":"check for leaks","agent":"bug-auditor"},{"id":"task-plan","prompt":"design architecture","agent":"planner"}]}`,
					Output:    `{"tasks":[{"id":"task-audit","status":"completed","output":"no leaks found"},{"id":"task-plan","status":"completed","output":"architecture approved"}]}`,
				},
				{
					ID:        "call_delegate_1",
					Name:      "delegate",
					Arguments: `{"task":"research sqlite persistence","agent":"researcher"}`,
					Output:    `{"status":"completed","output":"sqlite is persistent across restarts"}`,
				},
				{
					ID:        "call_spawn_1",
					Name:      "spawn_agent",
					Arguments: `{"prompt":"review security policies","agent":"security-reviewer"}`,
					Output:    `security verified`,
				},
			},
		},
	}

	uiadapter.PopulateFromToolCalls(threads, msgs)

	// Verify dispatch_tasks subtasks
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

	// Verify delegate
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

	// Verify spawn_agent
	spawnConv, ok := threads.Thread("call_spawn_1")
	if !ok || spawnConv == nil {
		t.Fatalf("expected thread for call_spawn_1")
	}
	if spawnConv.History()[0].Text != "review security policies" {
		t.Errorf("spawn_agent prompt mismatch: got %q", spawnConv.History()[0].Text)
	}
	if spawnConv.History()[1].Text != "security verified" {
		t.Errorf("spawn_agent output mismatch: got %q", spawnConv.History()[1].Text)
	}
}
