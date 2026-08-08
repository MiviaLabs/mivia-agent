package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// D2: on the two finish-only terminal paths inside execute (scope-acquisition
// failure, runaway output > ceiling×4), the finish closure recorded the turn
// bucket BEFORE postInvoke attached HookContext. Invoke's tail recordTurnResult
// then no-ops (the flight entry was already completed), so a same-step
// duplicate was answered with a pre-hook record - missing the HookContext and
// HookRuns the owner received (DC-9 dedup fidelity).

// TestTurnDedupDuplicateCarriesOwnerHookContext: a runaway handler (output >
// ceiling×4) with a PostInvokeHook. The owner gets the over-ceiling error with
// the hook context; a same-step identical re-issue must carry the SAME
// post-hook result (HookContext, HookRuns, Output byte-identical).
func TestTurnDedupDuplicateCarriesOwnerHookContext(t *testing.T) {
	const hookText = "post-hook ran for the runaway"
	policy := Policy{
		MaxOutputBytes: 4096,
		PostInvokeHook: func(context.Context, Request, Result) HookResult {
			return HookResult{
				Context: hookText,
				Runs:    []HookRun{{Event: "PostToolUse", Program: "fmt.sh"}},
			}
		},
	}
	var calls atomic.Int32
	d := toolDispatcher(t, policy, handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(strings.Repeat("x", 4096*4+1)), nil
	}))
	input := json.RawMessage(`{"argv":["runaway"]}`)
	owner := d.Invoke(context.Background(), Request{ID: "own-1", Kind: Tool, Name: "run_command", Input: input, TurnID: "turn:1", Step: 1})
	if owner.Err == nil {
		t.Fatal("runaway output must be destroyed as over-ceiling")
	}
	if owner.HookContext != hookText {
		t.Fatalf("owner HookContext = %q, want the hook text", owner.HookContext)
	}
	if len(owner.HookRuns) != 1 {
		t.Fatalf("owner HookRuns = %d, want the post-hook run", len(owner.HookRuns))
	}

	dup := d.Invoke(context.Background(), Request{ID: "own-2", Kind: Tool, Name: "run_command", Input: input, TurnID: "turn:1", Step: 1})
	if dup.Metadata.Status != "duplicate" {
		t.Fatalf("same-step re-issue status = %q, want duplicate", dup.Metadata.Status)
	}
	if dup.HookContext != hookText {
		t.Fatalf("duplicate HookContext = %q, want owner's %q (pre-hook record served?)", dup.HookContext, hookText)
	}
	if len(dup.HookRuns) != 1 || dup.HookRuns[0] != owner.HookRuns[0] {
		t.Fatalf("duplicate HookRuns = %+v, want owner's %+v", dup.HookRuns, owner.HookRuns)
	}
	if string(dup.Output) != string(owner.Output) {
		t.Fatalf("duplicate Output differs from owner's: %s vs %s", dup.Output, owner.Output)
	}
	if dup.Err == nil || dup.Err.Error() != owner.Err.Error() {
		t.Fatalf("duplicate Err = %v, want owner's %v", dup.Err, owner.Err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler executed %d times, want exactly 1", got)
	}
}

