package agent

// Wave 1 task B (RED tests): pin the intended Surface-hook behavior that
// Loop.Run must implement in w1c. The hook (Options.Surface) is NOT called by
// the loop yet, so every test below fails with an assertion failure (never a
// compile error) while the surface is never refreshed per step.

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// surfaceHookDispatcher builds a real runtime dispatcher over reg for surface
// swaps. Reused by several tests; fatal on construction failure.
func surfaceHookDispatcher(t *testing.T, reg *tools.Registry) *runtime.Dispatcher {
	t.Helper()
	d, err := runtime.NewToolDispatcher(reg, runtime.Policy{})
	if err != nil {
		t.Fatalf("NewToolDispatcher: %v", err)
	}
	return d
}

// bigResultTool returns an oversized body so a small MaxToolResultChars forces
// the loop to spool the truncation into the step's RemainderSpool.
type bigResultTool struct {
	name string
	body string
}

func (t *bigResultTool) Name() string               { return t.name }
func (t *bigResultTool) Description() string        { return "big result tool" }
func (t *bigResultTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *bigResultTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, ResourceKey: "path:big"}
}
func (t *bigResultTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return t.body, nil
}

// TestSurfaceHookRefreshesToolSurfacePerStep pins that the loop re-reads the
// host surface at the top of EVERY step: a tool absent from the run-start
// registry but present in the step-2 surface must actually execute. While the
// hook is never invoked, the step-2 call is denied by the stale registry, the
// tool's Execute never runs, and no successful tool message lands in history.
func TestSurfaceHookRefreshesToolSurfacePerStep(t *testing.T) {
	startTool := &scheduledTestTool{name: "start_tool", class: tools.ExecutionRead, key: "path:start"}
	reg := tools.NewRegistry()
	reg.Register(startTool)

	var newExecuted atomic.Int32
	newTool := &scheduledTestTool{name: "new_tool", class: tools.ExecutionRead, key: "path:new", started: &newExecuted}
	newReg := tools.NewRegistry()
	newReg.Register(startTool)
	newReg.Register(newTool)
	newDisp := surfaceHookDispatcher(t, newReg)

	comp := &scriptCompleter{
		steps: []provider.Response{
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("1", "start_tool", `{}`)}},
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("2", "new_tool", `{}`)}},
			{Content: "done", FinishReason: "stop"},
		},
	}
	loop := &Loop{Completer: comp, Tools: reg}
	_, err := loop.Run(context.Background(), "run", Options{Model: "m",
		MaxSteps: 5,
		Surface: func() Surface {
			return Surface{Registry: newReg, Dispatcher: newDisp, ToolSpecs: newReg.OpenAITools()}
		},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if newExecuted.Load() != 1 {
		t.Fatalf("new tool executed %d times, want 1: the Surface hook must refresh the tool surface per step so step-2's new tool actually runs (hook is currently never invoked)", newExecuted.Load())
	}
	found := false
	for _, m := range loop.Messages {
		if m.Role == provider.RoleTool && m.Name == "new_tool" && strings.Contains(m.Content, "secret-result") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no successful tool result for new_tool in history; messages: %+v", loop.Messages)
	}
}

// TestSurfaceHookCalledExactlyOncePerStep pins the invocation cadence: the
// hook runs once per executed step after the first, no more, no less. Step 1
// is excluded: nothing can be staged before the first provider call, and a
// turn that dies there must not publish. While uncalled, the counter stays 0
// and the assertion fails.
func TestSurfaceHookCalledExactlyOncePerStep(t *testing.T) {
	var invocations atomic.Int32
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "t1", class: tools.ExecutionRead, key: "path:a"})
	steps := []provider.Response{
		{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("1", "t1", `{}`)}},
		{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("2", "t1", `{}`)}},
		{Content: "done", FinishReason: "stop"},
	}
	comp := &scriptCompleter{steps: steps}
	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "run", Options{Model: "m",
		MaxSteps: 5,
		Surface: func() Surface {
			invocations.Add(1)
			return Surface{}
		},
	}); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	want := len(steps) - 1 // 2 steps after the first: the second tool step + the closing stop step
	if got := invocations.Load(); got != int32(want) {
		t.Fatalf("Surface hook invoked %d times, want exactly %d (once per executed step after the first); the hook is not wired into the loop", got, want)
	}
}

