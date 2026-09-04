package conversation

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

type compactionTestConversation struct {
	usage ports.Usage
	sends int
}

func (c *compactionTestConversation) Send(context.Context, intent.Send) (ports.TurnHandle, error) {
	c.sends++
	return nil, nil
}
func (*compactionTestConversation) ActiveTurn() (ports.TurnHandle, bool) { return nil, false }
func (*compactionTestConversation) History() []ports.Message             { return nil }
func (*compactionTestConversation) Model() ports.ModelInfo               { return ports.ModelInfo{} }
func (c *compactionTestConversation) ContextUsage() ports.Usage          { return c.usage }
func (*compactionTestConversation) Title() string                        { return "test" }
func (*compactionTestConversation) ID() string                           { return "compact-test" }

type compactionTestHandle struct{ events chan ports.CompactionEvent }

func (h compactionTestHandle) Events() <-chan ports.CompactionEvent { return h.events }
func (compactionTestHandle) Cancel()                                {}

func TestMessageEnteredDuringCompactionIsQueued(t *testing.T) {
	conv := &compactionTestConversation{}
	s := newScreen(t, conv, nil, nil)
	s.compaction = compactionTestHandle{events: make(chan ports.CompactionEvent)}
	s = typeText(t, s, "after compact")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Screen)
	if conv.sends != 0 {
		t.Fatalf("Send calls = %d, want 0 while compacting", conv.sends)
	}
	if len(got.queue) != 1 || got.queue[0] != "after compact" {
		t.Fatalf("queue = %#v, want one queued message", got.queue)
	}
}

func TestCompactionCompletionRefreshesTopbarUsage(t *testing.T) {
	conv := &compactionTestConversation{usage: ports.Usage{InputTokens: 120}}
	s := newScreen(t, conv, nil, nil)
	s.topbar.SetSession(conv.Model(), ports.Usage{InputTokens: 900})
	s.compaction = compactionTestHandle{events: make(chan ports.CompactionEvent)}
	next, _ := s.handleCompactionEvent(ports.CompactionEvent{SessionID: conv.ID(), Done: true})
	got := next.(Screen)
	if usage := got.topbar.Usage().InputTokens; usage != 120 {
		t.Fatalf("topbar input tokens = %d, want 120", usage)
	}
}
