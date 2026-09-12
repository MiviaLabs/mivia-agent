package uiadapter

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestTurnStreamSendInitialClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan uievent.Event, 2)
	s := newTurnStream(ch, ctx.Done(), cancel)
	s.Close()

	if s.SendInitial(uievent.Event{Kind: uievent.KindTurnStart}) {
		t.Fatal("SendInitial on closed stream returned true")
	}
}

func TestConversationLiveNilGuards(t *testing.T) {
	var c *Conversation
	if c.IsForeground() {
		t.Fatal("nil IsForeground returned true")
	}
	if c.IsBackground() {
		t.Fatal("nil IsBackground returned true")
	}
	c.SetForeground(true) // should not panic
	c.SetBackground(true) // should not panic
}

func TestConversationProgressNilGuards(t *testing.T) {
	var c *Conversation
	if c.beginTurnProgress(func(agent.Event) {}) != 0 {
		t.Fatal("nil beginTurnProgress returned non-zero")
	}
	c.endTurnProgress(1)         // should not panic
	c.syncProgressRegistration() // should not panic
}

func TestTurnHandlerForwardClosedAndFilter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan uievent.Event, 10)
	stream := newTurnStream(ch, ctx.Done(), cancel)
	var closed atomic.Bool
	var turnIDPtr atomic.Pointer[string]
	tid := "turn-123"
	turnIDPtr.Store(&tid)
	var seq uint64

	handler := newTurnHandler(stream, &closed, &turnIDPtr, &seq, ctx, TranslateOptions{}, nil)

	// Subagent origin event with forwardable kind vs non-forwardable
	evSub := agent.Event{
		Origin: agent.EventOrigin{TaskID: "task-1"},
		Kind:   agent.EventSubagentDone,
	}
	handler(evSub)

	// Now mark closed and verify early return
	closed.Store(true)
	handler(agent.Event{Kind: agent.EventAssistant, Content: "test"})
	handler(evSub)

	// Test filterSubagentForward with matching kinds
	matched := filterSubagentForward([]uievent.Event{
		{Kind: uievent.KindToolOutput},
		{Kind: uievent.KindTurnStart}, // not in subagentForwardKinds
	})
	if len(matched) != 1 || matched[0].Kind != uievent.KindToolOutput {
		t.Fatalf("unexpected filterSubagentForward result: %v", matched)
	}
	if filterSubagentForward(nil) != nil {
		t.Fatal("expected nil for empty slice")
	}
}
