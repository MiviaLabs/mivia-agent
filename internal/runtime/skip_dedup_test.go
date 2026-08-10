package runtime

// SkipDedup: per-request opt-out from the per-turn and ID-keyed dedup. A
// read-only tool call marked SkipDedup always executes fresh: it never reserves
// a flight key, never joins a waiter, is never answered from a recorded result,
// and never writes dedup state. The zero value keeps today's behavior exactly.

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestTurnDedupKeyHonorsSkipDedup pins the single gate: turnDedupKey returns
// ("","") for a Tool call with a TurnID when SkipDedup is true, and a non-empty
// key plus content hash otherwise. The empty key disables flight-entry
// creation, duplicate reservation, in-flight completion and bucket recording
// for the skipped call.
func TestTurnDedupKeyHonorsSkipDedup(t *testing.T) {
	req := Request{Kind: Tool, TurnID: "turn:1", ParentID: "session", Step: 1, Name: "t", Input: json.RawMessage(`{"argv":["read-only"]}`)}
	key, hash := turnDedupKey(req)
	if key == "" || hash == "" {
		t.Fatalf("turnDedupKey without SkipDedup = (%q,%q), want non-empty key and content hash", key, hash)
	}
	req.SkipDedup = true
	key, hash = turnDedupKey(req)
	if key != "" || hash != "" {
		t.Fatalf("turnDedupKey with SkipDedup = (%q,%q), want (\"\",\"\")", key, hash)
	}
}

