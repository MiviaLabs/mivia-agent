// tool_cancel_nested_test.go proves ToolCancelReadyHook registers under a
// live coordinator pool dispatch: a MultiStepHandler-backed subagent task
// whose OnToolCancelReady is the hook forwards the pool-stamped
// TaskIdentity to the coordinator registry with real IDs. The coordinator
// package already proves the canceler reaches the run end to end
// (cancel_subagent_tool_call_test.go); this test proves the orchestrate
// hook wiring survives real dispatch instead of only isolated calls.
package orchestrate

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// hookProbeTool blocks one tool call until released.
type hookProbeTool struct {
	name    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *hookProbeTool) Name() string               { return b.name }
func (b *hookProbeTool) Description() string        { return "blocks until released" }
func (b *hookProbeTool) Parameters() map[string]any { return map[string]any{"type": "object"} }

func (b *hookProbeTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return `"probe-done"`, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// hookProbeCompleter issues one fixed tool call, then final text.
type hookProbeCompleter struct {
	mu       sync.Mutex
	calls    int
	callID   string
	toolName string
}

func (c *hookProbeCompleter) Name() string { return "hook-probe" }

func (c *hookProbeCompleter) ChatTurn(ctx context.Context, _ provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	if n == 1 {
		call := provider.ToolCall{ID: c.callID, Type: "function"}
		call.Function.Name = c.toolName
		call.Function.Arguments = "{}"
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	}
	return &provider.Response{Content: "done", FinishReason: "stop"}, nil
}

func (c *hookProbeCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil || r == nil {
		return "", err
	}
	return r.Content, nil
}

func (c *hookProbeCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}

// TestToolCancelReadyHookRegistersUnderPoolDispatch registers a
// MultiStepHandler with OnToolCancelReady=ToolCancelReadyHook(d) on a live
// coordinator pool and asserts the hook forwards real run/task IDs.
func TestToolCancelReadyHookRegistersUnderPoolDispatch(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	t.Cleanup(d.Close)
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := coordinator.New(repo, p)

	// The hook resolves the coordinator lazily from the package map, so
	// store the REAL coordinator (InitCoordinator type-asserts to
	// *coordinator.Coordinator, which the fake double is not).
	cleanup := StoreTestCoordinator(d, c, repo)
	defer cleanup()

	tool := &hookProbeTool{name: "probe-tool", entered: make(chan struct{}), release: make(chan struct{})}
	reg := tools.NewRegistry()
	reg.Register(tool)

	h := &subagents.MultiStepHandler{
		Completer:         &hookProbeCompleter{callID: "call-probe", toolName: tool.Name()},
		FullRegistry:      reg,
		SystemPrompt:      "test subagent",
		MaxSteps:          4,
		OnToolCancelReady: ToolCancelReadyHook(d),
	}
	if err := d.Register(runtime.Subagent, "probe", h); err != nil {
		t.Fatal(err)
	}

	rh, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "task1", Name: "probe", Input: json.RawMessage(`"do it"`)},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-tool.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("tool call never started within 10s")
	}
	// The hook fires when the run's cancel registry exists; poll the
	// coordinator for the registration via a cancel round-trip.
	deadline := time.Now().Add(10 * time.Second)
	for {
		ok, err := c.CancelSubagentToolCall(context.Background(), rh, "task1", "call-probe")
		if err != nil {
			t.Fatalf("CancelSubagentToolCall: %v", err)
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ToolCanceler for run %q task task1 never registered within 10s", rh.RunID())
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(tool.release)
	if _, err := c.Join(context.Background(), rh); err != nil {
		t.Fatal(err)
	}
}
