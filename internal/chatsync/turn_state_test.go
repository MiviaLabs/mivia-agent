package chatsync

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func TestProjectorDropsTerminalForUnseenTurn(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})

	// Terminal for turn never seen before: dropped (loss tolerance)
	evEnd := events.Event{
		Kind:      events.KindTurnEnd,
		SessionID: "sess-1",
		TurnID:    "turn:unseen",
		Detail:    "completed",
		Timestamp: time.Now(),
	}
	weEnd := p.Project(evEnd)
	if len(weEnd) != 0 {
		t.Fatalf("unseen turn_end produced %d wire events, want 0", len(weEnd))
	}
	if p.LastSeq() != 0 {
		t.Errorf("lastSeq = %d, want 0", p.LastSeq())
	}

	evErr := events.Event{
		Kind:      events.KindError,
		SessionID: "sess-1",
		TurnID:    "turn:unseen2",
		Detail:    "failed",
		Timestamp: time.Now(),
	}
	weErr := p.Project(evErr)
	if len(weErr) != 0 {
		t.Fatalf("unseen error produced %d wire events, want 0", len(weErr))
	}
	if p.LastSeq() != 0 {
		t.Errorf("lastSeq = %d, want 0", p.LastSeq())
	}
}

func TestProjectorTerminalMarksTurnDoneAndBlocksDuplicateTerminal(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})

	// 1. Turn start
	start := events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "user text",
		Timestamp: time.Now(),
	}
	weStart := p.Project(start)
	if len(weStart) != 1 {
		t.Fatalf("turn_start produced %d events, want 1", len(weStart))
	}

	// 2. Turn end
	end := events.Event{
		Kind:      events.KindTurnEnd,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "completed",
		Timestamp: time.Now(),
	}
	weEnd := p.Project(end)
	if len(weEnd) != 1 {
		t.Fatalf("turn_end produced %d events, want 1", len(weEnd))
	}

	// 3. Duplicate turn end -> dropped
	weDupEnd := p.Project(end)
	if len(weDupEnd) != 0 {
		t.Fatalf("duplicate turn_end produced %d events, want 0", len(weDupEnd))
	}

	// 4. Duplicate turn start -> dropped
	weDupStart := p.Project(start)
	if len(weDupStart) != 0 {
		t.Fatalf("turn_start after turn_end produced %d events, want 0", len(weDupStart))
	}
}

func TestProjectorLRUBoundsTrackedTurns(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})

	// Mint 70 turns (capacity is 64)
	for i := 1; i <= 70; i++ {
		turnID := fmt.Sprintf("turn:%d", i)
		p.Project(events.Event{
			Kind:      events.KindTurnStart,
			SessionID: "sess-1",
			TurnID:    turnID,
			Detail:    fmt.Sprintf("turn %d", i),
			Timestamp: time.Now(),
		})
	}

	// Turn 1 was evicted (oldest-first)
	evictedEnd := events.Event{
		Kind:      events.KindTurnEnd,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "completed",
		Timestamp: time.Now(),
	}
	weEvicted := p.Project(evictedEnd)
	if len(weEvicted) != 0 {
		t.Errorf("evicted turn produced %d wire events, want 0", len(weEvicted))
	}

	// Turn 70 is still live
	recentEnd := events.Event{
		Kind:      events.KindTurnEnd,
		SessionID: "sess-1",
		TurnID:    "turn:70",
		Detail:    "completed",
		Timestamp: time.Now(),
	}
	weRecent := p.Project(recentEnd)
	if len(weRecent) != 1 {
		t.Fatalf("recent turn_end produced %d wire events, want 1", len(weRecent))
	}
}

