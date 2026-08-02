package events

import (
	"context"
	"sync/atomic"
)

// defaultBufSize is the per-subscriber queue capacity when Subscribe is used
// without explicit options. 256 is large enough for bursty publish patterns
// (e.g. tool_parallel, subagent_heartbeat bursts) while bounding unbounded
// memory growth from a slow consumer.
const defaultBufSize = 256

// subscription manages the async delivery channel and goroutine for one
// (Kind, Handler) pair registered on the Bus.
type subscription struct {
	handler Handler
	ch      chan Event
	drops   atomic.Uint64 // drop-oldest counter
	cancel  context.CancelFunc
	done    chan struct{}      // closed when delivery goroutine exits
	flushCh chan chan struct{} // dedicated channel for flush barriers
}

func newSubscription(ctx context.Context, h Handler, bufSize int) *subscription {
	if bufSize <= 0 {
		bufSize = defaultBufSize
	}
	s := &subscription{
		handler: h,
		ch:      make(chan Event, bufSize),
		done:    make(chan struct{}),
		flushCh: make(chan chan struct{}, 1), // small buffer for flush signals
	}
	ctx, s.cancel = context.WithCancel(ctx)
	go s.deliver(ctx)
	return s
}

// deliver reads from the subscriber's event channel and calls HandleEvent.
// It also monitors the flush channel to support the Bus.Flush() synchronization
// mechanism. It exits when ctx is cancelled.
func (s *subscription) deliver(ctx context.Context) {
	defer close(s.done)
	for {
		select {
		case ev, ok := <-s.ch:
			if !ok {
				return
			}
			s.handler.HandleEvent(ctx, ev)
		case reply := <-s.flushCh:
			// Flush barrier: drain all queued events first, then
			// close the reply channel to signal completion.
			s.drain(ctx)
			close(reply)
		case <-ctx.Done():
			s.drain(ctx)
			return
		}
	}
}

// drain processes all events currently buffered in the event channel.
func (s *subscription) drain(ctx context.Context) {
	for {
		select {
		case ev, ok := <-s.ch:
			if !ok {
				return
			}
			s.handler.HandleEvent(ctx, ev)
		case reply := <-s.flushCh:
			close(reply)
		default:
			return
		}
	}
}

// Drops returns the number of events dropped due to a full queue.
func (s *subscription) Drops() uint64 {
	return s.drops.Load()
}

// trySend enqueues an event to the subscriber's channel. If the channel is
// full, it drops the oldest event (receive one, discard it, then send).
func (s *subscription) trySend(ev Event) {
	for {
		select {
		case s.ch <- ev:
			return
		default:
			// Channel full: drop oldest to make room.
			select {
			case <-s.ch:
				s.drops.Add(1)
			default:
				// Channel was drained between the two selects; retry send.
				continue
			}
		}
	}
}

// flushSend sends a flush barrier to the delivery goroutine and waits for
// acknowledgment. Unlike event sends, flush barriers use a dedicated channel
// that is never subject to drop-oldest, ensuring Flush() never deadlocks.
func (s *subscription) flushSend() {
	reply := make(chan struct{})
	select {
	case s.flushCh <- reply:
		<-reply
	case <-s.done:
		// Goroutine already exited.
	}
}

// stop cancels the delivery context and waits for the goroutine to exit.
func (s *subscription) stop() {
	s.cancel()
	<-s.done
}
