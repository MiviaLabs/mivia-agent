package events

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Re-entrant handler API (Delivery)
// ---------------------------------------------------------------------------

// A handler that stops its own subscription from inside HandleEvent. The
// direct Bus.Unsubscribe path joins the delivery goroutine that is running
// the handler (self-join deadlock), so the handler must use the Delivery
// handle from DeliveryFrom(ctx). Delivery.Unsubscribe marks the subscription
// stopped + cancelled, removes it from the bus, and returns without joining:
// the delivery goroutine exits at the deliver() loop-top stopped check after
// the current handler returns, without re-invoking the handler for queued
// events.
func TestBusHandlerCanUnsubscribeItselfFromHandleEvent(t *testing.T) {
	bus := New()

	var calls atomic.Int64
	unsubscribed := make(chan struct{})
	noTicket := make(chan struct{})
	bus.Subscribe(KindToolStart, HandlerFunc(func(ctx context.Context, ev Event) {
		if calls.Add(1) == 1 {
			d, ok := DeliveryFrom(ctx)
			if !ok {
				close(noTicket)
				return
			}
			d.Unsubscribe()
			close(unsubscribed)
		}
	}))

	bus.mu.Lock()
	sub := bus.subs[KindToolStart][0]
	bus.mu.Unlock()

	// Burst: any events queued past the first must NOT be re-invoked once the
	// handler has stopped itself (the goroutine exits at the loop-top stopped
	// check instead of draining them).
	for i := 0; i < 6; i++ {
		bus.Publish(NewEvent(KindToolStart))
	}

	// Delivery.Unsubscribe must return without deadlocking.
	select {
	case <-unsubscribed:
	case <-noTicket:
		t.Fatal("DeliveryFrom returned false inside HandleEvent")
	case <-time.After(5 * time.Second):
		t.Fatal("Delivery.Unsubscribe from HandleEvent did not return")
	}

	// The delivery goroutine must exit promptly (loop-top stopped check).
	select {
	case <-sub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("delivery goroutine did not exit after self-Unsubscribe")
	}

	if calls.Load() != 1 {
		t.Fatalf("self-unsubscribed handler invoked %d times, want 1 (queued events must not be re-invoked)", calls.Load())
	}

	// The bus remains usable: a fresh subscription still receives events and
	// Flush still synchronizes.
	other := &collectHandler{}
	bus.Subscribe(KindToolStart, other)
	bus.Publish(NewEvent(KindToolStart))
	bus.Flush()
	if other.Len() != 1 {
		t.Fatalf("bus not usable after self-Unsubscribe: other handler got %d events, want 1", other.Len())
	}
	if calls.Load() != 1 {
		t.Fatalf("self-unsubscribed handler invoked %d times after later publishes, want 1", calls.Load())
	}

	bus.Close()
}

// A handler that flushes the bus from inside HandleEvent. The direct
// Bus.Flush path sends a barrier to the caller's own delivery goroutine,
// which is busy running the handler (self-barrier deadlock), so the handler
// must use the Delivery handle. Delivery.Flush barriers every subscription
// EXCEPT the caller's own (whose goroutine cannot ack until the handler
// returns) and blocks until those are acked.
func TestBusHandlerCanFlushFromHandleEvent(t *testing.T) {
	bus := New()

	other := &collectHandler{}
	bus.Subscribe(KindToolStart, other)

	var calls atomic.Int64
	flushed := make(chan struct{})
	noTicket := make(chan struct{})
	bus.Subscribe(KindToolStart, HandlerFunc(func(ctx context.Context, ev Event) {
		if calls.Add(1) == 1 {
			d, ok := DeliveryFrom(ctx)
			if !ok {
				close(noTicket)
				return
			}
			d.Flush() // must return: skips our own subscription, barriers the rest
			close(flushed)
		}
	}))

	bus.Publish(NewEvent(KindToolStart))

	select {
	case <-flushed:
	case <-noTicket:
		t.Fatal("DeliveryFrom returned false inside HandleEvent")
	case <-time.After(5 * time.Second):
		t.Fatal("Delivery.Flush from HandleEvent did not return within 5s")
	}

	// Delivery.Flush must have synchronized the OTHER subscription: the event
	// published before it was delivered by the time it returned.
	if other.Len() != 1 {
		t.Fatalf("other handler got %d events, want 1 (Delivery.Flush must barrier other subscriptions)", other.Len())
	}

	// The bus stays usable: a later event is delivered and an external Flush
	// still synchronizes (calls reaches 2).
	bus.Publish(NewEvent(KindToolStart))
	bus.Flush()
	if calls.Load() != 2 {
		t.Fatalf("handler invoked %d times, want 2 (bus must stay usable after Delivery.Flush)", calls.Load())
	}

	bus.Close()
}

