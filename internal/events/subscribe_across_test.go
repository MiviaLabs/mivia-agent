package events

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// orderRecorder appends a label for every event it receives, in the order its
// delivery goroutine ran them.
type orderRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *orderRecorder) HandleEvent(_ context.Context, ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, string(ev.Kind)+":"+ev.Content)
}

func (r *orderRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.seen))
	copy(out, r.seen)
	return out
}

// turnScript renders the causal publish order a chat turn produces: a
// turn_start, some assistant deltas, then a terminal. Producers emit these in
// this order on one goroutine (internal/chat publishes the boundaries around
// internal/agent's synchronous delta writes), so this IS the order a consumer
// must see.
func turnScript(turns, deltas int) []Event {
	var evs []Event
	for t := 0; t < turns; t++ {
		id := fmt.Sprintf("turn:%d", t)
		evs = append(evs, Event{Kind: KindTurnStart, TurnID: id, Content: id + "/start"})
		for d := 0; d < deltas; d++ {
			evs = append(evs, Event{Kind: KindAssistant, TurnID: id, Content: fmt.Sprintf("%s/delta%d", id, d)})
		}
		evs = append(evs, Event{Kind: KindTurnEnd, TurnID: id, Content: id + "/end"})
	}
	return evs
}

func labelsOf(evs []Event) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, string(ev.Kind)+":"+ev.Content)
	}
	return out
}

// TestSubscribeAcrossPreservesCrossKindOrder is the reason SubscribeAcross
// exists.
//
// SubscribeMany registers one subscription PER kind, each with its own queue
// and its own delivery goroutine, so a handler covering several kinds receives
// them in an order those goroutines race to decide. Within a kind order holds;
// across kinds it does not. A turn's terminal therefore routinely arrives
// before the deltas it terminates, and a consumer that treats a terminal as
// "this turn is over" is wrong through no fault of its own.
//
// One subscription registered under many kinds has ONE queue and ONE delivery
// goroutine, so the order the handler sees is the order Publish was called -
// which, for the turn lifecycle, is the causal order.
func TestSubscribeAcrossPreservesCrossKindOrder(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	rec := &orderRecorder{}
	// Size the queue above the script: this test is about ORDER, and a queue
	// that overflows would drop the oldest events and fail on length instead,
	// hiding whatever the ordering did.
	bus.SubscribeAcross([]Kind{KindTurnStart, KindAssistant, KindTurnEnd}, rec, SubscribeOptions{BufSize: 4096})

	script := turnScript(200, 3)
	for _, ev := range script {
		bus.Publish(ev)
	}
	bus.Flush()

	want := labelsOf(script)
	got := rec.snapshot()
	if len(got) != len(want) {
		t.Fatalf("handler saw %d events, published %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %q, want %q; cross-kind order was not preserved", i, got[i], want[i])
		}
	}
}

// TestSubscribeAcrossPreservesOrderUnderConcurrentPublish keeps the guarantee
// honest about what it does and does not promise. Publishes from ONE goroutine
// are ordered; publishes from several are ordered only per publisher, because
// the bus cannot invent an order the callers never had. That is enough for the
// turn lifecycle, whose events all come from one goroutine per turn.
func TestSubscribeAcrossPreservesOrderUnderConcurrentPublish(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	rec := &orderRecorder{}
	bus.SubscribeAcross([]Kind{KindTurnStart, KindAssistant, KindTurnEnd}, rec, SubscribeOptions{BufSize: 4096})

	const publishers = 4
	var wg sync.WaitGroup
	scripts := make([][]Event, publishers)
	for p := 0; p < publishers; p++ {
		script := turnScript(25, 2)
		for i := range script {
			script[i].SessionID = fmt.Sprintf("pub%d", p)
			script[i].Content = fmt.Sprintf("pub%d/%s", p, script[i].Content)
		}
		scripts[p] = script
		wg.Add(1)
		go func(evs []Event) {
			defer wg.Done()
			for _, ev := range evs {
				bus.Publish(ev)
			}
		}(script)
	}
	wg.Wait()
	bus.Flush()

	// Per publisher, the subsequence the handler saw must equal what that
	// publisher sent. Interleaving between publishers is allowed.
	got := rec.snapshot()
	for p := 0; p < publishers; p++ {
		prefix := fmt.Sprintf("pub%d/", p)
		var sub []string
		for _, label := range got {
			if containsPrefixAfterKind(label, prefix) {
				sub = append(sub, label)
			}
		}
		want := labelsOf(scripts[p])
		if len(sub) != len(want) {
			t.Fatalf("publisher %d: handler saw %d of its events, sent %d", p, len(sub), len(want))
		}
		for i := range want {
			if sub[i] != want[i] {
				t.Fatalf("publisher %d event %d = %q, want %q; one publisher's own order was reordered", p, i, sub[i], want[i])
			}
		}
	}
}

