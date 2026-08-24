package uiadapter_test

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
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
	var count int
	for range h.Events() {
		count++
	}
	if count == 0 {
		t.Error("expected stream events on Send")
	}
	if len(gotConv.History()) != 3 {
		t.Errorf("expected 3 history items after send, got %d", len(gotConv.History()))
	}
}

func TestSubagentTranscriptConversation_EmptyTitle(t *testing.T) {
	conv := uiadapter.NewSubagentTranscriptConversation("", ports.ModelInfo{}, nil)
	if conv.Title() != "Subagent Thread" {
		t.Errorf("got %q, want 'Subagent Thread'", conv.Title())
	}
}
