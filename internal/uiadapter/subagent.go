package uiadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
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

// HandleEvent receives subagent-originated agent.Events, translates them,
// records their history, and routes them to the matching SubagentTranscriptConversation.
func (s *SubagentThreads) HandleEvent(ev agent.Event, opts TranslateOptions) {
	if ev.Origin.IsZero() {
		return
	}
	keys := []string{ev.Origin.TaskID, ev.ToolCallID, ev.Origin.Agent}
	var hasKey bool
	for _, k := range keys {
		if k != "" {
			hasKey = true
			break
		}
	}
	if !hasKey {
		return
	}

	conv := s.getOrCreate(keys, ev.Origin.Agent)
	translated := TranslateEventWithOptions(ev, opts)
	for _, e := range translated {
		conv.RecordEvent(e)
	}
}

func (s *SubagentThreads) getOrCreate(keys []string, title string) *SubagentTranscriptConversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if k == "" {
			continue
		}
		if c, ok := s.threads[k]; ok {
			if stc, ok := c.(*SubagentTranscriptConversation); ok {
				for _, other := range keys {
					if other != "" {
						s.threads[other] = stc
					}
				}
				return stc
			}
		}
	}
	if title == "" {
		for _, k := range keys {
			if k != "" {
				title = k
				break
			}
		}
	}
	stc := NewSubagentTranscriptConversation(title, ports.ModelInfo{Name: title}, nil)
	for _, k := range keys {
		if k != "" {
			s.threads[k] = stc
		}
	}
	return stc
}

// SubagentTranscriptConversation represents a subagent transcript thread.
type SubagentTranscriptConversation struct {
	title     string
	model     ports.ModelInfo
	mu        sync.Mutex
	history   []ports.Message
	listeners []chan uievent.Event
	active    bool
}

// NewSubagentTranscriptConversation creates a new thread conversation.
func NewSubagentTranscriptConversation(title string, model ports.ModelInfo, history []ports.Message) *SubagentTranscriptConversation {
	var histCopy []ports.Message
	if len(history) > 0 {
		histCopy = make([]ports.Message, len(history))
		copy(histCopy, history)
	}
	return &SubagentTranscriptConversation{
		title:   title,
		model:   model,
		history: histCopy,
	}
}

func isDoneNotice(e uievent.Event) bool {
	if e.Kind == uievent.KindNotice {
		if b, ok := e.Body.(uievent.NoticeBody); ok {
			return strings.HasPrefix(b.Text, "subagent done")
		}
	}
	return false
}

// RecordEvent records one translated uievent into message history and notifies listeners.
func (c *SubagentTranscriptConversation) RecordEvent(e uievent.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.active = true
	switch e.Kind {
	case uievent.KindTurnStart:
		body, _ := e.Body.(uievent.TurnStartBody)
		c.history = append(c.history, ports.Message{
			Role: "user",
			Text: body.Input,
			At:   e.At,
		})
	case uievent.KindReasoning:
		body, _ := e.Body.(uievent.ReasoningDeltaBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		c.history[lastIdx].Reasoning += body.Text
	case uievent.KindToolStart:
		body, _ := e.Body.(uievent.ToolStartBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		argsBytes, _ := json.Marshal(body.Args)
		c.history[lastIdx].ToolCalls = append(c.history[lastIdx].ToolCalls, ports.ToolCall{
			ID:        body.ToolCallID,
			Name:      body.Name,
			Arguments: string(argsBytes),
		})
	case uievent.KindToolEnd:
		body, _ := e.Body.(uievent.ToolEndBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		for i := range c.history[lastIdx].ToolCalls {
			if c.history[lastIdx].ToolCalls[i].ID == body.ToolCallID {
				c.history[lastIdx].ToolCalls[i].Output = body.Result
				break
			}
		}
		if body.Diff != nil {
			c.history[lastIdx].Diffs = append(c.history[lastIdx].Diffs, *body.Diff)
		}
	case uievent.KindTextDelta:
		body, _ := e.Body.(uievent.TextDeltaBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		c.history[lastIdx].Text += body.Text
	case uievent.KindTextEnd:
		body, _ := e.Body.(uievent.TextEndBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		if c.history[lastIdx].Text == "" {
			c.history[lastIdx].Text = body.Text
		}
	}

	for _, ch := range c.listeners {
		select {
		case ch <- e:
		default:
		}
	}
	if e.Kind == uievent.KindTurnEnd || isDoneNotice(e) {
		c.active = false
		for _, ch := range c.listeners {
			close(ch)
		}
		c.listeners = nil
	}
}

func (c *SubagentTranscriptConversation) ensureLastAssistantMessage(at time.Time) {
	if len(c.history) == 0 || c.history[len(c.history)-1].Role != "assistant" {
		c.history = append(c.history, ports.Message{
			Role: "assistant",
			At:   at,
		})
	}
}

// ActiveTurn returns a live event subscription for the active subagent.
func (c *SubagentTranscriptConversation) ActiveTurn() (ports.TurnHandle, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return nil, false
	}
	ch := make(chan uievent.Event, 32)
	c.listeners = append(c.listeners, ch)
	id := fmt.Sprintf("%s-live", c.title)
	h := &subagentTurnHandle{
		id:     id,
		events: ch,
		cancel: func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			for i, l := range c.listeners {
				if l == ch {
					c.listeners = append(c.listeners[:i], c.listeners[i+1:]...)
					close(ch)
					break
				}
			}
		},
	}
	return h, true
}

// Send records user messages in the thread and emits transcript stream events.
func (c *SubagentTranscriptConversation) Send(_ context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.mu.Lock()
	c.history = append(c.history, ports.Message{Role: "user", Text: in.Text, At: time.Now()})
	ch := make(chan uievent.Event, 32)
	c.listeners = append(c.listeners, ch)
	c.mu.Unlock()

	id := fmt.Sprintf("%s-turn-%d", c.title, len(c.history))
	h := &subagentTurnHandle{
		id:     id,
		events: ch,
		cancel: func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			for i, l := range c.listeners {
				if l == ch {
					c.listeners = append(c.listeners[:i], c.listeners[i+1:]...)
					close(ch)
					break
				}
			}
		},
	}
	return h, nil
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
	cancel func()
	once   sync.Once
}

func (h *subagentTurnHandle) ID() string                   { return h.id }
func (h *subagentTurnHandle) Events() <-chan uievent.Event { return h.events }
func (h *subagentTurnHandle) Cancel() {
	h.once.Do(func() {
		if h.cancel != nil {
			h.cancel()
		}
	})
}
