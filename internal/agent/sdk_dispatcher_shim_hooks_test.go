package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// hookAwareTool is a plain read-class tool used to exercise the shim's real
// dispatcher.Invoke -> emitHookRuns wiring, independent of dedup.
type hookAwareTool struct{}

func (h *hookAwareTool) Name() string               { return "hook-aware-tool" }
func (h *hookAwareTool) Description() string        { return "test" }
func (h *hookAwareTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (h *hookAwareTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func newHookDispatcher(t *testing.T, pre func(context.Context, runtime.Request) runtime.HookVerdict,
	post func(context.Context, runtime.Request, runtime.Result) runtime.HookResult) *runtime.Dispatcher {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(&hookAwareTool{})
	dispatcher, err := runtime.NewToolDispatcher(reg, runtime.Policy{PreInvokeHook: pre, PostInvokeHook: post})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	return dispatcher
}

func collectHookEventsFromShim(t *testing.T, dispatcher *runtime.Dispatcher) []Event {
	t.Helper()
	var got []Event
	shim := &dispatcherShim{
		inner: &sdkToolForName{name: "hook-aware-tool"},
		cli:   &hookAwareTool{},
		opts: Options{Dispatcher: dispatcher, SessionID: "s", OnEvent: func(e Event) {
			if e.Kind == EventHook {
				got = append(got, e)
			}
		}},
		turn: newSDKTurnState(),
	}
	if _, err := shim.Run(context.Background(), sdktools.InOut{Value: map[string]any{}}); err != nil {
		t.Fatalf("shim.Run: %v", err)
	}
	return got
}

// The real tool-call path (dispatcherShim.Run) must surface every hook run,
// not just denials - this is the production wiring emitHookRuns previously
// had no caller for.
func TestDispatcherShimEmitsHookEventsForPreAndPost(t *testing.T) {
	pre := func(context.Context, runtime.Request) runtime.HookVerdict {
		return runtime.HookVerdict{Runs: []runtime.HookRun{{Event: "PreToolUse", Program: "guard.sh"}}}
	}
	post := func(context.Context, runtime.Request, runtime.Result) runtime.HookResult {
		return runtime.HookResult{Runs: []runtime.HookRun{{Event: "PostToolUse", Program: "fmt.sh"}}}
	}
	got := collectHookEventsFromShim(t, newHookDispatcher(t, pre, post))

	if len(got) != 2 {
		t.Fatalf("got %d hook events, want 2 (one Pre, one Post): %+v", len(got), got)
	}
	if got[0].Name != "PreToolUse" || got[1].Name != "PostToolUse" {
		t.Fatalf("hook events must appear Pre before Post, got %+v", got)
	}
	for _, e := range got {
		if e.ToolCallID != "hook-aware-tool" {
			t.Fatalf("a hook event must correlate to its tool call, got %q", e.ToolCallID)
		}
	}
}

// A silent hook - one that ran and said nothing - must still produce a row.
// Without this, "did my hook fire?" has no answer short of instrumenting the
// script.
func TestDispatcherShimEmitsHookEventForASilentRun(t *testing.T) {
	post := func(context.Context, runtime.Request, runtime.Result) runtime.HookResult {
		return runtime.HookResult{Runs: []runtime.HookRun{{Event: "PostToolUse", Program: "fmt.sh"}}}
	}
	got := collectHookEventsFromShim(t, newHookDispatcher(t, nil, post))
	if len(got) != 1 {
		t.Fatalf("got %d hook events, want 1", len(got))
	}
	if got[0].Output != "" {
		t.Fatalf("a silent hook run must have empty output, got %q", got[0].Output)
	}
}

// A denied call still ran its PreToolUse hook - that run must still be
// reported even though the tool itself never executed and PostToolUse never
// fires.
func TestDispatcherShimEmitsHookEventForADeniedCall(t *testing.T) {
	pre := func(context.Context, runtime.Request) runtime.HookVerdict {
		return runtime.HookVerdict{Denied: true, Reason: "no", Runs: []runtime.HookRun{
			{Event: "PreToolUse", Program: "guard.sh", Denied: true, Output: "policy forbids this"},
		}}
	}
	got := collectHookEventsFromShim(t, newHookDispatcher(t, pre, nil))
	if len(got) != 1 {
		t.Fatalf("got %d hook events, want 1 (the denied Pre run)", len(got))
	}
	if got[0].Output != "policy forbids this" {
		t.Fatalf("the denial reason must reach the row, got %q", got[0].Output)
	}
}

// A dedup-served duplicate is answered with the OWNER's HookRuns (DC-9
// fidelity), which did not run for THIS call. Emitting them would report a
// hook execution that never happened for this invocation.
func TestDispatcherShimSuppressesHookEventsOnDuplicate(t *testing.T) {
	reg := tools.NewRegistry()
	tool := &duplicateAwareTool{}
	reg.Register(tool)
	post := func(context.Context, runtime.Request, runtime.Result) runtime.HookResult {
		return runtime.HookResult{Runs: []runtime.HookRun{{Event: "PostToolUse", Program: "fmt.sh"}}}
	}
	dispatcher, err := runtime.NewToolDispatcher(reg, runtime.Policy{PostInvokeHook: post})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)

	var events []Event
	newShim := func() *dispatcherShim {
		return &dispatcherShim{
			inner: &sdkToolForName{name: "dup-tool"},
			cli:   tool,
			opts: Options{Dispatcher: dispatcher, SessionID: "s", OnEvent: func(e Event) {
				if e.Kind == EventHook {
					events = append(events, e)
				}
			}},
			turn: newSDKTurnState(),
		}
	}
	if _, err := newShim().Run(context.Background(), sdktools.InOut{Value: map[string]any{}}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("owner call: got %d hook events, want 1", len(events))
	}
	events = nil
	if _, err := newShim().Run(context.Background(), sdktools.InOut{Value: map[string]any{}}); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("a dedup-served duplicate must emit no hook events, got %d: %+v", len(events), events)
	}
}
