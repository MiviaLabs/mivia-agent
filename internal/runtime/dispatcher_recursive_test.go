package runtime

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestDispatcherRecursiveDuplicateDoesNotReleaseOtherWaiters(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	d := New(Policy{})
	if err := d.Register(Tool, "t", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	ownerReq := Request{ID: "same", ParentID: "root", Kind: Tool, Name: "t", Budget: 1}
	ownerDone := make(chan Result, 1)
	go func() { ownerDone <- d.Invoke(context.Background(), ownerReq) }()
	<-started
	waiterDone := make(chan Result, 1)
	go func() {
		waiterDone <- d.Invoke(context.Background(), Request{ID: "same", ParentID: "root", Kind: Tool, Name: "t", Budget: 1})
	}()
	waitForIDKeyedWaiterRegistered(t, d, "same", "root")

	recursive := d.Invoke(context.Background(), Request{ID: "same", ParentID: "same", Kind: Tool, Name: "t"})
	if recursive.Err == nil {
		t.Fatal("recursive invocation succeeded")
	}
	select {
	case waiter := <-waiterDone:
		t.Fatalf("recursive result released another waiter: %+v", waiter)
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	owner := <-ownerDone
	waiter := <-waiterDone
	if owner.Err != nil || waiter.Err != nil || waiter.Metadata.Status != "duplicate" {
		t.Fatalf("results = owner:%+v waiter:%+v", owner, waiter)
	}
}

// TestDispatcherSkipDedupCallWithParentIDMayExecute: a SkipDedup call never
// reads or writes the ID-keyed dedup state, so its ID equaling its parent's ID
// cannot self-deadlock and must execute. Regression: the recursion guard moved
// before reserve rejected every ParentID==ID call, including read-class tool
// calls (SkipDedup) whose model-supplied ID happened to equal the enclosing
// agent call's ID (TestLedgerReadPageIsNotTailCutByTheAgentLoop).
func TestDispatcherSkipDedupCallWithParentIDMayExecute(t *testing.T) {
	d := New(Policy{})
	var calls atomic.Int32
	if err := d.Register(Tool, "t", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	result := d.Invoke(context.Background(), Request{ID: "same", ParentID: "same", Kind: Tool, Name: "t", SkipDedup: true})
	if result.Err != nil {
		t.Fatalf("SkipDedup call with ParentID == ID = %+v, want execution", result)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
	// The non-SkipDedup form is still rejected as recursive.
	rejected := d.Invoke(context.Background(), Request{ID: "same", ParentID: "same", Kind: Tool, Name: "t"})
	if rejected.Err == nil {
		t.Fatal("non-SkipDedup call with ParentID == ID succeeded; want recursive rejection")
	}
}
