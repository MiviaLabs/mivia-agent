package events

// Cross-kind ordered subscription.
//
// SubscribeMany registers one subscription PER kind (bus.go), so a handler
// covering several kinds gets N queues and N delivery goroutines racing into
// it. Order therefore holds WITHIN a kind and not between kinds: a turn's
// terminal routinely reaches the handler before the assistant deltas it
// terminates, even though the producers published them in causal order on one
// goroutine.
//
// That is not a theoretical hazard. It is why internal/hub withheld
// KindTurnEnd and KindError from its relayed set for as long as it did: a
// remote viewer that saw a terminal before the content it terminates would
// close the turn early and then render text into a turn it had already closed.
// It relays them now, on one SubscribeAcross subscription.
//
// SubscribeAcross registers ONE subscription under many kinds. One queue, one
// delivery goroutine, so the handler observes the order Publish was called in.

// SubscribeOptions configures a SubscribeAcross registration. The zero value
// selects the same defaults as Subscribe.
type SubscribeOptions struct {
	// BufSize is the subscription's queue capacity. Zero or negative selects
	// defaultBufSize.
	BufSize int
}

// Subscription is a handle to one registration, returned by SubscribeAcross.
//
// It exists because the per-kind accessors cannot describe a subscription that
// spans kinds: unsubscribing it means removing it from every kind it was
// registered under, and Bus.Unsubscribe takes exactly one. It also makes the
// drop and panic counters reachable, which they were not from outside this
// package at all.
//
// Every method tolerates a handle for a registration that never happened (a nil
// handler, an empty kind list, or a closed bus), so callers never nil-check.
type Subscription struct {
	b *Bus
	s *subscription
}

// SubscribeAcross registers h for every kind in kinds as a SINGLE subscription,
// so the handler observes events in publish order across those kinds.
//
// The bus cannot invent an order its callers never had, so the guarantee is per
// publisher: events published from one goroutine arrive in that goroutine's
// order. The turn lifecycle satisfies that, which is all this needs to do.
//
// Two costs, both deliberate. One queue means one budget: a low-volume kind
// that used to have a private 256 slots now competes with per-write assistant
// deltas, so a caller spanning many kinds should size the queue with
// SubscribeOptions. The trade is honest, because a merged queue drops a PREFIX
// - in order, and visible through Drops - instead of reordering, which is
// neither. One goroutine means one slow handler stalls all of its kinds rather
// than one; every handler on this path today is non-blocking, so that is
// currently theoretical, but it is the property a future handler will violate.
//
// Duplicate kinds are collapsed, so a list assembled from several sources does
// not deliver twice. It returns a usable handle even when nothing was
// registered.
//
// The returned handle's Unsubscribe joins the delivery goroutine, so a handler
// must not call it from inside HandleEvent - use DeliveryFrom(ctx).Unsubscribe,
// exactly as with Bus.Unsubscribe.
func (b *Bus) SubscribeAcross(kinds []Kind, h Handler, opts ...SubscribeOptions) *Subscription {
	var opt SubscribeOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	unique := dedupeKinds(kinds)
	if h == nil || len(unique) == 0 {
		return &Subscription{}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return &Subscription{}
	}
	sub := newSubscription(b.ctx, b, h, opt.BufSize)
	sub.kinds = unique
	for _, k := range unique {
		b.subs[k] = append(b.subs[k], sub)
	}
	// Once per SUBSCRIPTION, not once per kind: the counter tracks delivery
	// goroutines, and this registration started exactly one. Adding per kind
	// would leave Close waiting forever for goroutines that never existed.
	b.wg.Add(1)
	go func() {
		<-sub.done
		b.wg.Done()
	}()
	return &Subscription{b: b, s: sub}
}

// Drops returns the number of events this subscription dropped because its
// queue was full.
//
// The honest limit: this counts loss on the LOCAL bus only. A consumer reading
// the relayed stream in another process cannot see it, and there is a second
// silent loss point on the connection's own send path that this counter does
// not cover either. It tells the process that owns the bus that it is shedding
// events; it does not make that loss recoverable for anyone downstream.
//
// Reading it AFTER Unsubscribe can also over-report. Publish snapshots the
// subscriber slice under the lock and enqueues outside it, so a publish already
// in flight can still land in a removed subscription's queue and be dropped
// there. Nothing was lost - no handler was ever going to run for it - but the
// counter moves. Read it while the subscription is live.
func (h *Subscription) Drops() uint64 {
	if h == nil || h.s == nil {
		return 0
	}
	return h.s.Drops()
}

// Panics returns the number of handler panics this subscription has contained.
func (h *Subscription) Panics() uint64 {
	if h == nil || h.s == nil {
		return 0
	}
	return h.s.Panics()
}

// Unsubscribe removes the subscription from EVERY kind it was registered under
// and waits for its delivery goroutine to drain and exit. It is idempotent, and
// safe after the bus is closed.
func (h *Subscription) Unsubscribe() {
	if h == nil || h.b == nil || h.s == nil {
		return
	}
	h.b.mu.Lock()
	h.b.removeSubLocked(h.s)
	h.b.mu.Unlock()
	// Join outside the lock, for the reason Bus.Unsubscribe documents: the
	// delivery goroutine may run handlers that call back into the bus.
	h.s.stop()
}

// removeSubLocked removes s from every kind it was registered under. The caller
// must hold b.mu.
//
// Removing from only ONE kind is the hazard a multi-kind subscription
// introduces: the remaining registrations keep a live pointer to a subscription
// whose delivery goroutine has stopped, so trySend keeps enqueueing into a
// queue nobody drains and drop-oldest discards it all, silently.
func (b *Bus) removeSubLocked(s *subscription) {
	if b.subs == nil || s == nil {
		return
	}
	// s.kinds is the ONLY source here. There used to be a fallback that scanned
	// every kind when it was empty, which sounded defensive and was worse than
	// useless: it was unreachable (both registration paths set kinds before the
	// subscription is published to b.subs), and it silently covered for
	// Subscribe forgetting to record its own kind - so neither guard had a test
	// that failed on its own. See TestSubscribeRecordsItsKind.
	for _, k := range s.kinds {
		subs := b.subs[k]
		for i, cur := range subs {
			if cur == s {
				b.subs[k] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
	}
}

// dedupeKinds returns kinds with duplicates and empty entries removed,
// preserving first-seen order.
func dedupeKinds(kinds []Kind) []Kind {
	if len(kinds) == 0 {
		return nil
	}
	seen := make(map[Kind]struct{}, len(kinds))
	out := make([]Kind, 0, len(kinds))
	for _, k := range kinds {
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}