// TestDispatcherSkipDedupRunsIdenticalCallsFresh pins the per-turn half: two
// identical same-turn/same-step calls (distinct IDs) marked SkipDedup both
// reach the handler and neither is answered from the dedup bucket; the
// control without SkipDedup collapses to one execution answered as duplicate.
func TestDispatcherSkipDedupRunsIdenticalCallsFresh(t *testing.T) {
	d := New(Policy{})
	var calls atomic.Int32
	if err := d.Register(Tool, "t", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"argv":["read-only"]}`)

	first := d.Invoke(context.Background(), Request{ID: "skip-1", Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", Step: 1, SkipDedup: true})
	if first.Err != nil {
		t.Fatal(first.Err)
	}
	second := d.Invoke(context.Background(), Request{ID: "skip-2", Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", Step: 1, SkipDedup: true})
	if second.Err != nil {
		t.Fatal(second.Err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("SkipDedup handler executed %d times, want exactly 2", got)
	}
	if second.Metadata.Status == "duplicate" {
		t.Fatal("second SkipDedup invocation must not be answered from the dedup cache")
	}

	// Control: identical calls without SkipDedup dedup to one execution.
	d2 := New(Policy{})
	var ctrlCalls atomic.Int32
	if err := d2.Register(Tool, "t", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		ctrlCalls.Add(1)
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	if r := d2.Invoke(context.Background(), Request{ID: "ctl-1", Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", Step: 1}); r.Err != nil {
		t.Fatal(r.Err)
	}
	ctlSecond := d2.Invoke(context.Background(), Request{ID: "ctl-2", Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", Step: 1})
	if ctlSecond.Err != nil {
		t.Fatal(ctlSecond.Err)
	}
	if got := ctrlCalls.Load(); got != 1 {
		t.Fatalf("control handler executed %d times, want exactly 1", got)
	}
	if ctlSecond.Metadata.Status != "duplicate" {
		t.Fatalf("control second invocation status = %q, want duplicate", ctlSecond.Metadata.Status)
	}
}

// TestDispatcherSkipDedupBypassesCompletedMap pins the ID-keyed half: re-using
// the SAME ID with identical input and SkipDedup re-executes (the completed-map
// read and write are both skipped); the control without SkipDedup is answered
// from the recorded result as duplicate.
func TestDispatcherSkipDedupBypassesCompletedMap(t *testing.T) {
	d := New(Policy{})
	var calls atomic.Int32
	if err := d.Register(Tool, "t", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"argv":["read-only"]}`)

	first := d.Invoke(context.Background(), Request{ID: "same-id", Kind: Tool, Name: "t", Input: input, SkipDedup: true})
	if first.Err != nil {
		t.Fatal(first.Err)
	}
	second := d.Invoke(context.Background(), Request{ID: "same-id", Kind: Tool, Name: "t", Input: input, SkipDedup: true})
	if second.Err != nil {
		t.Fatal(second.Err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("SkipDedup same-ID handler executed %d times, want exactly 2", got)
	}
	if second.Metadata.Status == "duplicate" {
		t.Fatal("second same-ID SkipDedup invocation must not be answered from the completed map")
	}

	// Control: same ID without SkipDedup is answered from the completed map.
	d2 := New(Policy{})
	var ctrlCalls atomic.Int32
	if err := d2.Register(Tool, "t", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		ctrlCalls.Add(1)
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	if r := d2.Invoke(context.Background(), Request{ID: "same-id", Kind: Tool, Name: "t", Input: input}); r.Err != nil {
		t.Fatal(r.Err)
	}
	ctlSecond := d2.Invoke(context.Background(), Request{ID: "same-id", Kind: Tool, Name: "t", Input: input})
	if ctlSecond.Err != nil {
		t.Fatal(ctlSecond.Err)
	}
	if got := ctrlCalls.Load(); got != 1 {
		t.Fatalf("control same-ID handler executed %d times, want exactly 1", got)
	}
	if ctlSecond.Metadata.Status != "duplicate" {
		t.Fatalf("control same-ID second invocation status = %q, want duplicate", ctlSecond.Metadata.Status)
	}
}

// TestDispatcherSkipDedupSkipsInFlightCollapse pins the in-flight half: two
// CONCURRENT identical SkipDedup calls (same turn/step, distinct IDs) both
// reach the handler because neither reserves the flight key; the control
// without SkipDedup collapses to one execution via the in-flight entry.
func TestDispatcherSkipDedupSkipsInFlightCollapse(t *testing.T) {
	d := New(Policy{})
	var calls atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	if err := d.Register(Tool, "t", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"argv":["dedup","concurrent"]}`)
	start := make(chan struct{})
	results := make(chan Result, 2)
	for _, id := range []string{"c-1", "c-2"} {
		go func(id string) {
			<-start
			results <- d.Invoke(context.Background(), Request{ID: id, Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", Step: 1, SkipDedup: true})
		}(id)
	}
	// Both invocations must be inside the handler at the same time: neither may
	// wait on a flight key owned by the other.
	close(start)
	waitForEntries(t, entered, 2, 5*time.Second)
	close(release)
	r1 := <-results
	r2 := <-results
	if r1.Err != nil || r2.Err != nil {
		t.Fatalf("results errored: r1=%+v r2=%+v", r1, r2)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("SkipDedup concurrent handler executed %d times, want exactly 2", got)
	}
	if r1.Metadata.Status == "duplicate" || r2.Metadata.Status == "duplicate" {
		t.Fatalf("SkipDedup concurrent statuses must not be duplicate: r1=%q r2=%q", r1.Metadata.Status, r2.Metadata.Status)
	}

	// Control: concurrent identical calls without SkipDedup collapse to one
	// execution; the waiter receives the owner's result as duplicate.
	d2 := New(Policy{})
	var ctrlCalls atomic.Int32
	ctrlEntered := make(chan struct{}, 2)
	ctrlRelease := make(chan struct{})
	if err := d2.Register(Tool, "t", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		ctrlCalls.Add(1)
		ctrlEntered <- struct{}{}
		<-ctrlRelease
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	ctrlStart := make(chan struct{})
	ctrlResults := make(chan Result, 2)
	for _, id := range []string{"c-1", "c-2"} {
		go func(id string) {
			<-ctrlStart
			ctrlResults <- d2.Invoke(context.Background(), Request{ID: id, Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", Step: 1})
		}(id)
	}
	close(ctrlStart)
	// The second invocation never enters the handler: it waits on the owner's
	// in-flight entry, so only one arrival happens and the grace period elapses.
	waitForEntries(t, ctrlEntered, 2, 5*time.Second)
	close(ctrlRelease)
	cr1 := <-ctrlResults
	cr2 := <-ctrlResults
	if cr1.Err != nil || cr2.Err != nil {
		t.Fatalf("control results errored: cr1=%+v cr2=%+v", cr1, cr2)
	}
	if got := ctrlCalls.Load(); got != 1 {
		t.Fatalf("control concurrent handler executed %d times, want exactly 1", got)
	}
	if cr2.Metadata.Status != "duplicate" {
		t.Fatalf("control concurrent second status = %q, want duplicate", cr2.Metadata.Status)
	}
}

// TestDispatcherSkipDedupActivePathGated pins the ID-keyed active/waiter half:
// the SAME ID issued CONCURRENTLY with SkipDedup never collapses on the active
// map - a skipped call never joins the ID-keyed dedup state - so both
// invocations execute. The loop stamps every invocation with a fresh ID, so the
// mixed case (a SkipDedup owner with a non-SkipDedup same-ID duplicate) never
// occurs in practice and is deliberately not exercised.
func TestDispatcherSkipDedupActivePathGated(t *testing.T) {
	d := New(Policy{})
	var calls atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	if err := d.Register(Tool, "t", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"argv":["read-only"]}`)
	start := make(chan struct{})
	results := make(chan Result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			results <- d.Invoke(context.Background(), Request{ID: "same", Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", Step: 1, SkipDedup: true})
		}()
	}
	close(start)
	waitForEntries(t, entered, 2, 5*time.Second)
	close(release)
	r1 := <-results
	r2 := <-results
	if r1.Err != nil || r2.Err != nil {
		t.Fatalf("results errored: r1=%+v r2=%+v", r1, r2)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("same-ID SkipDedup handler executed %d times, want exactly 2 (no same-ID collapse)", got)
	}
	if r1.Metadata.Status == "duplicate" || r2.Metadata.Status == "duplicate" {
		t.Fatalf("same-ID SkipDedup statuses must not be duplicate: r1=%q r2=%q", r1.Metadata.Status, r2.Metadata.Status)
	}
}

// TestResultIsDuplicate pins the truth table of Result.IsDuplicate: only the
// "duplicate" status reports a cache-served re-delivery.
func TestResultIsDuplicate(t *testing.T) {
	for status, want := range map[string]bool{
		"duplicate": true,
		"completed": false,
		"":          false,
		"canceled":  false,
	} {
		r := Result{Metadata: Metadata{Status: status}}
		if got := r.IsDuplicate(); got != want {
			t.Fatalf("Result{Status:%q}.IsDuplicate() = %v, want %v", status, got, want)
		}
	}
}

// TestSkipDedupMixedWaiterSteal pins the audit finding: a failing SkipDedup
// call that reuses an in-flight owner's ID must NOT steal or delete the
// ID-keyed waiters (deliverTerminal gate). The duplicate's registration must
// survive, and the owner's own completion must be the one to deliver.
func TestSkipDedupMixedWaiterSteal(t *testing.T) {
	d := New(Policy{})
	entered := make(chan struct{})
	release := make(chan struct{})
	if err := d.Register(Tool, "t", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := d.Register(Tool, "boom", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		return nil, errors.New("boom")
	})); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"x":1}`)

	// Owner: non-SkipDedup, ID "same", in flight.
	ownerDone := make(chan Result, 1)
	go func() {
		ownerDone <- d.Invoke(context.Background(), Request{ID: "same", Kind: Tool, Name: "t", Input: input, Budget: 1})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("owner never entered the handler")
	}
	waiterDone := make(chan Result, 1)
	go func() {
		waiterDone <- d.Invoke(context.Background(), Request{ID: "same", Kind: Tool, Name: "t", Input: input, Budget: 1})
	}()
	waitForIDKeyedWaiterRegistered(t, d, "same", "same")

	// Intruder: SkipDedup, same ID, failing handler. Its terminal path must
	// not read or delete the ID-keyed waiter.
	intruder := d.Invoke(context.Background(), Request{ID: "same", Kind: Tool, Name: "boom", Input: input, SkipDedup: true})
	if intruder.Err == nil {
		t.Fatal("intruder invocation should fail")
	}
	d.mu.Lock()
	waiterCount := len(d.waiters["same"])
	d.mu.Unlock()
	if waiterCount != 1 {
		t.Fatalf("ID-keyed waiter count = %d, want 1", waiterCount)
	}

	close(release)
	owner := <-ownerDone
	waiter := <-waiterDone
	if owner.Err != nil || string(owner.Output) != `{"ok":true}` {
		t.Fatalf("owner result = err=%v out=%s, want success", owner.Err, owner.Output)
	}
	if waiter.Err != nil || waiter.Metadata.Status != "duplicate" {
		t.Fatalf("waiter result = %+v, want duplicate success", waiter)
	}
}

// TestSkipDedupBypassesFingerprintCheck pins the audit finding: a SkipDedup
// call never reads ID-keyed dedup state, so reusing an ID with DIFFERENT input
// must not fail with "invocation id reused with different input" — it executes
// fresh.
func TestSkipDedupBypassesFingerprintCheck(t *testing.T) {
	d := New(Policy{})
	var calls atomic.Int32
	if err := d.Register(Tool, "t", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	first := d.Invoke(context.Background(), Request{ID: "x", Kind: Tool, Name: "t", Input: json.RawMessage(`{"a":1}`)})
	if first.Err != nil {
		t.Fatal(first.Err)
	}
	// Same ID, different input, SkipDedup: must run fresh, never the reused-ID
	// error, and never answer from the first result.
	second := d.Invoke(context.Background(), Request{ID: "x", Kind: Tool, Name: "t", Input: json.RawMessage(`{"b":2}`), SkipDedup: true})
	if second.Err != nil {
		t.Fatalf("SkipDedup call with a reused ID failed: %v", second.Err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("handler executed %d times, want exactly 2", got)
	}
}
