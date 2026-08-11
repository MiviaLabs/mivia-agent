package runtime

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

// TestDispatcherNoDedupAcrossDifferentParentIDs characterizes (does not
// assert a bug fix for) the per-turn dedup key's ParentID scoping: it is
// TurnID+ParentID by design (turnDedupKey), "so identical calls in different
// task contexts never collide" - correct for same-task retries, but it means
// two different concurrent subagent tasks (distinct ParentID) issuing the
// IDENTICAL tool call within the same turn get no dedup protection from each
// other. This test pins that current behavior (both calls execute) so a
// future change to cross-agent coordination has an explicit before/after,
// and it explains why two sibling agents can independently duplicate a
// side-effecting call (e.g. search_replace) on a shared resource: dedup
// alone will never catch it. This test PASSES today - it documents the gap,
// it does not prove a defect in isolation (see the tools-layer idempotency
// tests, which do fail today).
func TestDispatcherNoDedupAcrossDifferentParentIDs(t *testing.T) {
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
	input := json.RawMessage(`{"argv":["dedup","cross-agent"]}`)
	start := make(chan struct{})
	results := make(chan Result, 2)
	for _, parent := range []string{"task-A", "task-B"} {
		go func(parent string) {
			<-start
			results <- d.Invoke(context.Background(), Request{
				ID: "id-" + parent, Kind: Tool, Name: "t", Input: input,
				TurnID: "turn:1", ParentID: parent,
			})
		}(parent)
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
		t.Fatalf("handler executed %d times, want 2 (no dedup across different ParentIDs, by current design)", got)
	}
}