// TestTurnDedupDuplicateCarriesOwnerHookContextScopeFailure: the other
// finish-only terminal path - acquireScope fails when a holder occupies the
// scope and the caller's context is canceled. The owner's failed call must
// record a POST-hook bucket record so a same-content re-issue carries the hook
// context.
func TestTurnDedupDuplicateCarriesOwnerHookContextScopeFailure(t *testing.T) {
	const hookText = "post-hook ran for the scope failure"
	policy := Policy{
		PostInvokeHook: func(context.Context, Request, Result) HookResult {
			return HookResult{Context: hookText}
		},
	}
	d := New(policy)
	if err := d.Register(Tool, "t", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	// Holder: occupies the shared scope so the scoped call below fails
	// acquireScope. Different content, so no dedup collision with the scoped
	// call's flight key.
	holderStarted := make(chan struct{})
	holderRelease := make(chan struct{})
	if err := d.Register(Tool, "holder", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		close(holderStarted)
		<-holderRelease
		return json.RawMessage(`{"held":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	holderDone := make(chan Result, 1)
	go func() {
		holderDone <- d.Invoke(context.Background(), Request{ID: "holder-1", Kind: Tool, Name: "holder", Input: json.RawMessage(`{"argv":["hold"]}`), Scope: "shared-scope"})
	}()
	<-holderStarted

	input := json.RawMessage(`{"argv":["scoped"]}`)
	scopedCtx, cancel := context.WithCancel(context.Background())
	cancel()
	owner := d.Invoke(scopedCtx, Request{ID: "scoped-1", Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", Step: 1, Scope: "shared-scope"})
	if owner.Err == nil {
		t.Fatal("scope-acquisition failure must surface as an error")
	}
	if owner.HookContext != hookText {
		t.Fatalf("owner HookContext = %q, want the hook text", owner.HookContext)
	}

	// Same-content re-issue without the scope: answered from the step bucket,
	// must carry the owner's post-hook context.
	dup := d.Invoke(context.Background(), Request{ID: "scoped-2", Kind: Tool, Name: "t", Input: input, TurnID: "turn:1", Step: 1})
	if dup.Metadata.Status != "duplicate" {
		t.Fatalf("same-content re-issue status = %q, want duplicate", dup.Metadata.Status)
	}
	if dup.HookContext != hookText {
		t.Fatalf("duplicate HookContext = %q, want owner's %q (pre-hook record served?)", dup.HookContext, hookText)
	}
	if dup.Err == nil || dup.Err.Error() != owner.Err.Error() {
		t.Fatalf("duplicate Err = %v, want owner's %v", dup.Err, owner.Err)
	}

	close(holderRelease)
	holder := <-holderDone
	if holder.Err != nil {
		t.Fatal(holder.Err)
	}
}

// TestTurnDedupHandlerFailureDuplicateCarriesHookContext: control. The
// handler-error path already records POST-hook via Invoke's tail (execute
// returns failResult directly, without the finish closure), so the duplicate
// carries the hook context on HEAD and after - pins that the fix does not
// regress the consistent path.
func TestTurnDedupHandlerFailureDuplicateCarriesHookContext(t *testing.T) {
	const hookText = "post-hook ran for the handler failure"
	policy := Policy{
		PostInvokeHook: func(context.Context, Request, Result) HookResult {
			return HookResult{Context: hookText}
		},
	}
	var calls atomic.Int32
	d := toolDispatcher(t, policy, handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		calls.Add(1)
		return nil, errors.New("boom")
	}))
	input := json.RawMessage(`{"argv":["fail"]}`)
	owner := d.Invoke(context.Background(), Request{ID: "fail-1", Kind: Tool, Name: "run_command", Input: input, TurnID: "turn:1", Step: 1})
	if owner.Err == nil || owner.Err.Error() != "boom" {
		t.Fatalf("owner Err = %v, want boom", owner.Err)
	}
	if owner.HookContext != hookText {
		t.Fatalf("owner HookContext = %q, want the hook text", owner.HookContext)
	}
	dup := d.Invoke(context.Background(), Request{ID: "fail-2", Kind: Tool, Name: "run_command", Input: input, TurnID: "turn:1", Step: 1})
	if dup.Metadata.Status != "duplicate" {
		t.Fatalf("same-step re-issue status = %q, want duplicate", dup.Metadata.Status)
	}
	if dup.HookContext != hookText {
		t.Fatalf("duplicate HookContext = %q, want owner's %q", dup.HookContext, hookText)
	}
	if string(dup.Output) != string(owner.Output) {
		t.Fatalf("duplicate Output differs from owner's: %s vs %s", dup.Output, owner.Output)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler executed %d times, want exactly 1", got)
	}
}
