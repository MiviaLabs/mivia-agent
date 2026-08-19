// Package replay implements ports.Conversation/TurnHandle/Approver over a
// fixed sequence of uievent.Event, replayed on every Send call. It is the
// "replay fake" build spec step 6 names: what components and the demo
// binary drive against before the real internal/chat adapter exists
// (deferred to a later step). It takes its event data as a parameter
// rather than owning a fixture file, so it has no opinion about where
// that data lives.
package replay

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

var (
	_ ports.Conversation = (*Conversation)(nil)
	_ ports.TurnHandle   = (*turnHandle)(nil)
)

// Conversation replays the same fixed events slice for every Send call,
// regardless of the intent text: it is a fixture player, not a model.
type Conversation struct {
	events []uievent.Event
	pace   time.Duration // inter-event delay; 0 replays as fast as possible

	mu    sync.Mutex
	turns int
}

// New returns a Conversation that replays events for every Send call,
// spaced pace apart (0 for no delay, the common case in tests).
func New(events []uievent.Event, pace time.Duration) *Conversation {
	out := make([]uievent.Event, len(events))
	copy(out, events)
	return &Conversation{events: out, pace: pace}
}

// Send starts a new replay of the fixed event sequence. The returned
// handle's TurnID stamps every event, overriding whatever TurnID the
// fixture recorded, so concurrent Send calls (were the fake to allow
// them) would still fence correctly against each other.
func (c *Conversation) Send(ctx context.Context, _ intent.Send) (ports.TurnHandle, error) {
	c.mu.Lock()
	c.turns++
	id := fmt.Sprintf("replay-%d", c.turns)
	c.mu.Unlock()

	ch := make(chan uievent.Event)
	cancelCh := make(chan struct{})
	var once sync.Once
	cancel := func() { once.Do(func() { close(cancelCh) }) }

	go func() {
		defer close(ch)
		for _, ev := range c.events {
			ev.TurnID = id
			select {
			case ch <- ev:
			case <-cancelCh:
				return
			case <-ctx.Done():
				return
			}
			if c.pace <= 0 {
				continue
			}
			select {
			case <-time.After(c.pace):
			case <-cancelCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	return &turnHandle{id: id, events: ch, cancel: cancel}, nil
}

// History, Model, and ContextUsage are static: the replay fake has no
// real conversation state to report.
func (c *Conversation) History() []ports.Message { return nil }

func (c *Conversation) Model() ports.ModelInfo {
	return ports.ModelInfo{Name: "replay", Provider: "fixture"}
}

func (c *Conversation) ContextUsage() ports.Usage { return ports.Usage{} }

type turnHandle struct {
	id     string
	events <-chan uievent.Event
	cancel func()
}

func (h *turnHandle) ID() string                   { return h.id }
func (h *turnHandle) Events() <-chan uievent.Event { return h.events }
func (h *turnHandle) Cancel()                      { h.cancel() }
