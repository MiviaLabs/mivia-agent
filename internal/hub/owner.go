package hub

import (
	"context"
	"net"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/gofrs/flock"
)

// owner is a workspace's hub: the process that won election. It accepts
// client connections, republishes this process's own EventBus to every
// connected client, and rebroadcasts whatever one client sends to every
// OTHER connected client - so two terminal TUIs both connected to one
// desktop-owned hub still see each other, not just the owner.
type owner struct {
	mu      sync.Mutex
	clients map[uint64]*conn
	nextID  uint64
	sess    *chat.Session
	sink    Sink
	sub     events.HandlerFunc
}

func newOwner(sess *chat.Session, sink Sink) *owner {
	return &owner{clients: make(map[uint64]*conn), sess: sess, sink: sink}
}

// start subscribes to the session's own event bus and accepts connections
// on ln until ctx is cancelled or Accept fails. Blocks - run it on its own
// goroutine.
func (o *owner) start(ctx context.Context, ln net.Listener) {
	o.sub = func(_ context.Context, ev events.Event) {
		o.broadcast(0, toWire(ev))
	}
	if o.sess.EventBus != nil {
		o.sess.EventBus.SubscribeMany(relayedKinds, o.sub)
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		o.accept(nc)
	}
}

// accept registers nc as a new client connection and starts its read/write
// loops. The read loop both rebroadcasts what the client sends (so other
// clients see it) and hands it to sink (so THIS process's own live surface
// sees it too, exactly like an event this process received as a client).
func (o *owner) accept(nc net.Conn) {
	c := newConn(nc)
	o.mu.Lock()
	o.nextID++
	id := o.nextID
	o.clients[id] = c
	o.mu.Unlock()
	go c.writeLoop()
	go func() {
		c.readLoop(func(w WireEvent) {
			o.broadcast(id, w)
			if o.sink != nil {
				o.sink(fromWire(w))
			}
		})
		o.mu.Lock()
		delete(o.clients, id)
		o.mu.Unlock()
		c.close()
	}()
}

// broadcast fans ev out to every connected client except originID (0 for
// "this process's own event", which has no client to exclude).
func (o *owner) broadcast(originID uint64, ev WireEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for id, c := range o.clients {
		if id == originID {
			continue
		}
		c.send(ev)
	}
}

// stop unsubscribes from the local event bus and closes every client
// connection, so clients notice promptly (a fresh read error) rather than
// waiting on a TCP/pipe-level timeout.
func (o *owner) stop() {
	if o.sess.EventBus != nil && o.sub != nil {
		for _, k := range relayedKinds {
			o.sess.EventBus.Unsubscribe(k, o.sub)
		}
	}
	o.mu.Lock()
	clients := make([]*conn, 0, len(o.clients))
	for _, c := range o.clients {
		clients = append(clients, c)
	}
	o.clients = make(map[uint64]*conn)
	o.mu.Unlock()
	for _, c := range clients {
		c.close()
	}
}

// runAsOwner blocks for the life of ctx, serving as storeDir's hub. lock is
// released on return so another process can take over.
func runAsOwner(ctx context.Context, storeDir string, lock *flock.Flock, sess *chat.Session, sink Sink) {
	defer func() { _ = lock.Unlock() }()
	ln, err := listen(storeDir)
	if err != nil {
		return
	}
	o := newOwner(sess, sink)
	o.start(ctx, ln)
	o.stop()
}