// A handler that closes the bus from inside HandleEvent. The direct Bus.Close
// path waits on b.wg, which includes the delivery goroutine running the
// handler (self-join deadlock), so the handler must use the Delivery handle.
// Delivery.Close performs the full close sequence but does NOT wait for the
// caller's own goroutine: it exits on its own once the handler returns (its
// context is cancelled, so it drains and terminates).
func TestBusHandlerCanCloseBusFromHandleEvent(t *testing.T) {
	bus := New()

	closed := make(chan struct{})
	noTicket := make(chan struct{})
	bus.Subscribe(KindToolStart, HandlerFunc(func(ctx context.Context, ev Event) {
		d, ok := DeliveryFrom(ctx)
		if !ok {
			close(noTicket)
			return
		}
		d.Close()
		close(closed)
	}))

	bus.Publish(NewEvent(KindToolStart))

	select {
	case <-closed:
	case <-noTicket:
		t.Fatal("DeliveryFrom returned false inside HandleEvent")
	case <-time.After(5 * time.Second):
		t.Fatal("Delivery.Close from HandleEvent did not return within 5s")
	}

	bus.mu.Lock()
	closedFlag := bus.closed
	subsNil := bus.subs == nil
	bus.mu.Unlock()
	if !closedFlag {
		t.Fatal("bus.closed not set after Delivery.Close from handler")
	}
	if !subsNil {
		t.Fatal("bus.subs not cleared after Delivery.Close from handler")
	}

	// Idempotent and safe afterwards: no panic, Publish is a silent no-op.
	bus.Close()
	bus.Publish(NewEvent(KindToolStart))
}

// ---------------------------------------------------------------------------
// Delivery.Close deadlock regressions (concurrent close paths)
// ---------------------------------------------------------------------------

// Regression: two subscriptions whose handlers both close the bus from
// inside HandleEvent must not deadlock. Each old Delivery.Close ran its wait
// for the OTHER subscription's <-done INSIDE b.close.Do; the second handler's
// Delivery.Close then parked inside sync.Once.Do until the first body
// returned, but that body was waiting on the second subscription's done -
// which only closes when the second handler's delivery goroutine exits, which
// requires the Do call to return. Permanent deadlock. The fixed contract
// never waits on a subscription whose delivery goroutine is currently running
// a handler, so both Close calls complete and the bus drains.
func TestDeliveryCloseFromConcurrentHandlersTerminates(t *testing.T) {
	bus := New()

	start := make(chan struct{})
	entered := make(chan struct{}, 2)
	closed := make(chan struct{}, 2)
	noTicket := make(chan struct{})
	h := HandlerFunc(func(ctx context.Context, ev Event) {
		entered <- struct{}{}
		<-start // both handlers must be delivering before either closes
		d, ok := DeliveryFrom(ctx)
		if !ok {
			close(noTicket)
			return
		}
		d.Close()
		closed <- struct{}{}
	})
	// Two separate subscriptions on the same kind: each has its own delivery
	// goroutine, so one published event runs this handler concurrently in
	// both goroutines.
	bus.Subscribe(KindToolStart, h)
	bus.Subscribe(KindToolStart, h)

	bus.Publish(NewEvent(KindToolStart))

	// Wait until both delivery goroutines are running the handler.
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("handlers never entered HandleEvent in both delivery goroutines")
		}
	}
	close(start)

	// Both Delivery.Close calls must return; a deadlock parks one of them
	// inside sync.Once.Do forever.
	for i := 0; i < 2; i++ {
		select {
		case <-closed:
		case <-noTicket:
			t.Fatal("DeliveryFrom returned false inside HandleEvent")
		case <-time.After(8 * time.Second):
			t.Fatal("Delivery.Close deadlocked: a concurrent handler's Close parked inside sync.Once.Do")
		}
	}

	bus.mu.Lock()
	closedFlag := bus.closed
	bus.mu.Unlock()
	if !closedFlag {
		t.Fatal("bus.closed not set after both handlers closed the bus")
	}

	// The delivery goroutines exit on their own once their contexts are
	// cancelled; an external Close must drain and return promptly.
	externalDone := make(chan struct{})
	go func() {
		bus.Close()
		close(externalDone)
	}()
	select {
	case <-externalDone:
	case <-time.After(8 * time.Second):
		t.Fatal("external Bus.Close deadlocked after handlers closed the bus")
	}
}