// subscriberCount totals the registrations across every kind. It counts
// REGISTRATIONS, not distinct subscriptions, which is exactly what a partial
// removal leaves behind.
func (b *Bus) subscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, subs := range b.subs {
		n += len(subs)
	}
	return n
}

// containsPrefixAfterKind reports whether label's Content part starts with
// prefix. Labels are "<kind>:<content>".
func containsPrefixAfterKind(label, prefix string) bool {
	for i := 0; i < len(label); i++ {
		if label[i] == ':' {
			rest := label[i+1:]
			return len(rest) >= len(prefix) && rest[:len(prefix)] == prefix
		}
	}
	return false
}

// TestUnsubscribeRemovesSubscriptionFromEveryKind is the hazard a multi-kind
// subscription introduces.
//
// Unsubscribe used to remove the subscription from ONE kind's slice and then
// stop its delivery goroutine. For a subscription registered under several
// kinds that leaves live pointers under the remaining kinds feeding a queue
// nobody drains: trySend keeps enqueueing, drop-oldest keeps discarding, and
// nothing reports it. The removal must span every kind the subscription was
// registered under.
func TestUnsubscribeRemovesSubscriptionFromEveryKind(t *testing.T) {
	bus := New()
	rec := &orderRecorder{}
	kinds := []Kind{KindTurnStart, KindAssistant, KindTurnEnd}
	sub := bus.SubscribeAcross(kinds, rec)

	bus.Publish(Event{Kind: KindTurnStart, Content: "before"})
	bus.Flush()
	if n := len(rec.snapshot()); n != 1 {
		t.Fatalf("before Unsubscribe the handler saw %d events, want 1", n)
	}

	sub.Unsubscribe()
	for _, k := range kinds {
		bus.Publish(Event{Kind: k, Content: "after"})
	}
	bus.Flush()

	for _, label := range rec.snapshot() {
		if label != string(KindTurnStart)+":before" {
			t.Errorf("handler received %q after Unsubscribe", label)
		}
	}
	if n := bus.subscriberCount(); n != 0 {
		t.Errorf("bus still holds %d subscription registrations after Unsubscribe", n)
	}
	// Close must return: wg.Add ran once per subscription, so the single
	// delivery goroutine that already exited cannot leave the counter above
	// zero. A hang here IS the failure.
	bus.Close()
}

// TestBusUnsubscribeByHandlerRemovesEveryKind covers the legacy accessor. A
// caller holding only the handler can still reach a multi-kind subscription
// through Bus.Unsubscribe, and that path must not leave partial registrations
// behind either.
func TestBusUnsubscribeByHandlerRemovesEveryKind(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	rec := &orderRecorder{}
	bus.SubscribeAcross([]Kind{KindTurnStart, KindAssistant, KindTurnEnd}, rec)

	bus.Unsubscribe(KindAssistant, rec)

	if n := bus.subscriberCount(); n != 0 {
		t.Fatalf("bus holds %d registrations after Unsubscribe on one of three kinds, want 0", n)
	}
	bus.Publish(Event{Kind: KindTurnStart, Content: "after"})
	bus.Flush()
	if n := len(rec.snapshot()); n != 0 {
		t.Errorf("handler received %d events after Unsubscribe", n)
	}
}

// selfUnsubscriber unsubscribes itself from inside HandleEvent, through the
// re-entrant Delivery ticket - the only safe way to do it from a handler.
type selfUnsubscriber struct {
	mu   sync.Mutex
	seen int
}

func (s *selfUnsubscriber) HandleEvent(ctx context.Context, _ Event) {
	s.mu.Lock()
	s.seen++
	s.mu.Unlock()
	if d, ok := DeliveryFrom(ctx); ok {
		d.Unsubscribe()
	}
}

func (s *selfUnsubscriber) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen
}

