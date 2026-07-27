package events

import (
	"testing"
	"time"
)

func TestNewEventFromAgentParts(t *testing.T) {
	now := time.Now()
	ev := NewEventFromAgentParts(KindToolStart, "call-1", "read_file", "running", "", `{"path":"a.go"}`, "")

	if ev.Kind != KindToolStart {
		t.Fatalf("expected KindToolStart, got %s", ev.Kind)
	}
	if ev.ToolCallID != "call-1" {
		t.Fatalf("expected ToolCallID call-1, got %s", ev.ToolCallID)
	}
	if ev.Name != "read_file" {
		t.Fatalf("expected Name read_file, got %s", ev.Name)
	}
	if ev.Detail != "running" {
		t.Fatalf("expected Detail running, got %s", ev.Detail)
	}
	if ev.Input != `{"path":"a.go"}` {
		t.Fatalf("expected Input json, got %s", ev.Input)
	}
	if ev.Output != "" {
		t.Fatalf("expected empty Output, got %s", ev.Output)
	}
	if ev.Timestamp.Before(now.Add(-time.Second)) {
		t.Fatal("Timestamp should be recent")
	}
}

func TestNewEventFromAgentParts_AllKinds(t *testing.T) {
	kinds := []Kind{
		KindAssistant, KindToolStart, KindToolEnd, KindStep,
		KindPrune, KindToolParallel, KindSubagentStart, KindSubagentEnd,
		KindSubagentHeartbeat,
	}
	for _, k := range kinds {
		ev := NewEventFromAgentParts(k, "tc-1", "tool", "detail", "content", "input", "output")
		if ev.Kind != k {
			t.Fatalf("expected kind %s, got %s", k, ev.Kind)
		}
		if ev.Content != "content" {
			t.Fatalf("expected content 'content', got %s", ev.Content)
		}
	}
}