// Regression: a handler's Delivery.Close racing an external Bus.Close from
// another goroutine must not deadlock when the EXTERNAL Close wins the shared
// b.close sync.Once. The old Bus.Close ran b.wg.Wait() inside the Do body, so
// when the external Close won the once, the handler's Delivery.Close parked
// inside Do and the handler's delivery goroutine (tracked by b.wg) could
// never exit: wg.Wait waited on a goroutine that was waiting on Do. The fixed
// Bus.Close only marks+cancels inside Do and waits afterwards, so a
// Do-blocked caller can never hold a delivery goroutine hostage.
func TestDeliveryCloseRacingExternalCloseExternalWins(t *testing.T) {
	// The external Close wins the sync.Once. The handler waits for the
	// shutdown context to be cancelled (which only the external Close does)
	// before calling Delivery.Close, so the external Do body is guaranteed to
	// be running (and, in the buggy code, parked in wg.Wait) when the handler
	// parks in Do.
	bus := New()
	entered := make(chan struct{})
	handlerClosed := make(chan struct{})
	bus.Subscribe(KindToolStart, HandlerFunc(func(ctx context.Context, ev Event) {
		close(entered)
		<-ctx.Done() // external Close cancels this before it waits
		if d, ok := DeliveryFrom(ctx); ok {
			d.Close()
			close(handlerClosed)
		}
	}))
	bus.Publish(NewEvent(KindToolStart))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never entered HandleEvent")
	}

	externalDone := make(chan struct{})
	go func() {
		bus.Close()
		close(externalDone)
	}()

	select {
	case <-handlerClosed:
	case <-time.After(8 * time.Second):
		t.Fatal("Delivery.Close deadlocked racing an external Bus.Close")
	}
	select {
	case <-externalDone:
	case <-time.After(8 * time.Second):
		t.Fatal("external Bus.Close deadlocked while a handler was closing the bus")
	}
}

// Regression: a handler's Delivery.Close racing an external Bus.Close from
// another goroutine must not deadlock when the HANDLER's Close wins the
// shared b.close sync.Once. The old Bus.Close ran b.wg.Wait() inside the Do
// body, so when the handler won the once, the external Close parked inside Do
// and the handler's delivery goroutine (tracked by b.wg) could never exit:
// wg.Wait waited on a goroutine that was waiting on Do. The fixed Bus.Close
// only marks+cancels inside Do and waits afterwards, so a Do-blocked caller
// can never hold a delivery goroutine hostage.
func TestDeliveryCloseRacingExternalCloseHandlerWins(t *testing.T) {
	// The handler's Delivery.Close wins the sync.Once. The handler signals
	// that it is about to close; the external Close is then called while the
	// handler is already inside (or about to enter) Do.
	bus := New()
	aboutToClose := make(chan struct{})
	handlerClosed := make(chan struct{})
	bus.Subscribe(KindToolStart, HandlerFunc(func(ctx context.Context, ev Event) {
		if d, ok := DeliveryFrom(ctx); ok {
			close(aboutToClose)
			d.Close()
			close(handlerClosed)
		}
	}))
	bus.Publish(NewEvent(KindToolStart))
	select {
	case <-aboutToClose:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never entered HandleEvent")
	}

	externalDone := make(chan struct{})
	go func() {
		bus.Close()
		close(externalDone)
	}()

	select {
	case <-handlerClosed:
	case <-time.After(8 * time.Second):
		t.Fatal("Delivery.Close deadlocked after winning the once")
	}
	select {
	case <-externalDone:
	case <-time.After(8 * time.Second):
		t.Fatal("external Bus.Close deadlocked after the handler closed the bus")
	}
}