// TestDeliveryUnsubscribeRemovesSubscriptionFromEveryKind covers the same
// hazard on the re-entrant path. Delivery.Unsubscribe scanned every kind but
// returned after the FIRST match, so a multi-kind subscription stayed
// registered under the rest.
func TestDeliveryUnsubscribeRemovesSubscriptionFromEveryKind(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	h := &selfUnsubscriber{}
	bus.SubscribeAcross([]Kind{KindTurnStart, KindAssistant, KindTurnEnd}, h)

	bus.Publish(Event{Kind: KindTurnStart, Content: "first"})
	bus.Flush()

	if n := bus.subscriberCount(); n != 0 {
		t.Fatalf("bus holds %d registrations after a self-Unsubscribe, want 0", n)
	}
	if got := h.count(); got != 1 {
		t.Fatalf("handler ran %d times, want 1", got)
	}
}

// TestSubscribeAcrossDeliversADuplicateKindOnce guards the registration loop.
// A caller passing the same kind twice - easy when the list is assembled from
// several sources - must not get every event of that kind twice.
func TestSubscribeAcrossDeliversADuplicateKindOnce(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	rec := &orderRecorder{}
	bus.SubscribeAcross([]Kind{KindAssistant, KindTurnStart, KindAssistant}, rec)

	bus.Publish(Event{Kind: KindAssistant, Content: "once"})
	bus.Flush()

	if n := len(rec.snapshot()); n != 1 {
		t.Errorf("a duplicated kind delivered %d copies, want 1", n)
	}
}

// TestSubscribeAcrossFlushSendsOneBarrier pins that Bus.Flush barriers a
// subscription ONCE however many kinds it is registered under.
//
// This test previously asserted only that the handler saw one event, which the
// duplicate-kind test already covers - it passed with Bus.Flush's pointer-keyed
// dedup removed entirely, and its comment claimed redundant barriers "would
// block". They do not: flushCh has a live consumer, so they serialize, and the
// subscription simply drains once per barrier. That is precisely why the
// property needs a counter to be observable at all.
func TestSubscribeAcrossFlushSendsOneBarrier(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	rec := &orderRecorder{}
	h := bus.SubscribeAcross([]Kind{KindTurnStart, KindAssistant, KindTurnEnd}, rec)

	bus.Publish(Event{Kind: KindAssistant, Content: "x"})
	bus.Flush()

	if n := len(rec.snapshot()); n != 1 {
		t.Errorf("handler saw %d events after Flush, want 1", n)
	}
	if n := h.s.Barriers(); n != 1 {
		t.Errorf("one Flush sent %d barriers to a subscription spanning 3 kinds, want 1", n)
	}
}

// TestDeliveryFlushSendsOneBarrierPerSubscription is the re-entrant sibling.
// Bus.Flush deduped by pointer from the start; Delivery.Flush and
// Delivery.Close iterated b.subs raw, which was equivalent to a set only while
// a subscription could appear under exactly one kind. SubscribeAcross ended
// that, so a re-entrant Flush barriered a three-kind subscription three times.
func TestDeliveryFlushSendsOneBarrierPerSubscription(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)

	target := bus.SubscribeAcross([]Kind{KindTurnStart, KindAssistant, KindTurnEnd}, &orderRecorder{})

	// A second subscription whose handler flushes the bus re-entrantly.
	flushed := make(chan struct{})
	bus.Subscribe(KindToolStart, HandlerFunc(func(ctx context.Context, _ Event) {
		if d, ok := DeliveryFrom(ctx); ok {
			d.Flush()
		}
		close(flushed)
	}))

	bus.Publish(Event{Kind: KindToolStart})
	select {
	case <-flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("re-entrant Delivery.Flush never returned")
	}

	if n := target.s.Barriers(); n != 1 {
		t.Errorf("one Delivery.Flush sent %d barriers to a subscription spanning 3 kinds, want 1", n)
	}
}

// TestSubscribeRecordsItsKind names the single-kind half of the removal
// contract on its own.
//
// Subscribe records sub.kinds so removal takes one path for every
// subscription. That line and removeSubLocked's old scan-everything fallback
// used to mask each other: deleting either alone left the package green, and
// only deleting both surfaced a failure - in an unrelated metrics test. The
// fallback is gone, so this test is now the thing that fails if Subscribe
// stops recording its kind.
func TestSubscribeRecordsItsKind(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	rec := &orderRecorder{}
	bus.Subscribe(KindAssistant, rec)

	if n := bus.subscriberCount(); n != 1 {
		t.Fatalf("expected 1 registration after Subscribe, got %d", n)
	}
	bus.Unsubscribe(KindAssistant, rec)
	if n := bus.subscriberCount(); n != 0 {
		t.Fatalf("bus still holds %d registrations after Unsubscribe; the subscription did not record its kind", n)
	}
}

