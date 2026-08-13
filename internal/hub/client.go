package hub

import (
	"context"
	"net"

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
	sub  events.HandlerFunc
}

func newClient(nc net.Conn, sess *chat.Session, sink Sink) *client {
	return &client{c: newConn(nc), sess: sess, sink: sink}
}

// run starts the read/write loops and the local EventBus forwarding
// subscription. Blocks until the connection closes (owner exited, or ctx
// cancelled) - callers run it on their own goroutine and treat return as
// "the hub is gone, go re-elect."
func (cl *client) run(ctx context.Context) {
	cl.sub = func(_ context.Context, ev events.Event) {
		cl.c.send(toWire(ev))
	}
	if cl.sess.EventBus != nil {
		cl.sess.EventBus.SubscribeMany(relayedKinds, cl.sub)
	}
	go cl.c.writeLoop()
	go func() {
		<-ctx.Done()
		cl.c.close()
	}()
	cl.c.readLoop(func(w WireEvent) {
		if cl.sink != nil {
			cl.sink(fromWire(w))
		}
	})
}

func (cl *client) stop() {
	if cl.sess.EventBus != nil && cl.sub != nil {
		for _, k := range relayedKinds {
			cl.sess.EventBus.Unsubscribe(k, cl.sub)
		}
	}
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