func TestProjectorDeltaVsAggregateNonStreaming(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{StreamAssistant: false})

	// Turn start
	p.Project(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "prompt",
		Timestamp: time.Now(),
	})

	// Streamed deltas are suppressed when StreamAssistant is false
	weDelta := p.Project(events.Event{
		Kind:      events.KindAssistant,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "delta",
		Content:   "Hello ",
		Timestamp: time.Now(),
	})
	if len(weDelta) != 0 {
		t.Errorf("non-streaming delta produced %d wire events, want 0", len(weDelta))
	}

	// Final aggregate produces exactly one assistant.message with Fragments=0 and Text populated
	weMsg := p.Project(events.Event{
		Kind:      events.KindAssistant,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "",
		Content:   "Hello world!",
		Timestamp: time.Now(),
	})
	if len(weMsg) != 1 {
		t.Fatalf("assistant aggregate produced %d wire events, want 1", len(weMsg))
	}
	if weMsg[0].Type != TypeAssistantMessage {
		t.Fatalf("type = %q, want %q", weMsg[0].Type, TypeAssistantMessage)
	}
	msgPayload := weMsg[0].Payload.(*AssistantMessagePayload)
	if msgPayload.Fragments != 0 {
		t.Errorf("fragments = %d, want 0", msgPayload.Fragments)
	}
	if msgPayload.Text != "Hello world!" {
		t.Errorf("text = %q, want 'Hello world!'", msgPayload.Text)
	}
}

func TestProjectorDeltaVsAggregateStreaming(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{StreamAssistant: true})

	p.Project(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "prompt",
		Timestamp: time.Now(),
	})

	// Deltas produce assistant.delta
	d1 := p.Project(events.Event{
		Kind:      events.KindAssistant,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "delta",
		Content:   "part1 ",
		Timestamp: time.Now(),
	})
	if len(d1) != 1 || d1[0].Type != TypeAssistantDelta {
		t.Fatalf("d1 = %+v, want 1 assistant.delta", d1)
	}
	pd1 := d1[0].Payload.(*AssistantDeltaPayload)
	if pd1.Text != "part1 " || pd1.Index != 0 {
		t.Errorf("pd1 = %+v, want Text='part1 ', Index=0", pd1)
	}

	d2 := p.Project(events.Event{
		Kind:      events.KindAssistant,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "delta",
		Content:   "part2",
		Timestamp: time.Now(),
	})
	if len(d2) != 1 || d2[0].Type != TypeAssistantDelta {
		t.Fatalf("d2 = %+v, want 1 assistant.delta", d2)
	}
	pd2 := d2[0].Payload.(*AssistantDeltaPayload)
	if pd2.Text != "part2" || pd2.Index != 1 {
		t.Errorf("pd2 = %+v, want Text='part2', Index=1", pd2)
	}

	// Final aggregate with streaming ON produces assistant.message with Fragments=2, Text="" (INV-1)
	agg := p.Project(events.Event{
		Kind:      events.KindAssistant,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "",
		Content:   "part1 part2",
		Timestamp: time.Now(),
	})
	if len(agg) != 1 || agg[0].Type != TypeAssistantMessage {
		t.Fatalf("agg = %+v, want 1 assistant.message", agg)
	}
	pAgg := agg[0].Payload.(*AssistantMessagePayload)
	if pAgg.Fragments != 2 {
		t.Errorf("fragments = %d, want 2", pAgg.Fragments)
	}
	if pAgg.Text != "" {
		t.Errorf("text = %q, want '' (INV-1: message text must be empty when fragments > 0)", pAgg.Text)
	}

	// Assert JSON omits text field when empty
	raw, err := json.Marshal(pAgg)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if _, present := m["text"]; present {
		t.Errorf("json carries 'text' key when fragments > 0: %s", string(raw))
	}
}

func TestProjectorLateSubagentContentAfterTerminal(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})

	// Start and end turn
	p.Project(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "prompt",
		Timestamp: time.Now(),
	})
	p.Project(events.Event{
		Kind:      events.KindTurnEnd,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "completed",
		Timestamp: time.Now(),
	})

	// Late subagent event on same turn (subagents execute on separate goroutine)
	subEv := events.Event{
		Kind:       events.KindSubagentStart,
		SessionID:  "sess-1",
		TurnID:     "turn:1",
		ToolCallID: "sub_call_1",
		Name:       "subagent",
		Input:      "late input",
		Timestamp:  time.Now(),
	}
	weSub := p.Project(subEv)
	if len(weSub) != 1 {
		t.Fatalf("late subagent event produced %d wire events, want 1", len(weSub))
	}
	if weSub[0].Type != TypeSubagentToolStarted {
		t.Errorf("type = %q, want %q", weSub[0].Type, TypeSubagentToolStarted)
	}
}
