package runtime

import (
	"context"
	"encoding/json"
	"runtime"
	"sync/atomic"
	"testing"
)

// D1: Dispatcher.execute wrote d.completed[req.ID] = result for every
// non-SkipDedup call, but the only reader of that map (reserve,
// dispatcher_reserve.go) is gated on turnDedupKey(req) == "": turn-scoped Tool
// calls are answered from the step-aware turn bucket, never the completed map.
// The write was therefore dead retention of the full Result - Output bytes up
// to the per-tool ceiling - for the whole session until Close. These tests pin
// that a turn-scoped call leaves no completed entry while the non-turn paths
// keep the ID-keyed behavior exactly.

// TestTurnScopedToolCallLeavesNoCompletedEntry: a Tool call with TurnID+Step
// (the shape every write-class agent-loop call carries, loop_tools.go) must
// not retain an ID-keyed completed entry, while its step-scoped bucket record
// must exist so a same-step re-issue still dedups.
func TestTurnScopedToolCallLeavesNoCompletedEntry(t *testing.T) {
	d := New(Policy{})
	if err := d.Register(Tool, "t", okHandler(`{"ran":true}`)); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"argv":["git","commit"]}`)
	req := Request{ID: "call-1", Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", Step: 1}
	if r := d.Invoke(context.Background(), req); r.Err != nil {
		t.Fatal(r.Err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.completed[req.ID]; ok {
		t.Fatal("turn-scoped call retained an ID-keyed completed entry (D1 dead retention)")
	}
	key, contentHash := turnDedupKey(req)
	if _, ok := d.turnResults[key][contentHash]; !ok {
		t.Fatal("turn-scoped call did not record its step-scoped bucket result")
	}
}

// TestNonTurnSkillStillRecordsCompletedEntry: a Skill call (no TurnID) keeps
// the ID-keyed completed entry - reserve reads it for exactly this shape.
func TestNonTurnSkillStillRecordsCompletedEntry(t *testing.T) {
	d := New(Policy{})
	if err := d.Register(Skill, "s", okHandler(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	req := Request{ID: "skill-1", Kind: Skill, Name: "s"}
	if r := d.Invoke(context.Background(), req); r.Err != nil {
		t.Fatal(r.Err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	entry, ok := d.completed[req.ID]
	if !ok {
		t.Fatal("non-turn call lost its ID-keyed completed entry")
	}
	if string(entry.Output) != `{"ok":true}` {
		t.Fatalf("completed entry output = %s, want %s", entry.Output, `{"ok":true}`)
	}
}

// TestNonTurnSameIDReissueStillDedups: a same-ID non-turn re-issue is answered
// duplicate from the completed map; the handler runs exactly once.
func TestNonTurnSameIDReissueStillDedups(t *testing.T) {
	d := New(Policy{})
	var calls atomic.Int32
	if err := d.Register(Skill, "s", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	req := Request{ID: "same", Kind: Skill, Name: "s"}
	first := d.Invoke(context.Background(), req)
	second := d.Invoke(context.Background(), req)
	if first.Err != nil || second.Err != nil {
		t.Fatalf("results errored: first=%+v second=%+v", first, second)
	}
	if second.Metadata.Status != "duplicate" {
		t.Fatalf("same-ID re-issue status = %q, want duplicate", second.Metadata.Status)
	}
	if string(second.Output) != string(first.Output) {
		t.Fatalf("re-issue output = %s, want recorded %s", second.Output, first.Output)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler executed %d times, want exactly 1", got)
	}
}

// waitForIDKeyedWaiterRegistered deterministically waits until the duplicate
// invocation's reserve has run and captured the ID-keyed waiter channel. The
// channel itself is created by the OWNER's reserve, so its existence proves
// nothing about the waiter: releasing the owner before the waiter's reserve
// would let the owner complete and delete active[id], turning the late waiter
// into a second owner (handler double-execution). Both calls carry Budget 1
// under the same budgetKey (their TurnID), so spent[key] == 2 proves the
// waiter passed reserve's budget charge; because the owner is still blocked in
// its handler, the waiter's active-check inside the SAME critical section
// resolves it as a duplicate. Polls dispatcher state under d.mu with
// runtime.Gosched between polls (no time.Sleep).
func waitForIDKeyedWaiterRegistered(t *testing.T, d *Dispatcher, id, budgetKey string) {
	t.Helper()
	for i := 0; i < 1_000_000; i++ {
		d.mu.Lock()
		charged := d.spent[budgetKey]
		d.mu.Unlock()
		if charged >= 2 {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("waiter for %q never passed reserve's budget charge", id)
}

// TestTurnScopedCallStillDeliversToIDKeyedWaiter: the ID-keyed waiter delivery
// inside execute is SEPARATE from the completed-map write and must stay
// ungated. A same-ID turn-scoped duplicate carrying a different Step (so the
// turn-dedup key differs and the turn bucket misses) collapses on the
// active/waiter path and receives the owner's result.
func TestTurnScopedCallStillDeliversToIDKeyedWaiter(t *testing.T) {
	d := New(Policy{})
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
		ownerDone <- d.Invoke(context.Background(), Request{ID: "same", Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", Step: 1, Budget: 1})
	}()
	<-started

	// Same ID, different step: the turn bucket key differs (step is part of
	// the key), so the ID-keyed active/waiter path is the only collision.
	// Both calls carry Budget 1 so the waiter's reserve is observable via the
	// shared turn budget counter (see waitForIDKeyedWaiterRegistered).
	waiterDone := make(chan Result, 1)
	go func() {
		waiterDone <- d.Invoke(context.Background(), Request{ID: "same", Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", Step: 2, Budget: 1})
	}()
	// Deterministically wait until the waiter's reserve has run, then release
	// the owner so its completion delivers the result to the waiter.
	waitForIDKeyedWaiterRegistered(t, d, "same", "turn:1")
	close(release)

	owner := <-ownerDone
	waiter := <-waiterDone
	if owner.Err != nil || waiter.Err != nil {
		t.Fatalf("results errored: owner=%+v waiter=%+v", owner, waiter)
	}
	if string(waiter.Output) != string(owner.Output) {
		t.Fatalf("waiter output = %s, want owner's %s", waiter.Output, owner.Output)
	}
	if waiter.Metadata.Status != "duplicate" {
		t.Fatalf("waiter status = %q, want duplicate", waiter.Metadata.Status)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler executed %d times, want exactly 1", got)
	}
}
