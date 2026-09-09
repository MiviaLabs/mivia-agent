package uiadapter

import (
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// turnStream owns one turn's event channel and is the ONLY thing allowed
// to send on it or close it.
//
// The channel has two closers (turnHandle.Cancel on the UI goroutine and
// emitTurnEndIfWinner on the turn goroutine) and several senders, the
// hottest being the per-turn agent-event tap, which the agent loop calls
// SYNCHRONOUSLY. An atomic "closed" flag alone cannot make that safe: a
// sender that reads the flag as false can be descheduled and resume after
// a closer has already run, and its send then panics with "send on closed
// channel", killing the whole TUI from the agent-loop goroutine.
//
// The RWMutex closes that window. Senders hold it shared, so they still
// run concurrently with each other; a closer takes it exclusively and
// therefore cannot close while any send is in flight.
//
// Backpressure is deliberately preserved: the send BLOCKS while the
// buffer is full (turnBufferSize), because dropping events would silently
// lose transcript content. The escape hatch is the per-turn context's
// done channel, exactly as before. That makes the close ordering
// load-bearing - see Close.
type turnStream struct {
	mu     sync.RWMutex
	ch     chan uievent.Event
	done   <-chan struct{}
	cancel func()
	closed bool
}

// newTurnStream builds the stream. done is the per-turn context's Done
// channel (the sender's escape hatch) and cancel is that context's cancel
// func, which Close fires BEFORE closing so no sender can still be parked.
func newTurnStream(ch chan uievent.Event, done <-chan struct{}, cancel func()) *turnStream {
	return &turnStream{ch: ch, done: done, cancel: cancel}
}

// Send delivers one event, blocking while the buffer is full, and reports
// whether it was accepted. It returns false once the stream is closed or
// the turn's context is done.
//
// The shared lock is what makes this safe against Close; it is never held
// across anything but this one send, and the done arm guarantees the hold
// is bounded even when the UI has stopped reading.
func (s *turnStream) Send(e uievent.Event) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return false
	}
	select {
	case s.ch <- e:
		return true
	case <-s.done:
		return false
	}
}

// TrySend delivers one event only if it can be accepted immediately,
// reporting whether it was. It is the terminal events' path: a full
// buffer at close time means the reader has already stopped draining, and
// the old code guarded that with a non-blocking send rather than parking
// the turn goroutine forever. That trade is preserved verbatim.
func (s *turnStream) TrySend(e uievent.Event) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return false
	}
	select {
	case s.ch <- e:
		return true
	default:
		return false
	}
}

// Close closes the channel exactly once and reports whether THIS call did
// it, so a caller can behave like the winner of the old compare-and-swap.
//
// Cancelling the turn context FIRST is required, not incidental: a sender
// parked on a full buffer holds the shared lock until it either completes
// or observes done. Taking the exclusive lock before cancelling would
// wait on that sender while that sender waits on a reader that may never
// come - a deadlock in place of the old panic. Cancelling first
// guarantees every parked sender unparks, so the exclusive lock is always
// obtainable.
func (s *turnStream) Close() bool {
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.closed = true
	close(s.ch)
	return true
}

// Closed reports whether the stream has been closed. It is advisory only:
// a caller that acts on a false result must still go through Send, which
// re-checks under the lock.
func (s *turnStream) Closed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}