// TestSubscribeAcrossReportsDrops makes the drop counter reachable. It was
// implemented on the unexported subscription and no caller could read it, so
// silent loss on a full queue had no observable signal at all.
func TestSubscribeAcrossReportsDrops(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h := HandlerFunc(func(_ context.Context, _ Event) {
		once.Do(func() { close(entered) })
		<-release
	})
	sub := bus.SubscribeAcross([]Kind{KindAssistant, KindTurnEnd}, h, SubscribeOptions{BufSize: 4})

	bus.Publish(Event{Kind: KindAssistant})
	<-entered
	for i := 0; i < 20; i++ {
		bus.Publish(Event{Kind: KindAssistant})
	}
	if got := sub.Drops(); got == 0 {
		t.Error("a queue of 4 overflowed by 20 publishes reported 0 drops")
	}
	close(release)
	bus.Flush()
}

// TestSubscribeAcrossHonoursTheBufferOption checks the option actually reaches
// the queue. Without it, merging the relayed kinds onto one queue would shrink
// the effective headroom of every low-volume kind that used to have a private
// 256 slots.
func TestSubscribeAcrossHonoursTheBufferOption(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	h := HandlerFunc(func(_ context.Context, _ Event) {})
	sub := bus.SubscribeAcross([]Kind{KindAssistant}, h, SubscribeOptions{BufSize: 7})
	if got := cap(sub.s.ch); got != 7 {
		t.Errorf("queue capacity = %d, want the requested 7", got)
	}
	def := bus.SubscribeAcross([]Kind{KindTurnEnd}, h)
	if got := cap(def.s.ch); got != defaultBufSize {
		t.Errorf("default queue capacity = %d, want %d", got, defaultBufSize)
	}
}

// TestSubscribeAcrossIsSafeWithNothingToDo keeps the degenerate calls from
// panicking on a nil handle. Every accessor must tolerate a subscription that
// was never registered.
func TestSubscribeAcrossIsSafeWithNothingToDo(t *testing.T) {
	bus := New()
	nilHandler := bus.SubscribeAcross([]Kind{KindAssistant}, nil)
	noKinds := bus.SubscribeAcross(nil, HandlerFunc(func(context.Context, Event) {}))
	bus.Close()
	closed := bus.SubscribeAcross([]Kind{KindAssistant}, HandlerFunc(func(context.Context, Event) {}))

	for name, sub := range map[string]*Subscription{"nil handler": nilHandler, "no kinds": noKinds, "closed bus": closed} {
		if sub == nil {
			t.Fatalf("%s: SubscribeAcross returned a nil handle; every caller would have to nil-check", name)
		}
		if got := sub.Drops(); got != 0 {
			t.Errorf("%s: Drops = %d, want 0", name, got)
		}
		if got := sub.Panics(); got != 0 {
			t.Errorf("%s: Panics = %d, want 0", name, got)
		}
		sub.Unsubscribe()
		sub.Unsubscribe()
	}
}

// TestSubscriptionUnsubscribeIsIdempotent covers the shutdown path a hub owner
// takes: a second Unsubscribe, and one after the bus has already been closed,
// must be no-ops rather than a second removal or a second join.
func TestSubscriptionUnsubscribeIsIdempotent(t *testing.T) {
	bus := New()
	rec := &orderRecorder{}
	sub := bus.SubscribeAcross([]Kind{KindAssistant, KindTurnEnd}, rec)

	sub.Unsubscribe()
	if n := bus.subscriberCount(); n != 0 {
		t.Fatalf("after Unsubscribe the bus holds %d registrations, want 0", n)
	}
	sub.Unsubscribe()
	if n := bus.subscriberCount(); n != 0 {
		t.Errorf("after a repeated Unsubscribe the bus holds %d registrations, want 0", n)
	}
	bus.Close()
	sub.Unsubscribe()

	bus.Publish(Event{Kind: KindAssistant, Content: "after"})
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("handler received %d events across the whole sequence, want 0", got)
	}
}

// TestSubscribeAcrossCountsHandlerPanics makes the panic counter reachable for
// the same reason as Drops: a handler that panics on every event is contained
// silently, and the containment is invisible without an accessor.
func TestSubscribeAcrossCountsHandlerPanics(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	h := HandlerFunc(func(context.Context, Event) { panic("boom") })
	sub := bus.SubscribeAcross([]Kind{KindAssistant}, h)

	bus.Publish(Event{Kind: KindAssistant})
	bus.Flush()

	if got := sub.Panics(); got != 1 {
		t.Errorf("Panics = %d, want 1", got)
	}
}
