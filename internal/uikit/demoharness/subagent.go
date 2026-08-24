package demoharness

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

// Thread implements ports.SubagentThreads over the scenario's scripted
// subagents: a call id the fixture carries gets a live Conversation; an
// unknown one gets none, and the panel falls back to the step log.
func (h *Harness) Thread(callID string) (ports.Conversation, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fix, ok := h.subagents[callID]
	if !ok {
		return nil, false
	}
	history := make([]ports.Message, len(fix.History))
	copy(history, fix.History)
	return &threadConversation{pace: h.pace, fix: fix, history: history}, true
}

// threadConversation is the fixture-backed ports.Conversation for one
// subagent thread. History starts from the fixture and grows as the
// user sends; Send streams the scripted reply in the same event
// vocabulary the main harness uses (turn.start, text.delta chunks,
// text.end, turn.end), paced the same way, so the embedded screen's
// send path is exercised exactly as it is for the main chat.
type threadConversation struct {
	pace time.Duration
	fix  subagentFixture

	mu      sync.Mutex
	history []ports.Message
}

func (c *threadConversation) Send(_ context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.mu.Lock()
	c.history = append(c.history, ports.Message{Role: "user", Text: in.Text, At: time.Now()})
	reply := c.fix.Reply
	c.mu.Unlock()

	id := fmt.Sprintf("%s-turn-%d", c.fix.CallID, len(c.history))
	ch := make(chan uievent.Event)
	go func() {
		defer close(ch)
		at := time.Now()
		var seq uint64
		emit := func(kind uievent.Kind, body uievent.Body) {
			seq++
			ch <- uievent.Event{Kind: kind, TurnID: id, Seq: seq, At: at, Body: body}
			time.Sleep(c.pace)
		}
		emit(uievent.KindTurnStart, uievent.TurnStartBody{Input: in.Text})
		for _, chunk := range chunkReply(reply) {
			emit(uievent.KindTextDelta, uievent.TextDeltaBody{Text: chunk})
		}
		emit(uievent.KindTextEnd, uievent.TextEndBody{Text: reply})
		emit(uievent.KindTurnEnd, uievent.TurnEndBody{Reason: "completed"})
	}()
	return &turnHandle{id: id, events: ch, cancel: func() {}}, nil
}

// chunkReply splits a scripted reply into stream-sized pieces, the way
// the main scripts hand-write their deltas: by sentence.
func chunkReply(reply string) []string {
	var out []string
	for _, part := range strings.Split(reply, ". ") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		out = append(out, strings.TrimSpace(part)+". ")
	}
	return out
}

func (c *threadConversation) History() []ports.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ports.Message, len(c.history))
	copy(out, c.history)
	return out
}

func (c *threadConversation) Model() ports.ModelInfo    { return ports.ModelInfo{} }
func (c *threadConversation) ContextUsage() ports.Usage { return ports.Usage{} }
func (c *threadConversation) Title() string             { return c.fix.CallID }
func (c *threadConversation) ID() string                { return c.fix.CallID }