// closeRaceSubB is the hand-built subscription registered on the bus by
// TestDeliveryCloseDoesNotWaitOnSubThatStartsDelivering. Its delivery
// goroutine is test-driven; its delivering transition is gated until Close
// has passed the delivering check.
type closeRaceSubB struct {
	sub        *subscription
	gate       chan struct{}
	driverDone chan struct{}
}

// newCloseRaceSubB registers subB on the bus and starts its test-driven
// delivery goroutine. The driver waits for the bus shutdown context
// (cancelled by Close's sync.Once body) and then for gate, so subB's
// delivering transition is gated until Close has passed the delivering check.
// subB's handler calls DeliveryFrom(ctx).Flush(), which barriers every other
// subscription (subA).
func newCloseRaceSubB(bus *Bus) *closeRaceSubB {
	subB := &subscription{
		handler: HandlerFunc(func(ctx context.Context, ev Event) {
			if d, ok := DeliveryFrom(ctx); ok {
				d.Flush() // barriers every other subscription (subA)
			}
		}),
		ch:               make(chan Event, 1),
		done:             make(chan struct{}),
		flushCh:          make(chan chan struct{}, 1),
		deliveringChange: make(chan struct{}),
	}
	subBCtx, subBCancel := context.WithCancel(bus.ctx)
	subB.cancel = subBCancel
	bus.mu.Lock()
	bus.subs[KindToolStart] = append(bus.subs[KindToolStart], subB)
	bus.mu.Unlock()

	c := &closeRaceSubB{sub: subB, gate: make(chan struct{})}
	c.driverDone = make(chan struct{})
	go func() {
		defer close(c.driverDone)
		// Close's sync.Once body cancelled the bus ctx: subB's delivering
		// transition is gated until Close has passed the delivering check.
		<-subBCtx.Done()
		// The test opened the gate only after confirming Close released b.mu
		// with b.closed set: Close is entering the wait loop.
		<-c.gate
		c.sub.handle(withDelivery(subBCtx, &Delivery{b: bus, s: c.sub}), NewEvent(KindToolStart))
		close(c.sub.done)
	}()
	return c
}

// waitForCloseEnteringWaitLoop blocks until Close has run its sync.Once body
// (b.closed set) and released b.mu after collecting the others list: it is
// entering the wait loop, whose delivering.Load executes before the gated
// driver can reach Store.
func waitForCloseEnteringWaitLoop(t *testing.T, i int, bus *Bus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if bus.mu.TryLock() {
			closedFlag := bus.closed
			bus.mu.Unlock()
			if closedFlag {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("iter %d: Close never finished its once body and collection", i)
		}
		// Yield instead of sleeping: this goroutine must not stall the Close
		// goroutine that briefly holds b.mu between its once body and the
		// others-list collection (mivia.go.tests-no-time-sleep forbids
		// time.Sleep in tests). Deterministic lock-state synchronization: the
		// loop only observes the b.mu/closed state Close itself publishes.
		runtime.Gosched()
	}
}

