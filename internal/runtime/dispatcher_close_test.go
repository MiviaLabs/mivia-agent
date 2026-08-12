package runtime

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestDispatcherCloseReleasesDedupWaiters(t *testing.T) {
	t.Run("turn scoped", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		d := closeTestDispatcher(t, started, release)
		ownerReq := Request{ID: "owner", Kind: Tool, Name: "t", TurnID: "turn", Step: 1}
		ownerDone := make(chan Result, 1)
		go func() { ownerDone <- d.Invoke(context.Background(), ownerReq) }()
		<-started
		waiterDone := make(chan Result, 1)
		go func() {
			waiterDone <- d.Invoke(context.Background(), Request{ID: "waiter", Kind: Tool, Name: "t", TurnID: "turn", Step: 1})
		}()
		waitForInFlightWaiter(t, d, ownerReq)
		d.Close()
		assertClosedDispatcherResult(t, waiterDone)
		close(release)
		assertDispatcherResult(t, ownerDone)
	})

	t.Run("same ID later step", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		d := closeTestDispatcher(t, started, release)
		ownerReq := Request{ID: "same", Kind: Tool, Name: "t", TurnID: "turn", Step: 1, Budget: 1}
		ownerDone := make(chan Result, 1)
		go func() { ownerDone <- d.Invoke(context.Background(), ownerReq) }()
		<-started
		waiterDone := make(chan Result, 1)
		go func() {
			waiterDone <- d.Invoke(context.Background(), Request{ID: "same", Kind: Tool, Name: "t", TurnID: "turn", Step: 2, Budget: 1})
		}()
		waitForIDKeyedWaiterRegistered(t, d, "same")
		d.Close()
		assertClosedDispatcherResult(t, waiterDone)
		close(release)
		assertDispatcherResult(t, ownerDone)
	})

	t.Run("new call", func(t *testing.T) {
		var calls atomic.Int32
		d := New(Policy{})
		if err := d.Register(Skill, "t", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
			calls.Add(1)
			return json.RawMessage(`{"ok":true}`), nil
		})); err != nil {
			t.Fatal(err)
		}
		d.Close()
		result := d.Invoke(context.Background(), Request{ID: "after-close", Kind: Skill, Name: "t"})
		if result.Err == nil || result.Err.Error() != "dispatcher is closed" || result.Metadata.Status != "closed" {
			t.Fatalf("post-close result = %+v, want closed error", result)
		}
		if calls.Load() != 0 {
			t.Fatalf("post-close handler calls = %d, want 0", calls.Load())
		}
		invalid := d.Invoke(context.Background(), Request{ID: "invalid-after-close", Kind: Skill, Name: "t", Budget: -1})
		if invalid.Err == nil || invalid.Err.Error() != "dispatcher is closed" || invalid.Metadata.Status != "closed" {
			t.Fatalf("invalid post-close result = %+v, want closed error", invalid)
		}
	})
}

func TestDispatcherClosePreventsLateScopedState(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	d := New(Policy{PreInvokeHook: func(context.Context, Request) HookVerdict {
		close(entered)
		<-release
		return HookVerdict{}
	}})
	if err := d.Register(Tool, "t", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	done := make(chan Result, 1)
	go func() {
		done <- d.Invoke(context.Background(), Request{ID: "late", Kind: Tool, Name: "t", Scope: "scope"})
	}()
	<-entered
	d.Close()
	close(release)
	assertClosedDispatcherResult(t, done)
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.resources) != 0 {
		t.Fatalf("resources after close = %#v, want empty", d.resources)
	}
}

func closeTestDispatcher(t *testing.T, started chan<- struct{}, release <-chan struct{}) *Dispatcher {
	t.Helper()
	d := New(Policy{})
	if err := d.Register(Tool, "t", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	return d
}

func assertClosedDispatcherResult(t *testing.T, done <-chan Result) {
	t.Helper()
	select {
	case result := <-done:
		if result.Err == nil || result.Err.Error() != "dispatcher is closed" || result.Metadata.Status != "closed" {
			t.Fatalf("result = %+v, want closed dispatcher result", result)
		}
	case <-time.After(time.Second):
		t.Fatal("invocation did not return after dispatcher close")
	}
}

func assertDispatcherResult(t *testing.T, done <-chan Result) {
	t.Helper()
	select {
	case result := <-done:
		if result.Err != nil {
			t.Fatalf("owner result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("owner did not return after dispatcher close")
	}
}

// TestLateIDKeyedWaiterAfterOwnerDeliveryStillReceivesResult pins the
// stranded-waiter window: a same-ID duplicate that registers AFTER the owner's
// Invoke tail delivered to the ID-keyed waiters (completeIDKeyed, which runs
// after postInvoke attaches HookContext/HookRuns) but BEFORE the owner's
// active marker is removed must still receive the owner's result. The marker
// removal (releaseIDKeyed) drains the waiter slice with the owner's final
// result in the same critical section; without it the late waiter parks
// forever (the multi-waiter change replaced the old shared buffered channel
// with per-waiter channels drained exactly once).
func TestLateIDKeyedWaiterAfterOwnerDeliveryStillReceivesResult(t *testing.T) {
	hookEntered := make(chan struct{})
	hookRelease := make(chan struct{})
	d := New(Policy{PostInvokeHook: func(context.Context, Request, Result) HookResult {
		close(hookEntered)
		<-hookRelease
		return HookResult{}
	}})
	if err := d.Register(Tool, "t", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	ownerDone := make(chan Result, 1)
	go func() {
		ownerDone <- d.Invoke(context.Background(), Request{ID: "same", Kind: Tool, Name: "t", TurnID: "turn", Step: 1, Budget: 1})
	}()
	<-hookEntered
	// The owner's handler has returned and the owner is blocked in postInvoke,
	// before Invoke's tail runs completeIDKeyed and before releaseIDKeyed
	// removes the active marker. A same-ID, later-step call now registers as a
	// duplicate and must be released with the owner's result.
	waiterDone := make(chan Result, 1)
	go func() {
		waiterDone <- d.Invoke(context.Background(), Request{ID: "same", Kind: Tool, Name: "t", TurnID: "turn", Step: 2, Budget: 1})
	}()
	waitForIDKeyedWaiterRegistered(t, d, "same")
	close(hookRelease)
	assertDispatcherResult(t, ownerDone)
	select {
	case result := <-waiterDone:
		if result.Err != nil {
			t.Fatalf("late duplicate result = %+v, want the owner's result", result)
		}
		if string(result.Output) != `{"ok":true}` {
			t.Fatalf("late duplicate output = %s, want the owner's result", result.Output)
		}
		if result.Metadata.Status != "duplicate" {
			t.Fatalf("late duplicate status = %q, want duplicate", result.Metadata.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late duplicate waiter was never released with the owner's result")
	}
}
