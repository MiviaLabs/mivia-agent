package conversation

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestEnterWhileTurnActiveQueuesMessage(t *testing.T) {
	events := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}}}
	s := newScreen(t, replay.New(events, time.Hour), nil, nil)
	s = typeText(t, s, "hi")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	firstActive := s.active

	s = typeText(t, s, "again")
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Screen)
	if cmd != nil {
		t.Error("expected no Cmd: message should be queued until turn ends")
	}
	if got.active != firstActive {
		t.Error("expected the active handle to be unchanged while a turn is in flight")
	}
	if len(got.queue) != 1 || got.queue[0] != "again" {
		t.Errorf("expected queue = [\"again\"], got %v", got.queue)
	}
	if got.composer.Value() != "" {
		t.Errorf("expected composer to be cleared after queuing, got %q", got.composer.Value())
	}
}

func TestQueue_DrainsSequentiallyOnTurnEnd(t *testing.T) {
	events1 := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "msg1"}}}
	conv := replay.New(events1, time.Hour)
	s := newScreen(t, conv, nil, nil)

	// Send message 1 -> turn becomes active
	s = typeText(t, s, "msg1")
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.active == nil || cmd == nil {
		t.Fatal("expected active turn 1")
	}

	// Queue message 2 and message 3 while turn 1 is active
	s = typeText(t, s, "msg2")
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	s = typeText(t, s, "msg3")
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	if len(s.queue) != 2 || s.queue[0] != "msg2" || s.queue[1] != "msg3" {
		t.Fatalf("expected queue = [\"msg2\", \"msg3\"], got %v", s.queue)
	}

	// Turn 1 ends (channel closes -> turnEndedMsg)
	next, cmd = s.Update(turnEndedMsg{})
	s = next.(Screen)

	// Now msg2 should be popped and sent, becoming active turn 2
	if len(s.queue) != 1 || s.queue[0] != "msg3" {
		t.Errorf("expected queue = [\"msg3\"] after draining msg2, got %v", s.queue)
	}
	if s.active == nil || cmd == nil {
		t.Fatal("expected turn 2 to be started for msg2")
	}

	// Turn 2 ends -> msg3 should be sent
	next, cmd = s.Update(turnEndedMsg{})
	s = next.(Screen)

	if len(s.queue) != 0 {
		t.Errorf("expected empty queue after draining msg3, got %v", s.queue)
	}
	if s.active == nil || cmd == nil {
		t.Fatal("expected turn 3 to be started for msg3")
	}

	// Turn 3 ends -> queue empty -> active becomes nil
	next, _ = s.Update(turnEndedMsg{})
	s = next.(Screen)
	if s.active != nil {
		t.Errorf("expected s.active = nil after all queued messages complete")
	}
}

type failOnSecondSendConv struct {
	ports.Conversation
	sendCount int
	err       error
}

func (c *failOnSecondSendConv) Send(ctx context.Context, intent intent.Send) (ports.TurnHandle, error) {
	c.sendCount++
	if c.sendCount == 1 {
		return c.Conversation.Send(ctx, intent)
	}
	return nil, c.err
}

func TestQueue_SendErrorPreservesQueuedMessage(t *testing.T) {
	events := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "msg1"}}}
	first := replay.New(events, time.Hour)
	conv := &failOnSecondSendConv{Conversation: first, err: context.DeadlineExceeded}
	s := newScreen(t, conv, nil, nil)

	// Send message 1 -> active turn
	s = typeText(t, s, "msg1")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// Queue message 2
	s = typeText(t, s, "queued msg")
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// Turn 1 ends -> attempt to send "queued msg", which fails with deadline exceeded
	next, _ = s.Update(turnEndedMsg{})
	got := next.(Screen)

	if got.active != nil {
		t.Errorf("expected no active turn when queued send fails")
	}
	if len(got.queue) != 1 || got.queue[0] != "queued msg" {
		t.Errorf("expected queued message to be preserved after Send() error, got %v", got.queue)
	}
}
