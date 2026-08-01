package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func okHandler(payload string) Handler {
	return handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return json.RawMessage(payload), nil
	})
}

func toolDispatcher(t *testing.T, policy Policy, h Handler) *Dispatcher {
	t.Helper()
	d := New(policy)
	if err := d.Register(Tool, "run_command", h); err != nil {
		t.Fatalf("register: %v", err)
	}
	return d
}

func toolRequest(id string) Request {
	return Request{ID: id, Kind: Tool, Name: "run_command", Input: json.RawMessage(`{"argv":["git"]}`)}
}

// The no-hook path must be exactly today's behaviour: one nil compare.
func TestNilHookFieldsLeaveResultsUnchanged(t *testing.T) {
	d := toolDispatcher(t, Policy{}, okHandler(`{"ok":true}`))
	result := d.Invoke(context.Background(), toolRequest("a"))
	if result.Err != nil {
		t.Fatalf("err = %v", result.Err)
	}
	if string(result.Output) != `{"ok":true}` {
		t.Fatalf("output = %s", result.Output)
	}
	if result.Metadata.Status != "completed" {
		t.Fatalf("status = %q", result.Metadata.Status)
	}
	if result.HookContext != "" {
		t.Fatalf("no hooks configured, yet HookContext = %q", result.HookContext)
	}
}

// A policy block and a broken tool must not be indistinguishable in the audit
// sink, the TUI, or the CLI's status classification.
func TestPreToolUseDenyReturnsBlockedNotFailed(t *testing.T) {
	var called atomic.Int32
	policy := Policy{PreInvokeHook: func(context.Context, Request) HookVerdict {
		return HookVerdict{Denied: true, Reason: "commit uses a hook-bypass flag forbidden by policy"}
	}}
	d := toolDispatcher(t, policy, handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		called.Add(1)
		return json.RawMessage(`{"ran":true}`), nil
	}))

	result := d.Invoke(context.Background(), toolRequest("a"))
	if got := result.Metadata.Status; got != "blocked" {
		t.Fatalf("status = %q, want blocked", got)
	}
	if called.Load() != 0 {
		t.Fatal("a blocked call must not reach the handler")
	}
	// The reason must reach the model: that is the entire point of a block.
	if !strings.Contains(string(result.Output), "hook-bypass flag forbidden by policy") {
		t.Fatalf("output must carry the reason, got %s", result.Output)
	}
	var payload map[string]string
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("payload is not the status envelope: %v", err)
	}
	if payload["status"] != "blocked" {
		t.Fatalf("payload status = %q", payload["status"])
	}
	if result.Err == nil {
		t.Fatal("a blocked call must carry an error for callers that branch on it")
	}
}

// INV-AG-27's rule: a blocked call must not be misreported as a cancellation.
// The hook's reason is arbitrary text, so it cannot be spliced into the error
// the CLI classifies on.
func TestBlockedErrorAvoidsCancellationSubstrings(t *testing.T) {
	policy := Policy{PreInvokeHook: func(context.Context, Request) HookVerdict {
		return HookVerdict{Denied: true, Reason: "this operation was canceled by policy; deadline exceeded upstream"}
	}}
	d := toolDispatcher(t, policy, okHandler(`{}`))

	result := d.Invoke(context.Background(), toolRequest("a"))
	message := result.Err.Error()
	for _, forbidden := range []string{"canceled", "cancelled", "deadline exceeded"} {
		if strings.Contains(message, forbidden) {
			t.Errorf("blocked error %q contains %q and would classify as canceled or timed out", message, forbidden)
		}
	}
	// The verbatim reason still reaches the model through the payload.
	if !strings.Contains(string(result.Output), "canceled by policy") {
		t.Fatalf("the model must still receive the hook's own words, got %s", result.Output)
	}
}

// A block happens after reserve charged the budget and installed the active
// marker, so it must release its waiter exactly as a failure does.
func TestBlockedCallReleasesWaitersAndClearsTheActiveMarker(t *testing.T) {
	policy := Policy{PreInvokeHook: func(context.Context, Request) HookVerdict {
		return HookVerdict{Denied: true, Reason: "no"}
	}}
	d := toolDispatcher(t, policy, okHandler(`{}`))

	if got := d.Invoke(context.Background(), toolRequest("a")).Metadata.Status; got != "blocked" {
		t.Fatalf("status = %q", got)
	}
	d.mu.Lock()
	_, stillActive := d.active["a"]
	waiter := d.waiters["a"]
	d.mu.Unlock()
	if stillActive {
		t.Error("a blocked call must clear its active marker")
	}
	if waiter != nil {
		t.Error("a blocked call must release its waiter")
	}
}

