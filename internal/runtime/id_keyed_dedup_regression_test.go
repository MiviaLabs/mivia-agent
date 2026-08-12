package runtime

// ID-keyed dedup regression tests for the two confirmed runtime findings:
//
// (1) runtime-id-keyed-dedup-pre-hook-result: execute() ran completeIDKeyed in
// its success tail, BEFORE Invoke's tail attached HookContext/HookRuns via
// postInvoke. An ID-keyed duplicate - an in-flight waiter or a completed-map
// re-delivery - was therefore answered with a PRE-hook result, losing the
// PostToolUse hook context and hook runs the owner received (DC-9 dedup
// fidelity). (2) runtime-budget-charged-for-id-keyed-inflight-dup: reserve()
// ran the ID-keyed in-flight duplicate check AFTER the budget gate, so a
// same-ID duplicate either consumed cumulative budget (double charge) or was
// spuriously rejected at the cap.
//
// Each test fails on HEAD and passes after the fix. Concurrency is pinned with
// the repo's poll-under-d.mu barrier (waitForIDKeyedWaiterRegistered), never
// time.Sleep.

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
)

const idKeyedHookContext = "id-keyed-post-hook"

// TestIDKeyedCompletedMapDuplicateCarriesOwnerHookContext: sequential and
// fully deterministic. A non-turn owner with a PostInvokeHook completes; a
// same-ID re-issue is answered duplicate from the completed map and must carry
// the owner's HookContext and HookRuns (HEAD serves a pre-hook record, so the
// duplicate's HookContext is empty -> RED). Kind is Tool: lifecycle hooks
// fire only for Kind == Tool, and a non-turn Tool call still takes the
// ID-keyed completed-map path (turnDedupKey == "").
func TestIDKeyedCompletedMapDuplicateCarriesOwnerHookContext(t *testing.T) {
	d := New(Policy{PostInvokeHook: func(context.Context, Request, Result) HookResult {
		return HookResult{Context: idKeyedHookContext, Runs: []HookRun{{Event: "PostToolUse", Program: "fmt.sh"}}}
	}})
	var calls atomic.Int32
	if err := d.Register(Tool, "t", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	req := Request{ID: "same", Kind: Tool, Name: "t"}
	first := d.Invoke(context.Background(), req)
	second := d.Invoke(context.Background(), req)
	if first.Err != nil || second.Err != nil {
		t.Fatalf("results errored: first=%+v second=%+v", first, second)
	}
	if second.Metadata.Status != "duplicate" {
		t.Fatalf("same-ID re-issue status = %q, want duplicate", second.Metadata.Status)
	}
	if first.HookContext != idKeyedHookContext {
		t.Fatalf("owner HookContext = %q, want %q", first.HookContext, idKeyedHookContext)
	}
	if second.HookContext != idKeyedHookContext {
		t.Fatalf("completed-map duplicate HookContext = %q, want the owner's %q (pre-hook record served?)", second.HookContext, idKeyedHookContext)
	}
	if len(second.HookRuns) != 1 || second.HookRuns[0] != first.HookRuns[0] {
		t.Fatalf("completed-map duplicate HookRuns = %+v, want the owner's %+v", second.HookRuns, first.HookRuns)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler executed %d times, want exactly 1", got)
	}
}

// TestIDKeyedInFlightDuplicateCarriesOwnerHookContext: owner blocked in its
// handler; a same-ID duplicate registers via waitForIDKeyedWaiterRegistered and
// must receive the owner's POST-hook result (HEAD delivers the pre-hook result
// at execute-end, so the duplicate's HookContext is empty -> RED).
func TestIDKeyedInFlightDuplicateCarriesOwnerHookContext(t *testing.T) {
	d := New(Policy{PostInvokeHook: func(context.Context, Request, Result) HookResult {
		return HookResult{Context: idKeyedHookContext}
	}})
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	if err := d.Register(Tool, "t", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		calls.Add(1)
		close(started)
		<-release
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"argv":["same-id"]}`)
	ownerDone := make(chan Result, 1)
	go func() {
		ownerDone <- d.Invoke(context.Background(), Request{ID: "same", Kind: Tool, Name: "t", Input: input, Budget: 1})
	}()
	<-started

	waiterDone := make(chan Result, 1)
	go func() {
		waiterDone <- d.Invoke(context.Background(), Request{ID: "same", Kind: Tool, Name: "t", Input: input, Budget: 1})
	}()
	waitForIDKeyedWaiterRegistered(t, d, "same")
	close(release)

	owner := <-ownerDone
	waiter := <-waiterDone
	if owner.Err != nil || waiter.Err != nil {
		t.Fatalf("results errored: owner=%+v waiter=%+v", owner, waiter)
	}
	if waiter.Metadata.Status != "duplicate" {
		t.Fatalf("waiter status = %q, want duplicate", waiter.Metadata.Status)
	}
	if waiter.HookContext != idKeyedHookContext {
		t.Fatalf("in-flight duplicate HookContext = %q, want the owner's post-hook %q (pre-hook result served?)", waiter.HookContext, idKeyedHookContext)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler executed %d times, want exactly 1", got)
	}
}

// TestIDKeyedInFlightDupIsNotBudgetRejected: with Policy{MaxBudget: 10} and
// owner+dup both Budget 10 (non-turn, so the shared budget key is the ID), the
// same-ID in-flight duplicate must be served the owner's result, never
// 'cumulative budget exceeded' (HEAD rejects the dup at the budget gate before
// the active check, so waitForIDKeyedWaiterRegistered Fatal -> RED).
func TestIDKeyedInFlightDupIsNotBudgetRejected(t *testing.T) {
	d := New(Policy{MaxBudget: 10})
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	if err := d.Register(Tool, "t", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		calls.Add(1)
		close(started)
		<-release
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"argv":["budgeted"]}`)
	ownerDone := make(chan Result, 1)
	go func() {
		ownerDone <- d.Invoke(context.Background(), Request{ID: "same", Kind: Tool, Name: "t", Input: input, Budget: 10})
	}()
	<-started

	waiterDone := make(chan Result, 1)
	go func() {
		waiterDone <- d.Invoke(context.Background(), Request{ID: "same", Kind: Tool, Name: "t", Input: input, Budget: 10})
	}()
	waitForIDKeyedWaiterRegistered(t, d, "same")
	close(release)

	owner := <-ownerDone
	waiter := <-waiterDone
	if owner.Err != nil {
		t.Fatalf("owner result errored: %+v", owner)
	}
	if waiter.Err != nil {
		t.Fatalf("in-flight duplicate was rejected instead of served: %+v", waiter)
	}
	if waiter.Metadata.Status != "duplicate" {
		t.Fatalf("waiter status = %q, want duplicate", waiter.Metadata.Status)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler executed %d times, want exactly 1", got)
	}
}

