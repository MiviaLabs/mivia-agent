package subagents

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// recordingCompleter is the subagent's model, and what it SAW is the question
// these tests ask. multi_step returns the model's final text, so asserting on
// the handler's output would only prove what the mock was hardcoded to say -
// the hook's text reaches the nested model through the tool message, and that
// is the thing that must not be lost on the way down.
type recordingCompleter struct {
	mu        sync.Mutex
	toolCalls []provider.ToolCall
	calls     int
	seen      []string // content of every tool-role message the model received
}

func (c *recordingCompleter) Name() string { return "recording" }

func (c *recordingCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "done", nil
}

func (c *recordingCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	if w != nil {
		_, _ = w.Write([]byte("done"))
	}
	return "done", nil
}

func (c *recordingCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	for _, message := range req.Messages {
		if message.Role == provider.RoleTool {
			c.seen = append(c.seen, message.Content)
		}
	}
	next := c.calls
	c.calls++
	c.mu.Unlock()

	if next < len(c.toolCalls) {
		return &provider.Response{ToolCalls: []provider.ToolCall{c.toolCalls[next]}}, nil
	}
	return &provider.Response{Content: "done"}, nil
}

// toolMessages is everything the nested model was told a tool returned.
func (c *recordingCompleter) toolMessages() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.seen, "\n---\n")
}

// A PreToolUse gate a subagent escapes is not a gate. Subagents run the same
// tools against the same workspace, so a hook that fires for the root loop and
// not for a spawned one would be a gate with a documented way around it -
// "spawn an agent and do it there".
//
// runtime.TestScopedSubagentDispatcherInheritsHookFuncs asserts the Policy
// struct copy. These go the other way and drive a real multi_step dispatch, so
// they would catch the propagation being lost anywhere between the parent
// dispatcher and the tool actually running: parentPolicy(), newScopedLoop(),
// the agent loop's dispatcher wiring, or the goroutine the tool call lands on.

// hookedTool records whether the tool body ran. A blocked call must never reach
// it - "blocked" and "ran but returned an error" are different events.
type hookedTool struct{ ran atomic.Bool }

func (t *hookedTool) Name() string               { return "guarded" }
func (t *hookedTool) Description() string        { return "test tool behind a lifecycle gate" }
func (t *hookedTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *hookedTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.ran.Store(true)
	return "tool body ran", nil
}

// subagentCalling builds a handler whose scoped loop makes exactly one tool
// call, through a parent dispatcher carrying the given hook policy. The
// completer is returned so a test can assert on what the nested model saw.
func subagentCalling(t *testing.T, tool tools.Tool, policy runtime.Policy) (*MultiStepHandler, *recordingCompleter) {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(tool)
	parent, err := runtime.NewToolDispatcher(reg, policy)
	if err != nil {
		t.Fatalf("parent dispatcher: %v", err)
	}
	t.Cleanup(parent.Close)

	call := provider.ToolCall{ID: "nested-1", Type: "function"}
	call.Function.Name = tool.Name()
	call.Function.Arguments = `{}`
	completer := &recordingCompleter{toolCalls: []provider.ToolCall{call}}
	return &MultiStepHandler{
		Completer:    completer,
		FullRegistry: reg,
		Dispatcher:   parent,
		Model:        "test-model",
		MaxSteps:     3,
	}, completer
}

func TestSubagentToolCallFiresTheParentPreToolUseHook(t *testing.T) {
	var fired atomic.Int32
	var sawKind atomic.Value
	tool := &hookedTool{}
	h, _ := subagentCalling(t, tool, runtime.Policy{
		PreInvokeHook: func(_ context.Context, req runtime.Request) runtime.HookVerdict {
			fired.Add(1)
			sawKind.Store(string(req.Kind))
			return runtime.HookVerdict{}
		},
	})

	if _, err := h.Invoke(context.Background(), runtime.Request{Name: "multi_step", Input: json.RawMessage(`"task"`)}); err != nil {
		t.Fatalf("multi_step: %v", err)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("the gate fired %d times for a subagent's tool call, want 1", got)
	}
	if got, _ := sawKind.Load().(string); got != string(runtime.Tool) {
		t.Fatalf("hook saw kind %q; a subagent's TOOL call is still a tool call", got)
	}
	if !tool.ran.Load() {
		t.Fatal("an allowing gate must not stop the tool")
	}
}