// An event named PreToolUse that also fired on subagent dispatch would be a lie
// in a security-relevant name, and a matcher written against tool names would
// match subagent names by coincidence.
func TestHooksFireOnlyForKindTool(t *testing.T) {
	var seen []Kind
	policy := Policy{PreInvokeHook: func(_ context.Context, req Request) HookVerdict {
		seen = append(seen, req.Kind)
		return HookVerdict{}
	}}
	d := New(policy)
	for _, kind := range []Kind{Tool, Skill, Subagent} {
		if err := d.Register(kind, "thing", okHandler(`{}`)); err != nil {
			t.Fatalf("register %s: %v", kind, err)
		}
	}
	for i, kind := range []Kind{Tool, Skill, Subagent} {
		d.Invoke(context.Background(), Request{ID: string(rune('a' + i)), Kind: kind, Name: "thing"})
	}
	if len(seen) != 1 || seen[0] != Tool {
		t.Fatalf("hooks fired for %v, want [tool] only", seen)
	}
}

// A repeat req.ID returns the cached result before the hook point. Correct -
// the tool did not run - but a hook author would assume one fire per model
// tool call unless it is asserted.
func TestDeduplicatedInvocationFiresNoHook(t *testing.T) {
	var fires atomic.Int32
	policy := Policy{PreInvokeHook: func(context.Context, Request) HookVerdict {
		fires.Add(1)
		return HookVerdict{}
	}}
	d := toolDispatcher(t, policy, okHandler(`{"ok":true}`))

	d.Invoke(context.Background(), toolRequest("same"))
	d.Invoke(context.Background(), toolRequest("same"))
	if got := fires.Load(); got != 1 {
		t.Fatalf("hook fired %d times, want 1: the deduplicated call never ran the tool", got)
	}
}

// A PreToolUse gate a subagent escapes is not a gate: subagents run the same
// tools against the same workspace. This falls out of a struct copy today, so
// it is asserted as the decision it is.
func TestScopedSubagentDispatcherInheritsHookFuncs(t *testing.T) {
	policy := Policy{
		PreInvokeHook:  func(context.Context, Request) HookVerdict { return HookVerdict{Denied: true, Reason: "gate"} },
		PostInvokeHook: func(context.Context, Request, Result) string { return "post" },
	}
	parent := toolDispatcher(t, policy, okHandler(`{}`))

	derived := New(parent.Policy())
	if err := derived.Register(Tool, "run_command", okHandler(`{}`)); err != nil {
		t.Fatalf("register: %v", err)
	}
	result := derived.Invoke(context.Background(), toolRequest("a"))
	if result.Metadata.Status != "blocked" {
		t.Fatalf("a derived dispatcher escaped the gate: status = %q", result.Metadata.Status)
	}
}

func TestPostToolUseContextLandsInHookContext(t *testing.T) {
	policy := Policy{PostInvokeHook: func(_ context.Context, _ Request, result Result) string {
		if string(result.Output) != `{"ok":true}` {
			return ""
		}
		return "gofmt rewrote 2 files"
	}}
	d := toolDispatcher(t, policy, okHandler(`{"ok":true}`))

	result := d.Invoke(context.Background(), toolRequest("a"))
	if result.HookContext != "gofmt rewrote 2 files" {
		t.Fatalf("HookContext = %q", result.HookContext)
	}
	if string(result.Output) != `{"ok":true}` {
		t.Fatalf("hook context must never be spliced into Output, got %s", result.Output)
	}
}

// Appending hook stdout to Output would bypass the per-tool ceiling check
// entirely (INV-AG-25/26/27) and leave meta.OutputHash describing bytes the
// model never received.
func TestHookContextDoesNotChangeTheAuditHashOrPreview(t *testing.T) {
	plain := toolDispatcher(t, Policy{}, okHandler(`{"ok":true}`)).
		Invoke(context.Background(), toolRequest("a"))

	policy := Policy{PostInvokeHook: func(context.Context, Request, Result) string {
		return strings.Repeat("advice ", 200)
	}}
	hooked := toolDispatcher(t, policy, okHandler(`{"ok":true}`)).
		Invoke(context.Background(), toolRequest("a"))

	if hooked.Metadata.OutputHash != plain.Metadata.OutputHash {
		t.Fatal("hook context changed the audit hash; the record must describe the tool's bytes")
	}
	if hooked.Metadata.OutputPreview != plain.Metadata.OutputPreview {
		t.Fatal("hook context leaked into the audit preview")
	}
	if hooked.HookContext == "" {
		t.Fatal("the hook context itself was lost")
	}
}

