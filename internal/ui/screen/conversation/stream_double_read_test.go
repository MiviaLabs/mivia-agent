package conversation

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// stableHandle is a TurnHandle whose Events() returns the SAME channel on
// every call, which is what the real turnHandle does
// (internal/uiadapter/conversation.go: `return h.events`). fakeHandle
// mints a fresh closed channel per call, so it cannot expose a
// double-reader defect; this one can.
type stableHandle struct {
	id string
	ch chan uievent.Event
}

func newStableHandle(id string) *stableHandle {
	return &stableHandle{id: id, ch: make(chan uievent.Event, 8)}
}

func (h *stableHandle) ID() string                   { return h.id }
func (h *stableHandle) Events() <-chan uievent.Event { return h.ch }
func (h *stableHandle) Cancel()                      {}
func (h *stableHandle) CancelToolCall(string) bool   { return false }

// countReaders reports how many independent read continuations a Cmd
// arms against ch. Each reader consumes exactly one event, so pushing
// distinct events and counting how many are taken before the channel
// stops draining measures the number of concurrent readers.
func countReaders(cmd tea.Cmd, ch chan uievent.Event) int {
	if cmd == nil {
		return 0
	}
	// Fill the channel with more events than any correct implementation
	// could consume with one reader.
	for i := 0; i < 4; i++ {
		ch <- uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "x"}}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runLeaves(cmd)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	// Whatever is left un-drained tells how many were taken.
	return 4 - len(ch)
}

// runLeaves executes a Cmd tree to completion, expanding batches.
func runLeaves(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		done := make(chan struct{}, len(batch))
		for _, c := range batch {
			go func(c tea.Cmd) { defer func() { done <- struct{}{} }(); runLeaves(c) }(c)
		}
		for range batch {
			select {
			case <-done:
			case <-time.After(time.Second):
				return
			}
		}
	}
}

// TestStaleTurnEventDoesNotDoubleReadTheActiveStream pins that an event
// which did NOT come from the active turn's channel cannot arm a second
// reader on that channel.
//
// handleTurnEvent re-armed unconditionally on s.active.Events(). A real
// turnHandle returns the same channel every call, so an event arriving
// from ANY other source - a superseded turn's channel still draining
// after the handle was replaced, or a session whose registry entry moved
// - added one more permanent read continuation to the LIVE channel. Two
// readers then compete for every subsequent event, and each re-arms on
// delivery, so the reader count never falls back.
func TestStaleTurnEventDoesNotDoubleReadTheActiveStream(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)

	active := newStableHandle("turn-live")
	s.active = active

	// An event from a DIFFERENT channel than the active turn's: the
	// superseded turn's stream, still delivering after s.active moved on.
	stale := make(chan uievent.Event, 4)
	next, cmd := s.Update(uievent.EventMsg{
		Event:  uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "stale"}},
		Source: stale,
	})
	if _, ok := next.(Screen); !ok {
		t.Fatalf("Update returned %T, want Screen", next)
	}

	if n := countReaders(cmd, active.ch); n > 0 {
		t.Fatalf("an event from a foreign channel armed %d reader(s) on the ACTIVE turn's channel, want 0", n)
	}
}

// TestStaleTurnEventKeepsDrainingItsOwnStream pins the other half of the
// stale-source rule: the re-arm follows the event back to its own
// channel. Dropping it instead would strand the writer - which may be the
// agent loop's synchronous event tap - blocking once its buffer fills,
// the same hazard handleEventMsg's untracked-session path guards against.
func TestStaleTurnEventKeepsDrainingItsOwnStream(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = newStableHandle("turn-live")

	stale := make(chan uievent.Event, 8)
	_, cmd := s.Update(uievent.EventMsg{
		Event:  uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "stale"}},
		Source: stale,
	})

	if n := countReaders(cmd, stale); n != 1 {
		t.Fatalf("stale stream got %d reader(s) on its OWN channel, want exactly 1 (its writer would otherwise block)", n)
	}
}

// TestActiveTurnEventArmsExactlyOneReader pins the other half: an event
// that DID come from the active channel keeps the read loop alive with
// exactly one continuation, so the fix above cannot be satisfied by
// simply never re-arming.
func TestActiveTurnEventArmsExactlyOneReader(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)

	active := newStableHandle("turn-live")
	s.active = active

	_, cmd := s.Update(uievent.EventMsg{
		Event:  uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "live"}},
		Source: active.ch,
	})

	if n := countReaders(cmd, active.ch); n != 1 {
		t.Fatalf("an event from the active channel armed %d reader(s), want exactly 1", n)
	}
}
