package runtime

import (
	"context"
	"encoding/json"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// waitForInFlightWaiter deterministically waits until a duplicate invocation
// has attached to the owner's in-flight entry (entry.waiters non-empty). It
// polls the dispatcher's own state under d.mu with runtime.Gosched between
// polls — the waiter goroutine is already runnable, so this yields to it
// without any time.Sleep (the repo's test policy forbids sleeps).
func waitForInFlightWaiter(t *testing.T, d *Dispatcher, req Request) {
	t.Helper()
	key, contentHash := turnDedupKey(req)
	flightKey := key + "\x00" + contentHash
	for i := 0; i < 1_000_000; i++ {
		d.mu.Lock()
		entry := d.inFlight[flightKey]
		attached := entry != nil && len(entry.waiters) > 0
		d.mu.Unlock()
		if attached {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("waiter never attached to the in-flight entry")
}

// TestInFlightEntryOwnerOnlyCompletion pins the audit finding F1: only the
// invocation that reserved the flight key (the owner) may complete the in-flight
// entry or write its bucket record. A DIFFERENT caller with identical content
// whose validation fails (e.g. budget exceeded) must not tear the entry down,
// deliver its error to a legitimate waiter, or poison the bucket.
func TestInFlightEntryOwnerOnlyCompletion(t *testing.T) {
	d := New(Policy{MaxBudget: 10})
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	if err := d.Register(Tool, "t", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		calls.Add(1)
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
	input := json.RawMessage(`{"x":1}`)
	req := func(id string, budget int) Request {
		return Request{ID: id, Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", ParentID: "session", Step: 1, Budget: budget}
	}

	// Owner reserves the flight key and enters the handler.
	ownerDone := make(chan Result, 1)
	go func() { ownerDone <- d.Invoke(context.Background(), req("owner", 5)) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("owner never entered the handler")
	}

	// Intruder: identical content but invalid budget. It fails validation and
	// must NOT complete the owner's entry or write the bucket.
	intruder := d.Invoke(context.Background(), req("intruder", 100))
	if intruder.Err == nil {
		t.Fatal("intruder invocation should fail validation (budget exceeded)")
	}

	// A legitimate duplicate attaches to the in-flight entry while the owner
	// still runs; it must receive the OWNER's real result, not the intruder's
	// spurious error. Deterministically wait for the attach, then release the
	// owner so its completion delivers the result to the waiter.
	waiterDone := make(chan Result, 1)
	go func() { waiterDone <- d.Invoke(context.Background(), req("waiter", 5)) }()
	waitForInFlightWaiter(t, d, req("waiter", 5))
	close(release)

	owner := <-ownerDone
	waiter := <-waiterDone
	if owner.Err != nil || string(owner.Output) != `{"ok":true}` {
		t.Fatalf("owner result = err=%v out=%s, want success", owner.Err, owner.Output)
	}
	if waiter.Err != nil || string(waiter.Output) != `{"ok":true}` {
		t.Fatalf("waiter result = err=%v out=%s (in-flight entry poisoned by intruder?)", waiter.Err, waiter.Output)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler executed %d times, want exactly 1", got)
	}

	// A later same-step identical call dedups against the OWNER's recorded
	// result, not the intruder's spurious error.
	fourth := d.Invoke(context.Background(), req("fourth", 5))
	if fourth.Err != nil || string(fourth.Output) != `{"ok":true}` || fourth.Metadata.Status != "duplicate" {
		t.Fatalf("fourth result = err=%v out=%s status=%q, want owner's result as duplicate",
			fourth.Err, fourth.Output, fourth.Metadata.Status)
	}
}

// TestSameIDReissueInLaterStepReruns pins the audit finding F2: a same-ID,
// same-input re-issue in a LATER step of the same turn must re-run (the
// step-scoped content bucket misses, and the ID-keyed completed map must NOT
// replay the step-1 result). A same-step re-issue of the same ID still dedups.
func TestSameIDReissueInLaterStepReruns(t *testing.T) {
	d := New(Policy{})
	var calls atomic.Int32
	if err := d.Register(Tool, "t", handlerFunc(func(_ context.Context, _ Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"argv":["go","test"]}`)
	mk := func(id string, step int) Request {
		return Request{ID: id, Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", ParentID: "session", Step: step}
	}

	first := d.Invoke(context.Background(), mk("call-x", 1))
	if first.Err != nil {
		t.Fatal(first.Err)
	}

	// Same ID, same input, later step: must re-run, not replay step-1's result.
	second := d.Invoke(context.Background(), mk("call-x", 2))
	if second.Err != nil {
		t.Fatal(second.Err)
	}
	if second.Metadata.Status == "duplicate" {
		t.Fatal("same-ID re-issue in a later step must re-run, not replay the step-1 result")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("handler executed %d times, want exactly 2", got)
	}

	// Same ID, same input, same step: still dedups via the step-scoped bucket.
	third := d.Invoke(context.Background(), mk("call-x", 1))
	if third.Metadata.Status != "duplicate" {
		t.Fatal("same-ID same-step re-issue must dedup")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("handler executed %d times, want still 2", got)
	}
}