// Hook advice is worth less than the tool result it accompanies: over-budget
// context is truncated, and the result survives whole.
func TestOversizedHookContextIsTruncatedAndKeepsTheResult(t *testing.T) {
	policy := Policy{PostInvokeHook: func(context.Context, Request, Result) string {
		return strings.Repeat("x", MaxHookContextBytes*4)
	}}
	d := toolDispatcher(t, policy, okHandler(`{"ok":true}`))

	result := d.Invoke(context.Background(), toolRequest("a"))
	if string(result.Output) != `{"ok":true}` {
		t.Fatalf("an over-budget hook must not destroy the tool result, got %s", result.Output)
	}
	if len(result.HookContext) > MaxHookContextBytes+256 {
		t.Fatalf("HookContext = %d bytes, past its bound", len(result.HookContext))
	}
	if !strings.Contains(result.HookContext, "truncated") {
		t.Fatal("truncation must be announced, not silent")
	}
	if result.Metadata.Status != "completed" {
		t.Fatalf("status = %q", result.Metadata.Status)
	}
}

// The hook context derives from the dispatcher's INCOMING ctx, never from
// execute's callCtx, whose deferred cancel has already fired by this point. A
// PostToolUse hook run on that context would silently never execute.
func TestPostToolUseRunsAfterTheCallTimeoutExpired(t *testing.T) {
	var ran atomic.Int32
	policy := Policy{PostInvokeHook: func(ctx context.Context, _ Request, _ Result) string {
		if ctx.Err() != nil {
			return ""
		}
		ran.Add(1)
		return "ran"
	}}
	d := toolDispatcher(t, policy, handlerFunc(func(ctx context.Context, _ Request) (json.RawMessage, error) {
		<-ctx.Done()
		return json.RawMessage(`{"ok":true}`), nil
	}))

	request := toolRequest("a")
	request.Timeout = 50 * time.Millisecond
	result := d.Invoke(context.Background(), request)
	if ran.Load() != 1 {
		t.Fatalf("the post hook must run on a live context after the call's own timeout fired; result=%+v", result.Metadata)
	}
}

// If a hook ever reaches Invoke - through a future handler type or a bug - the
// nested gate is skipped rather than recursing. MaxDepth would not catch it,
// because hook execution carries no depth. The guard is context-scoped, not
// process-wide: a process-wide flag would let one call's hook suppress another
// concurrent call's gate, which fails open.
func TestHookReentryIntoTheDispatcherDoesNotRecurse(t *testing.T) {
	var depth atomic.Int32
	var d *Dispatcher
	policy := Policy{PreInvokeHook: func(ctx context.Context, _ Request) HookVerdict {
		if depth.Add(1) > 4 {
			return HookVerdict{}
		}
		d.Invoke(ctx, toolRequest("nested"))
		return HookVerdict{}
	}}
	d = toolDispatcher(t, policy, okHandler(`{"ok":true}`))

	done := make(chan struct{})
	go func() {
		d.Invoke(context.Background(), toolRequest("outer"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a hook re-entering the dispatcher recursed instead of skipping the nested gate")
	}
	if got := depth.Load(); got != 1 {
		t.Fatalf("the nested invocation fired the gate %d times, want 1 (the outer one only)", got)
	}
}

// A concurrent invocation must keep its own gate while another call's hook is
// running. This is what a process-wide re-entrancy flag would break.
func TestConcurrentInvocationKeepsItsGateWhileAnotherHookRuns(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var gated atomic.Int32
	policy := Policy{PreInvokeHook: func(_ context.Context, req Request) HookVerdict {
		gated.Add(1)
		if req.ID == "slow" {
			entered <- struct{}{}
			<-release
		}
		return HookVerdict{}
	}}
	d := toolDispatcher(t, policy, okHandler(`{"ok":true}`))

	go d.Invoke(context.Background(), toolRequest("slow"))
	<-entered
	d.Invoke(context.Background(), toolRequest("fast"))
	close(release)

	if got := gated.Load(); got != 2 {
		t.Fatalf("the gate fired %d times, want 2: a concurrent call must not inherit another's hook scope", got)
	}
}
