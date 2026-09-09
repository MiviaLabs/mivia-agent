package hub

import (
	"context"
	"net"
	"sync"
	"sync/atomic"

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
	sub     *events.Subscription
	// srcLoss is the last cumulative loss count seen from each client, and
	// loss is this process's own accumulated total across all of them. A
	// client's counter is meaningful only relative to ITSELF, so the owner
	// folds per-source DELTAS into one total rather than forwarding whichever
	// client's absolute number arrived last. Guarded by mu.
	srcLoss map[uint64]uint64
	loss    uint64
}

func newOwner(sess *chat.Session, sink Sink) *owner {
	return &owner{
		clients: make(map[uint64]*conn), sess: sess, sink: sink,
		srcLoss: make(map[uint64]uint64),
	}
}

// noteSourceLoss folds one client's cumulative counter into this process's own
// total and returns it. Only the forward delta counts: a client that reconnects
// restarts at zero, and a decrease must never subtract from a total that is
// documented as monotonic.
func (o *owner) noteSourceLoss(id, reported uint64) uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	if prev, ok := o.srcLoss[id]; !ok || reported > prev {
		if ok {
			o.loss += reported - prev
		} else {
			o.loss += reported
		}
		o.srcLoss[id] = reported
	}
	return o.loss
}

// start subscribes to the session's own event bus and accepts connections
// on ln until ctx is cancelled or Accept fails. Blocks - run it on its own
// goroutine.
func (o *owner) start(ctx context.Context, ln net.Listener) {
	o.subscribeRelay()
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

// subscribeRelay registers the fan-out handler for every relayed kind as ONE
// ordered subscription. It is separate from start so it can be exercised
// without a listener.
func (o *owner) subscribeRelay() {
	if o.sess.EventBus == nil {
		return
	}
	// The handler needs the handle SubscribeAcross has not returned yet - it
	// starts the delivery goroutine before assigning - so route the read
	// through an atomic holder rather than the o.sub field, which the handler
	// and this goroutine would otherwise race on. A nil load reads 0 drops,
	// which is the truth for an event delivered before the handle existed.
	var ref atomic.Pointer[events.Subscription]
	sub := o.sess.EventBus.SubscribeAcross(relayedKinds, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		w := toWire(ev)
		// Charge the relay queue's loss only to events that actually passed
		// through it. A rebroadcast from another client (accept's read loop)
		// never touched this subscription, so counting it there would report
		// loss that consumer did not take.
		w.Dropped = ref.Load().Drops()
		o.broadcast(0, w)
	}), events.SubscribeOptions{BufSize: relayBufSize})
	ref.Store(sub)
	o.sub = sub
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
			mine := o.noteSourceLoss(id, w.Dropped)
			// NEVER forward a foreign counter. Dropped describes the path to
			// ONE consumer; re-fanning the sender's absolute total makes the
			// receiving consumer's stream interleave two unrelated counter
			// origins, so it can fall - and a receiver holding a high-water
			// mark then swallows every later real loss below the foreign peak.
			// A rebroadcast reports only what THIS hub loses on the way out,
			// which conn.send adds.
			w.Dropped = 0
			o.broadcast(id, w)
			if o.sink != nil {
				o.sink(fromWire(w), Receipt{Dropped: mine})
			}
		})
		o.mu.Lock()
		delete(o.clients, id)
		delete(o.srcLoss, id)
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
	// Unsubscribe BEFORE taking o.mu: it joins the delivery goroutine, and that
	// goroutine's handler takes o.mu to broadcast. Holding the lock across the
	// join would deadlock the two against each other.
	o.sub.Unsubscribe()
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
