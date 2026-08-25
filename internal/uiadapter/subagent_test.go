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
