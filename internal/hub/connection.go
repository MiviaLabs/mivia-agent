package hub

import (
	"bufio"
	"encoding/json"
	"net"
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
	nc   net.Conn
	out  chan WireEvent
	done chan struct{}
}

func newConn(nc net.Conn) *conn {
	return &conn{nc: nc, out: make(chan WireEvent, connBufSize), done: make(chan struct{})}
}

// send enqueues ev for delivery, dropping the oldest queued event first if
// the writer has fallen behind. Never blocks.
func (c *conn) send(ev WireEvent) {
	select {
	case c.out <- ev:
		return
	default:
	}
	select {
	case <-c.out:
	default:
	}
	select {
	case c.out <- ev:
	default:
	}
}

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

func (c *conn) close() {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	_ = c.nc.Close()
}