// runDeliveryCloseNoWaitIteration runs one iteration of the close TOCTOU
// regression: subA is a real bus subscription whose handler calls
// DeliveryFrom(ctx).Close(); subB's delivering transition is gated until
// Close has passed the delivering check, then subB's handler Flushes every
// other subscription (subA). Pre-fix Close commits to <-subB.done and hangs;
// post-fix its select fires on subB's deliveringChange and it skips subB.
func runDeliveryCloseNoWaitIteration(t *testing.T, i int) {
	t.Helper()
	bus := New()

	subAEntered := make(chan struct{})
	noTicket := make(chan struct{})
	subADone := make(chan struct{})
	bus.Subscribe(KindToolStart, HandlerFunc(func(ctx context.Context, ev Event) {
		close(subAEntered)
		d, ok := DeliveryFrom(ctx)
		if !ok {
			close(noTicket)
			return
		}
		d.Close()
		close(subADone)
	}))

	subB := newCloseRaceSubB(bus)

	// Trigger subA's handler; its Delivery.Close runs the wait loop.
	bus.Publish(NewEvent(KindToolStart))
	select {
	case <-subAEntered:
	case <-noTicket:
		t.Fatalf("iter %d: subA DeliveryFrom returned false inside HandleEvent", i)
	case <-time.After(5 * time.Second):
		t.Fatalf("iter %d: subA handler never entered HandleEvent", i)
	}

	// Confirm Close has run its once body and released b.mu after collecting
	// the others list (it is entering the wait loop).
	waitForCloseEnteringWaitLoop(t, i, bus)

	// Open the gate: subB transitions to delivering and its handler Flushes
	// every other subscription (subA), whose delivery goroutine is blocked
	// inside Close.
	close(subB.gate)

	// Post-fix, Close returns once subB's transition fires the snapshotted
	// deliveringChange channel; pre-fix it hangs on <-subB.done forever.
	select {
	case <-subADone:
	case <-time.After(5 * time.Second):
		t.Fatalf("iter %d: Delivery.Close deadlocked waiting on subB (delivering TOCTOU)", i)
	}
	// subB's Flush must also complete (cleared subs map, or subA's goroutine,
	// now returned from Close, acks the barrier).
	select {
	case <-subB.driverDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("iter %d: subB driver did not finish (Flush blocked on subA barrier)", i)
	}

	bus.mu.Lock()
	subsCleared := bus.subs == nil
	bus.mu.Unlock()
	if !subsCleared {
		t.Fatalf("iter %d: bus.subs not cleared after Delivery.Close returned", i)
	}
}

// Regression: Delivery.Close must not deadlock against a subscription whose
// delivery goroutine starts running a handler after Close's delivering check.
// The old Close checked s.delivering.Load() and then committed to <-s.done
// without re-checking (a TOCTOU race): a subscription idle at the check but
// about to deliver can be joined. If that handler calls Delivery.Flush, it
// barriers every OTHER subscription — including the caller's own, whose
// delivery goroutine is blocked inside Close waiting on the joined
// goroutine's done — so the barrier can never be acked and Close hangs
// forever. The fixed Close snapshots the subscription's deliveringChange
// channel (closed on every delivering false->true transition in handle())
// under deliveringMu, re-checks delivering, and selects on {<-s.done,
// <-changed}: the moment the joined goroutine starts delivering, the select
// abandons the wait (it exits on its own once its handler returns).
//
// subA is a real bus subscription whose handler calls DeliveryFrom(ctx)
// .Close(). subB is a hand-built subscription registered on the bus with a
// test-driven delivery goroutine whose delivering transition is gated until
// Close has passed the delivering check: the driver waits for the bus
// shutdown context (cancelled by Close's sync.Once body), and the test opens
// the gate only after confirming Close released b.mu with b.closed set
// (Close has collected the others list and is entering the wait loop, whose
// delivering.Load executes before the just-released driver can reach
// Store). subB's handler calls DeliveryFrom(ctx).Flush(), which barriers
// every other subscription — including subA, whose delivery goroutine is
// blocked inside Close and can never ack while Close waits. Pre-fix the
// iteration deadlocks and times out; post-fix Close's select fires on
// subB's deliveringChange, Close skips subB and returns, and subB's Flush
// sees the cleared subs map (or subA's goroutine, now returned from Close,
// acks the barrier).
func TestDeliveryCloseDoesNotWaitOnSubThatStartsDelivering(t *testing.T) {
	const iterations = 50
	for i := 0; i < iterations; i++ {
		runDeliveryCloseNoWaitIteration(t, i)
	}
}

// ---------------------------------------------------------------------------
// External caller blocking semantics (Step-5 auditor regressions)
// ---------------------------------------------------------------------------

