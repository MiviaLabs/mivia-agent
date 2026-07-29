package subagents

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type markedPrivilegedTool struct{ privilegedSideEffectTool }

func (t *markedPrivilegedTool) Name() string { return "future_control" }
func (t *markedPrivilegedTool) Privileged()  {}

type oversizedTool struct{}

func (oversizedTool) Name() string               { return "oversized" }
func (oversizedTool) Description() string        { return "returns test output" }
func (oversizedTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (oversizedTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "output exceeds the scoped policy", nil
}

type parentOnlyHandler struct{ executed bool }

func (h *parentOnlyHandler) Invoke(context.Context, runtime.Request) (json.RawMessage, error) {
	h.executed = true
	return json.RawMessage(`"parent"`), nil
}

type rendezvousTool struct {
	mu      sync.Mutex
	arrived int
	calls   int
	release chan struct{}
}

func (*rendezvousTool) Name() string               { return "rendezvous" }
func (*rendezvousTool) Description() string        { return "coordinates concurrent tests" }
func (*rendezvousTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *rendezvousTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	t.mu.Lock()
	t.calls++
	t.arrived++
	if t.arrived == 2 {
		close(t.release)
	}
	t.mu.Unlock()
	select {
	case <-t.release:
		return string(args), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestMultiStepHandlerScopedDispatcherInheritsPolicy(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(oversizedTool{})
	parent, err := runtime.NewToolDispatcher(reg, runtime.Policy{MaxOutputBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	h := &MultiStepHandler{FullRegistry: reg, Dispatcher: parent}
	scoped, err := h.newScopedLoop()
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.dispatcher.Close()
	result := scoped.dispatcher.Invoke(context.Background(), runtime.Request{ID: "oversized", Kind: runtime.Tool, Name: "oversized", Input: json.RawMessage(`{}`)})
	if result.Err == nil || !strings.Contains(result.Err.Error(), "output budget exceeded") {
		t.Fatalf("result=%+v, want inherited output budget", result)
	}
}

func TestMultiStepHandlerScopedDispatcherIsNotParent(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(oversizedTool{})
	parentHandler := &parentOnlyHandler{}
	parent := runtime.New(runtime.Policy{})
	if err := parent.Register(runtime.Tool, "oversized", parentHandler); err != nil {
		t.Fatal(err)
	}
	call := provider.ToolCall{ID: "same-name", Type: "function"}
	call.Function.Name = "oversized"
	call.Function.Arguments = `{}`
	h := &MultiStepHandler{Completer: &multiStepMockCompleter{name: "test", toolCalls: []provider.ToolCall{call}}, FullRegistry: reg, Dispatcher: parent, Model: "test-model", MaxSteps: 3}
	if _, err := h.Invoke(context.Background(), runtime.Request{Name: "multi_step", Input: json.RawMessage(`"task"`)}); err != nil {
		t.Fatal(err)
	}
	if parentHandler.executed {
		t.Fatal("nested loop used the parent dispatcher")
	}
}

func TestRestrictedRegistryExcludesPrivilegedMarker(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&markedPrivilegedTool{})
	h := &MultiStepHandler{FullRegistry: reg}
	if _, ok := h.restrictedRegistry().Get("future_control"); ok {
		t.Fatal("restricted registry retained an unlisted privileged tool")
	}
}

func TestMultiStepHandlerConcurrentSubagentsDoNotShareToolCallIDs(t *testing.T) {
	reg := tools.NewRegistry()
	rendezvous := &rendezvousTool{release: make(chan struct{})}
	reg.Register(rendezvous)
	parent, err := runtime.NewToolDispatcher(reg, runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	newHandler := func(argument string) *MultiStepHandler {
		call := provider.ToolCall{ID: "call_1", Type: "function"}
		call.Function.Name = "rendezvous"
		call.Function.Arguments = argument
		return &MultiStepHandler{Completer: &multiStepMockCompleter{name: "test", toolCalls: []provider.ToolCall{call}}, FullRegistry: reg, Dispatcher: parent, Model: "test-model", MaxSteps: 3, ToolTimeout: 100 * time.Millisecond}
	}
	results := make(chan error, 2)
	for _, h := range []*MultiStepHandler{newHandler(`{"value":"one"}`), newHandler(`{"value":"two"}`)} {
		go func(h *MultiStepHandler) {
			_, err := h.Invoke(context.Background(), runtime.Request{Name: "multi_step", Input: json.RawMessage(`"task"`)})
			results <- err
		}(h)
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent subagent failed: %v", err)
		}
	}
	rendezvous.mu.Lock()
	calls := rendezvous.calls
	rendezvous.mu.Unlock()
	if calls != 2 {
		t.Fatalf("tool executions=%d, want distinct execution for both call_1 invocations", calls)
	}
}
