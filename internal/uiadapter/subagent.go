package uiadapter

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// SubagentThreads implements ports.SubagentThreads by dynamically resolving
// threads registered during runtime subagent executions.
type SubagentThreads struct {
	mu      sync.Mutex
	threads map[string]ports.Conversation
}

// Compile-time check that SubagentThreads satisfies ports.SubagentThreads.
var _ ports.SubagentThreads = (*SubagentThreads)(nil)

// NewSubagentThreads creates a new SubagentThreads registry.
func NewSubagentThreads() *SubagentThreads {
	return &SubagentThreads{
		threads: make(map[string]ports.Conversation),
	}
}

// RegisterThread adds or replaces an active conversation thread for a tool call ID.
func (s *SubagentThreads) RegisterThread(callID string, conv ports.Conversation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threads[callID] = conv
}

// Thread retrieves the conversation thread for a given tool call ID.
func (s *SubagentThreads) Thread(callID string) (ports.Conversation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.threads[callID]
	return c, ok
}

// SubagentTranscriptConversation represents a subagent transcript thread.
type SubagentTranscriptConversation struct {
	title   string
	model   ports.ModelInfo
	mu      sync.Mutex
	history []ports.Message
}

// NewSubagentTranscriptConversation creates a subagent conversation wrapper.
func NewSubagentTranscriptConversation(title string, model ports.ModelInfo, history []ports.Message) *SubagentTranscriptConversation {
	outHistory := make([]ports.Message, len(history))
	copy(outHistory, history)
	return &SubagentTranscriptConversation{
		title:   title,
		model:   model,
		history: outHistory,
	}
}

// Send records user messages in the thread and emits transcript stream events.
func (c *SubagentTranscriptConversation) Send(_ context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.mu.Lock()
	c.history = append(c.history, ports.Message{Role: "user", Text: in.Text, At: time.Now()})
	c.mu.Unlock()

	id := fmt.Sprintf("%s-turn-%d", c.title, len(c.history))
	ch := make(chan uievent.Event, 4)
	go func() {
		defer close(ch)
		at := time.Now()
		ch <- uievent.Event{
			Kind:   uievent.KindTurnStart,
			TurnID: id,
			Seq:    1,
			At:     at,
			Body:   uievent.TurnStartBody{Input: in.Text},
		}
		ch <- uievent.Event{
			Kind:   uievent.KindTurnEnd,
			TurnID: id,
			Seq:    2,
			At:     at,
			Body:   uievent.TurnEndBody{Reason: "completed"},
		}
	}()
	return &subagentTurnHandle{id: id, events: ch}, nil
}

// History returns a copy of the thread history.
func (c *SubagentTranscriptConversation) History() []ports.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ports.Message, len(c.history))
	copy(out, c.history)
	return out
}

// Model returns the subagent model information.
func (c *SubagentTranscriptConversation) Model() ports.ModelInfo {
	return c.model
}

// ContextUsage returns token usage for the subagent thread.
func (c *SubagentTranscriptConversation) ContextUsage() ports.Usage {
	return ports.Usage{}
}

// Title returns the title of the subagent thread.
func (c *SubagentTranscriptConversation) Title() string {
	if strings.TrimSpace(c.title) == "" {
		return "Subagent Thread"
	}
	return c.title
}

// ID returns the subagent thread ID.
func (c *SubagentTranscriptConversation) ID() string {
	return c.title
}

type subagentTurnHandle struct {
	id     string
	events chan uievent.Event
}

func (h *subagentTurnHandle) ID() string                   { return h.id }
func (h *subagentTurnHandle) Events() <-chan uievent.Event { return h.events }
func (h *subagentTurnHandle) Cancel()                      {}
