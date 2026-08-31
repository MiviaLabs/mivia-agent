package hub

import (
	"context"
	"net"
	"sync/atomic"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// client is a workspace's hub membership for a process that did not win
// election: it forwards this process's own EventBus to the owner (which
// rebroadcasts it to everyone else) and renders whatever the owner sends
// back through sink.
type client struct {
	c    *conn
	sess *chat.Session
	sink Sink
	sub  *events.Subscription
}

func newClient(nc net.Conn, sess *chat.Session, sink Sink) *client {
	return &client{c: newConn(nc), sess: sess, sink: sink}
}

// run starts the read/write loops and the local EventBus forwarding
// subscription. Blocks until the connection closes (owner exited, or ctx
// cancelled) - callers run it on their own goroutine and treat return as
// "the hub is gone, go re-elect."
func (cl *client) run(ctx context.Context) {
	cl.subscribeRelay()
	go cl.c.writeLoop()
	go func() {
		<-ctx.Done()
		cl.c.close()
	}()
	cl.c.readLoop(func(w WireEvent) {
		if cl.sink != nil {
			cl.sink(fromWire(w), Receipt{Dropped: w.Dropped})
		}
	})
}

// subscribeRelay registers the forwarding handler for every relayed kind as ONE
// ordered subscription, mirroring owner.subscribeRelay. It is separate from run
// so it can be exercised without the socket loops.
func (cl *client) subscribeRelay() {
	if cl.sess.EventBus == nil {
		return
	}
	// Atomic holder for the same reason owner.subscribeRelay uses one: the
	// handler runs before SubscribeAcross has returned the handle.
	var ref atomic.Pointer[events.Subscription]
	sub := cl.sess.EventBus.SubscribeAcross(relayedKinds, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		w := toWire(ev)
		w.Dropped = ref.Load().Drops()
		cl.c.send(w)
	}), events.SubscribeOptions{BufSize: relayBufSize})
	ref.Store(sub)
	cl.sub = sub
}

func (cl *client) stop() {
	cl.sub.Unsubscribe()
	cl.c.close()
}

// runAsClient blocks until the connection to storeDir's hub closes or ctx is
// cancelled. Returns immediately (no error) if no hub is reachable, so the
// caller's retry loop falls through to the next election attempt without a
// long stall.
func runAsClient(ctx context.Context, storeDir string, sess *chat.Session, sink Sink) {
	nc, err := dial(storeDir)
	if err != nil {
		return
	}
	cl := newClient(nc, sess, sink)
	cl.run(ctx)
	cl.stop()
}