// TestIDKeyedInFlightDupDoesNotDoubleChargeBudget: owner+dup share the
// ParentID budget key (non-turn). After both complete, a fresh call sharing the
// ParentID with Budget 10 must run: the dup never charged, so spent stays at
// the owner's 10 and 10+10 == 20 <= MaxBudget (HEAD double-charged spent to 20,
// so the fresh call is spuriously rejected -> RED). The fresh call is a new ID
// and executes the handler a second time, so the started-channel close is
// once-only (sync.Once) rather than per-execution.
func TestIDKeyedInFlightDupDoesNotDoubleChargeBudget(t *testing.T) {
	d := New(Policy{MaxBudget: 20})
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	if err := d.Register(Tool, "t", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		calls.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"argv":["shared-key"]}`)
	ownerDone := make(chan Result, 1)
	go func() {
		ownerDone <- d.Invoke(context.Background(), Request{ID: "owner-id", ParentID: "session", Kind: Tool, Name: "t", Input: input, Budget: 10})
	}()
	<-started

	waiterDone := make(chan Result, 1)
	go func() {
		waiterDone <- d.Invoke(context.Background(), Request{ID: "owner-id", ParentID: "session", Kind: Tool, Name: "t", Input: input, Budget: 10})
	}()
	waitForIDKeyedWaiterRegistered(t, d, "owner-id")
	close(release)

	owner := <-ownerDone
	waiter := <-waiterDone
	if owner.Err != nil || waiter.Err != nil {
		t.Fatalf("results errored: owner=%+v waiter=%+v", owner, waiter)
	}
	if waiter.Metadata.Status != "duplicate" {
		t.Fatalf("waiter status = %q, want duplicate", waiter.Metadata.Status)
	}

	// Fresh call under the same ParentID budget key must still fit: the dup
	// never charged, so spent["session"] == 10, and 10+10 == 20 <= MaxBudget.
	fresh := d.Invoke(context.Background(), Request{ID: "fresh", ParentID: "session", Kind: Tool, Name: "t", Input: input, Budget: 10})
	if fresh.Err != nil {
		t.Fatalf("fresh call under the shared budget key was rejected: %+v", fresh)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("handler executed %d times, want exactly 2 (owner + fresh)", got)
	}
}