// TestSurfaceHookSwapsDispatcher pins that the step's Dispatcher field is the
// one used to execute that step's tool calls. The step-2 surface supplies a
// dispatcher built over a registry containing marker_tool; only if that
// dispatcher runs the call does the marker execute. While uncalled, the loop
// falls back to its own dispatcher over the stale registry and marker_tool is
// denied, so the marker never fires.
func TestSurfaceHookSwapsDispatcher(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "start_tool", class: tools.ExecutionRead, key: "path:start"})

	var swappedExecuted atomic.Int32
	swappedReg := tools.NewRegistry()
	swappedReg.Register(&scheduledTestTool{name: "marker_tool", class: tools.ExecutionRead, key: "path:marker", started: &swappedExecuted})
	swappedDisp := surfaceHookDispatcher(t, swappedReg)

	comp := &scriptCompleter{
		steps: []provider.Response{
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("1", "start_tool", `{}`)}},
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("2", "marker_tool", `{}`)}},
			{Content: "done", FinishReason: "stop"},
		},
	}
	loop := &Loop{Completer: comp, Tools: reg}
	_, err := loop.Run(context.Background(), "run", Options{Model: "m",
		MaxSteps: 5,
		Surface: func() Surface {
			return Surface{Registry: swappedReg, Dispatcher: swappedDisp, ToolSpecs: swappedReg.OpenAITools()}
		},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if swappedExecuted.Load() != 1 {
		t.Fatalf("marker_tool executed %d times, want 1: the step-2 tool call must go through the Surface-swapped dispatcher (hook is currently never invoked)", swappedExecuted.Load())
	}
}

// TestSurfaceHookSwapsRemainderSpool pins that the step's RemainderSpool is
// the one used to spool truncated tool-result bodies. Step 2 returns an
// oversized body with a small MaxToolResultChars; only the surface-swapped
// spool's backing store can end up non-empty. While uncalled, the loop spools
// into its own (nil) spool, so the swapped store stays empty and the
// assertion fails.
//
// The Surface rotates Dispatcher alongside Registry, matching every real
// production Surface hook (internal/chat/session_turn_surface.go always
// pairs them via resolveTurnExecutionSurface): a Registry-only rotation with
// no matching Dispatcher has no real caller and behaves differently across
// backends (the legacy loop's executeToolsParallel re-scopes a fresh
// dispatcher per batch over whatever registry is current when Dispatcher is
// nil; the SDK path's dispatcher is scoped once at run start and does not
// follow a Registry-only rotation).
func TestSurfaceHookSwapsRemainderSpool(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "start_tool", class: tools.ExecutionRead, key: "path:start"})

	swappedStore := remainder.NewMemoryStore()
	swappedSpool := remainder.NewSpool(swappedStore)
	bigReg := tools.NewRegistry()
	bigReg.Register(&bigResultTool{name: "big_tool", body: strings.Repeat("B", 4096)})
	bigDisp := surfaceHookDispatcher(t, bigReg)

	comp := &scriptCompleter{
		steps: []provider.Response{
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("1", "start_tool", `{}`)}},
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("2", "big_tool", `{}`)}},
			{Content: "done", FinishReason: "stop"},
		},
	}
	loop := &Loop{Completer: comp, Tools: reg}
	_, err := loop.Run(context.Background(), "run", Options{Model: "m",
		MaxSteps:           5,
		SessionID:          "sess-swap",
		MaxToolResultChars: 64,
		RemainderSpool:     nil, // loop's own spool; the swapped one must take over per step
		Surface: func() Surface {
			return Surface{Registry: bigReg, Dispatcher: bigDisp, RemainderSpool: swappedSpool, ToolSpecs: bigReg.OpenAITools()}
		},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if n := swappedStore.Len(); n == 0 {
		t.Fatalf("swapped spool store holds %d entries, want >0: the step-2 truncation must be spooled into the Surface-swapped RemainderSpool (hook is currently never invoked)", n)
	}
}

// TestSurfaceHookNilFieldsKeepStepSurface pins that a Surface whose fields are
// all nil is a no-op: the step still runs against the loop's own registry and
// its tools behave exactly as in step 1. It also asserts the hook WAS invoked
// (so the nil-return path is actually exercised); that second assertion is
// what makes the test fail while the hook is never called.
func TestSurfaceHookNilFieldsKeepStepSurface(t *testing.T) {
	var hookCalls atomic.Int32
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "same_tool", class: tools.ExecutionRead, key: "path:same"})

	comp := &scriptCompleter{
		steps: []provider.Response{
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("1", "same_tool", `{}`)}},
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("2", "same_tool", `{}`)}},
			{Content: "done", FinishReason: "stop"},
		},
	}
	loop := &Loop{Completer: comp, Tools: reg}
	text, err := loop.Run(context.Background(), "run", Options{Model: "m",
		MaxSteps: 5,
		Surface: func() Surface {
			hookCalls.Add(1)
			return Surface{} // all-nil: keep the loop's own registry/dispatcher/specs/spool
		},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want %q", text, "done")
	}
	if hookCalls.Load() == 0 {
		t.Fatalf("Surface hook never invoked: an all-nil Surface must still be called once per step and behave as a no-op (hook is currently not wired into the loop)")
	}
	// Both steps ran same_tool against the unchanged loop registry: exactly two
	// successful tool results must be in history.
	ok := 0
	for _, m := range loop.Messages {
		if m.Role == provider.RoleTool && m.Name == "same_tool" && strings.Contains(m.Content, "secret-result") {
			ok++
		}
	}
	if ok != 2 {
		t.Fatalf("same_tool succeeded %d times, want 2 (nil Surface fields must leave the step surface unchanged); messages: %+v", ok, loop.Messages)
	}
}
