// This file (conversation_live.go) holds the live-viewer machinery on
// Conversation: the viewer registry SubscribeLive serves, the fan-out
// the turn stream's tee calls, and the background/foreground ownership
// marks the registrar gate and the UI's send guard read. Split out of
// conversation.go, which had crossed the hard per-file line budget.
package uiadapter

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// liveViewerBufferSize is each live viewer's event buffer. It matches
// the per-turn channel's: the tee is non-blocking, so a smaller buffer
// would overflow on bursts the primary channel absorbs.
const liveViewerBufferSize = turnBufferSize

// liveViewer is one subscribed view of the conversation. The events
// channel is NEVER closed by the publisher - a viewer that stops
// reading calls Close and stops; done and stale are the only lifecycle
// signals, each closed exactly once.
type liveViewer struct {
	events    chan uievent.Event
	done      chan struct{}
	stale     chan struct{}
	doneOnce  sync.Once
	staleOnce sync.Once
	drop      atomic.Bool
}

// liveSubscription is the viewer-side handle. Close is idempotent.
type liveSubscription struct {
	c    *Conversation
	id   int64
	v    *liveViewer
	once sync.Once
}

func (s *liveSubscription) Done() <-chan struct{} { return s.v.done }

func (s *liveSubscription) Stale() <-chan struct{} { return s.v.stale }

func (s *liveSubscription) Close() {
	s.once.Do(func() { s.c.removeViewer(s.id, s.v) })
}

// SetForeground marks c as the UI's active session (see the foreground
// field doc). The screen calls this on every session switch.
func (c *Conversation) SetForeground(on bool) {
	if c == nil {
		return
	}
	c.foreground.Store(on)
	c.syncProgressRegistration()
}

// IsForeground reports whether a UI currently owns c as its active
// session.
func (c *Conversation) IsForeground() bool {
	if c == nil {
		return false
	}
	return c.foreground.Load()
}

// SetBackground marks c as background (an automation run) or
// foreground. A background conversation is by definition not owned by
// the screen, so this clears foreground too; a screen that later adopts
// the conversation calls SetForeground(true) itself.
func (c *Conversation) SetBackground(on bool) {
	if c == nil {
		return
	}
	c.background.Store(on)
	if on {
		c.foreground.Store(false)
	}
	c.syncProgressRegistration()
}

// SubscribeLive registers a read-only viewer for this conversation's
// turn events. The returned channel receives every event the per-turn
// streams deliver from now on (never anything already delivered - a
// viewer that attaches mid-turn paints History() first and self-heals
// text at text.end). The channel is never closed; Done and Stale on the
// subscription are the lifecycle signals. Overflowing the buffer marks
// the subscription stale once and drops its events from then on: the
// viewer reloads history and resubscribes. Safe to call from any
// goroutine.
func (c *Conversation) SubscribeLive() (<-chan uievent.Event, ports.LiveSubscription) {
	v := &liveViewer{
		events: make(chan uievent.Event, liveViewerBufferSize),
		done:   make(chan struct{}),
		stale:  make(chan struct{}),
	}
	c.viewMu.Lock()
	defer c.viewMu.Unlock()
	if c.viewers == nil {
		c.viewers = make(map[int64]*liveViewer)
	}
	c.viewSeq++
	id := c.viewSeq
	c.viewers[id] = v
	return v.events, &liveSubscription{c: c, id: id, v: v}
}

// removeViewer unregisters one viewer and closes its done channel.
func (c *Conversation) removeViewer(id int64, v *liveViewer) {
	c.viewMu.Lock()
	delete(c.viewers, id)
	c.viewMu.Unlock()
	v.closeDone()
}

func (v *liveViewer) closeDone() {
	v.doneOnce.Do(func() { close(v.done) })
}

// broadcast fans one accepted event out to every live viewer. Called
// with the turn stream's RLock held, so it must never block: a full
// viewer buffer marks that viewer stale once and drops its events from
// then on (the viewer reloads history and resubscribes).
func (c *Conversation) broadcast(e uievent.Event) {
	c.viewMu.Lock()
	defer c.viewMu.Unlock()
	for _, v := range c.viewers {
		if v.drop.Load() {
			continue
		}
		select {
		case v.events <- e:
		default:
			v.drop.Store(true)
			v.staleOnce.Do(func() { close(v.stale) })
		}
	}
}

// IsBackground reports whether c is marked background.
func (c *Conversation) IsBackground() bool {
	if c == nil {
		return false
	}
	return c.background.Load()
}

// emitSyntheticTurnStart sends the leading KindTurnStart with Seq=1 and
// TurnID="" through the turn stream (and therefore to any live viewer)
// so no agent event can race ahead of it on the per-turn channel. The
// channel buffer is sized to hold this event without blocking.
//
// The empty TurnID is the documented "empty-TurnID window" (see the
// package doc in event.go): chat.Session only surfaces the real ID
// after SendUserWithEvent returns, so the tap-installed events stamp
// the real ID via a shared atomic.Pointer once known. The terminal
// KindTurnEnd emitted by emitTurnEndIfWinner carries the real ID
// unconditionally, so renderers that index by TurnID should defer
// indexing until they see that event.
func emitSyntheticTurnStart(stream *turnStream, input string, seq *uint64) {
	atomic.AddUint64(seq, 1)
	stream.SendInitial(uievent.Event{
		Kind:   uievent.KindTurnStart,
		TurnID: "",
		Seq:    atomic.LoadUint64(seq),
		At:     time.Now(),
		Body:   uievent.TurnStartBody{Input: input},
	})
}