// External Flush must block until a slow handler is released, then return
// with every event published before the Flush delivered. The old caller-blind
// code capped flushSend at 5s, so Flush returned while the handler was still
// blocked. The handler stays blocked for longer than the old cap, so a
// reintroduced cap fails this test.
func TestExternalFlushBlocksUntilSlowHandlerReleased(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)

	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var mu sync.Mutex
	count := 0
	h := HandlerFunc(func(ctx context.Context, ev Event) {
		mu.Lock()
		count++
		first := count == 1
		mu.Unlock()
		if first {
			close(entered)
			<-release
		}
	})
	bus.Subscribe(KindToolStart, h)

	bus.Publish(NewEvent(KindToolStart))
	<-entered
	bus.Publish(NewEvent(KindToolStart))
	bus.Publish(NewEvent(KindToolStart))

	flushDone := make(chan struct{})
	go func() {
		bus.Flush()
		close(flushDone)
	}()

	// The old capped flushSend returned after 5s even with the handler still
	// blocked. The blocking semantics require Flush to stay pending until the
	// handler is released. Observe for longer than the removed 5s cap.
	select {
	case <-flushDone:
		t.Fatal("external Flush returned while the handler was still blocked")
	case <-time.After(6 * time.Second):
	}

	close(release)

	select {
	case <-flushDone:
	case <-time.After(5 * time.Second):
		t.Fatal("external Flush did not return after the handler was released")
	}

	mu.Lock()
	got := count
	mu.Unlock()
	if got != 3 {
		t.Fatalf("handler saw %d events, want 3 (all events must be delivered before Flush returns)", got)
	}
}

// blockingHandler blocks on its first invocation until release is closed,
// then counts every invocation. It is a pointer type so bus.Unsubscribe can
// match it by pointer identity (the documented comparison contract).
type blockingHandler struct {
	mu      sync.Mutex
	count   int
	entered chan struct{}
	release chan struct{}
}

func (h *blockingHandler) HandleEvent(ctx context.Context, ev Event) {
	h.mu.Lock()
	h.count++
	first := h.count == 1
	h.mu.Unlock()
	if first {
		close(h.entered)
		<-h.release
	}
}

// External Unsubscribe must block until the target handler completes and all
// queued events have been drained. The old caller-blind code skipped the
// join while the handler was delivering, so Unsubscribe returned immediately
// with the handler still mid-flight and queued events undelivered.
func TestExternalUnsubscribeDrainsWhileHandlerBlocked(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)

	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	h := &blockingHandler{entered: make(chan struct{}), release: release}
	bus.Subscribe(KindToolStart, h)

	const total = 5
	for i := 0; i < total; i++ {
		bus.Publish(NewEvent(KindToolStart))
	}
	<-h.entered

	unsubDone := make(chan struct{})
	go func() {
		bus.Unsubscribe(KindToolStart, h)
		close(unsubDone)
	}()

	// The old caller-blind code returned immediately (delivering flag true,
	// join skipped). The blocking semantics require Unsubscribe to stay
	// pending until the handler completes and the queue is drained.
	select {
	case <-unsubDone:
		t.Fatal("external Unsubscribe returned while the handler was still blocked")
	case <-time.After(300 * time.Millisecond):
	}

	close(release)

	select {
	case <-unsubDone:
	case <-time.After(5 * time.Second):
		t.Fatal("external Unsubscribe did not return after the handler completed")
	}

	h.mu.Lock()
	got := h.count
	h.mu.Unlock()
	if got != total {
		t.Fatalf("handler saw %d events, want %d (Unsubscribe must drain queued events before returning)", got, total)
	}
}

// External Close must not return before a mid-flight handler completes. The
// old caller-blind code bounded the wait at 5s, so Close returned while the
// handler was still blocked. The handler stays blocked for longer than the
// old cap, so a reintroduced cap fails this test.
func TestExternalCloseBlocksUntilHandlerCompletes(t *testing.T) {
	bus := New()

	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	handlerDone := make(chan struct{})
	bus.Subscribe(KindToolStart, HandlerFunc(func(ctx context.Context, ev Event) {
		close(entered)
		<-release
		close(handlerDone)
	}))

	bus.Publish(NewEvent(KindToolStart))
	<-entered

	closeDone := make(chan struct{})
	go func() {
		bus.Close()
		close(closeDone)
	}()

	// The old capped Close returned after 5s even with the handler still
	// blocked. The blocking semantics require Close to stay pending until the
	// handler completes. Observe for longer than the removed 5s cap.
	select {
	case <-closeDone:
		t.Fatal("external Close returned while the handler was still blocked")
	case <-time.After(6 * time.Second):
	}

	close(release)

	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("external Close did not return after the handler completed")
	}

	// Close must only have returned after the handler completed.
	select {
	case <-handlerDone:
	default:
		t.Fatal("Close returned before the handler completed")
	}
}