// The half that matters. A gate that fires inside a subagent but cannot stop
// anything there is decoration.
func TestSubagentToolCallIsBlockedByTheParentGate(t *testing.T) {
	tool := &hookedTool{}
	h, nested := subagentCalling(t, tool, runtime.Policy{
		PreInvokeHook: func(context.Context, runtime.Request) runtime.HookVerdict {
			return runtime.HookVerdict{Denied: true, Reason: "policy forbids this tool in subagents"}
		},
	})

	if _, err := h.Invoke(context.Background(), runtime.Request{Name: "multi_step", Input: json.RawMessage(`"task"`)}); err != nil {
		t.Fatalf("a blocked nested tool must not fail the whole subagent: %v", err)
	}
	if tool.ran.Load() {
		t.Fatal("the tool body ran despite the gate denying it")
	}
	// The reason reaches the SUBAGENT's model - the one that has to decide what
	// to do next, and that will simply retry a call it cannot explain.
	if seen := nested.toolMessages(); !strings.Contains(seen, "policy forbids this tool in subagents") {
		t.Fatalf("the block reason never reached the nested model: %q", seen)
	}
}

// PostToolUse rides the same copy, and its advisory text must land on the
// nested result rather than being dropped on the way back up.
func TestSubagentToolCallCarriesPostToolUseContext(t *testing.T) {
	var fired atomic.Int32
	h, nested := subagentCalling(t, &hookedTool{}, runtime.Policy{
		PostInvokeHook: func(context.Context, runtime.Request, runtime.Result) runtime.HookResult {
			fired.Add(1)
			return runtime.HookResult{
				Context: "formatter touched 1 file",
				Runs:    []runtime.HookRun{{Event: "PostToolUse", Program: "fmt.sh"}},
			}
		},
	})

	if _, err := h.Invoke(context.Background(), runtime.Request{Name: "multi_step", Input: json.RawMessage(`"task"`)}); err != nil {
		t.Fatalf("multi_step: %v", err)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("the reactive hook fired %d times in the subagent, want 1", got)
	}
	seen := nested.toolMessages()
	if !strings.Contains(seen, "formatter touched 1 file") {
		t.Fatalf("hook context was lost between the nested tool and its model: %q", seen)
	}
	// Framed there too. A subagent's model reads hook output under exactly the
	// delimiter the root loop's does, or the framing has a hole shaped like a
	// subagent - which is the shape every other gate in this file exists to
	// prevent.
	if !strings.Contains(seen, "<lifecycle-hook-output>") || !strings.Contains(seen, "</lifecycle-hook-output>") {
		t.Fatalf("nested hook output reached the model unframed: %q", seen)
	}
}

// A handler with no parent dispatcher builds an empty Policy. Nil hook fields
// must stay nil rather than becoming a call on a nil func.
func TestSubagentWithNoParentDispatcherRunsNoHooks(t *testing.T) {
	reg := tools.NewRegistry()
	tool := &hookedTool{}
	reg.Register(tool)
	h := &MultiStepHandler{FullRegistry: reg}

	scoped, err := h.newScopedLoop()
	if err != nil {
		t.Fatalf("newScopedLoop: %v", err)
	}
	defer scoped.dispatcher.Close()

	result := scoped.dispatcher.Invoke(context.Background(), runtime.Request{
		ID: "a", Kind: runtime.Tool, Name: "guarded", Input: json.RawMessage(`{}`),
	})
	if result.Err != nil {
		t.Fatalf("err = %v", result.Err)
	}
	if result.HookContext != "" || len(result.HookRuns) != 0 {
		t.Fatalf("a parentless subagent invented hooks: context=%q runs=%+v", result.HookContext, result.HookRuns)
	}
}
