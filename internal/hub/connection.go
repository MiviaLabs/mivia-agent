package hub

import (
	"bufio"
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
)

// connBufSize bounds one connection's outbound queue. Overflow policy is
// drop-oldest, matching internal/events.Bus's own backpressure philosophy:
// a slow or stalled reader on one connection must never block delivery to
// everyone else.
const connBufSize = 256

// conn wraps one hub socket connection - either the owner's view of a
// connected client, or a client's view of the owner - with a bounded,
// drop-oldest outbound queue.
type conn struct {
	nc  net.Conn
	out chan WireEvent
	// dropped counts events this connection discarded because its outbound
	// queue was full. It exists because drop-oldest here was previously
	// SILENT: internal/events at least exposes Drops(), while the connection
	// shed events with no counter at all, so a consumer could not tell a quiet
	// turn from a lossy one.
	dropped   atomic.Uint64
	done      chan struct{}
	closeOnce sync.Once
}

func newConn(nc net.Conn) *conn {
	return &conn{nc: nc, out: make(chan WireEvent, connBufSize), done: make(chan struct{})}
}

// send enqueues ev for delivery, dropping the oldest queued event first if
// the writer has fallen behind. Never blocks.
//
// ev.Dropped arrives carrying whatever loss happened UPSTREAM of this
// connection (the shared relay queue); send adds this connection's own running
// total, so the value the far end reads is the total number of events this hub
// failed to deliver to it. The count is stamped at enqueue time, so an event
// that is itself later dropped takes its snapshot with it - harmless, because
// the total is cumulative and any surviving later event carries a value at
// least as large.
func (c *conn) send(ev WireEvent) {
	// Stamp BEFORE the first attempt, not only on the retry path: the happy
	// path must still report loss this connection took earlier, or the count
	// would fall back to the upstream-only value as soon as the reader caught
	// up. A counter that can go down is worse than none, because a consumer
	// diffing it would read the recovery as "no loss".
	ev.Dropped += c.dropped.Load()
	select {
	case c.out <- ev:
		return
	default:
	}
	select {
	case <-c.out:
		// Count it on both the connection and THIS event, so the event that
		// displaced one reports it immediately rather than one event later.
		c.dropped.Add(1)
		ev.Dropped++
	default:
	}
	select {
	case c.out <- ev:
	default:
	}
}

// Dropped returns the cumulative number of events this connection discarded.
func (c *conn) Dropped() uint64 { return c.dropped.Load() }

// writeLoop drains c.out to the socket until the connection or c.done
// closes. Run it in its own goroutine.
func (c *conn) writeLoop() {
	enc := json.NewEncoder(c.nc)
	for {
		select {
		case ev, ok := <-c.out:
			if !ok {
				return
			}
			if err := enc.Encode(ev); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

// readLoop decodes newline-delimited WireEvent JSON from the socket,
// calling onReceive for each, until the connection closes or a decode error
// occurs. A malformed line is skipped, not fatal: one bad line (a future
// protocol mismatch) shouldn't sever the whole session. Runs on the calling
// goroutine - callers spawn it themselves.
func (c *conn) readLoop(onReceive func(WireEvent)) {
	sc := bufio.NewScanner(c.nc)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var w WireEvent
		if err := json.Unmarshal(sc.Bytes(), &w); err != nil {
			continue
		}
		onReceive(w)
	}
}

// close is safe to call concurrently and more than once: client.go's ctx.Done
// watcher and its normal run()/stop() sequence can both race to close the
// same conn (owner exit and ctx cancellation landing together), and a
// check-then-close on c.done without synchronization let two callers both
// pass the check before either closed the channel, panicking on the second
// close.
func (c *conn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.nc.Close()
	})
}
