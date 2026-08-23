package subagents

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// steerSinkCompleter plays a two-step subagent turn: the first Chat
// returns a tool call (so the run crosses a step boundary), the second
// returns the final text. It records every request so a test can
// assert what the model saw at each iteration.
type steerSinkCompleter struct {
	mu       sync.Mutex
	requests []provider.Request
	calls    int
}

func (c *steerSinkCompleter) Name() string { return "steer-sink" }

func (c *steerSinkCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.calls++
	n := c.calls
	c.mu.Unlock()
	if n == 1 {
		call := provider.ToolCall{ID: "sc-1", Type: "function"}
		call.Function.Name = "steer_signal_tool"
		call.Function.Arguments = `{}`
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	}
	return &provider.Response{Content: "recovered after steer", FinishReason: "stop"}, nil
}

func (c *steerSinkCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil || r == nil {
		return "", err
	}
	return r.Content, nil
}

func (c *steerSinkCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}

func (c *steerSinkCompleter) seen() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.requests...)
}

// steerSignalTool queues one parent steer into the mailbox when it
// runs, simulating a mid-run parent steer landing while the subagent
// executes a tool batch.
type steerSignalTool struct{ mailbox *steerMailbox }

func (t *steerSignalTool) Name() string               { return "steer_signal_tool" }
func (t *steerSignalTool) Description() string        { return "queues a parent steer" }
func (t *steerSignalTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *steerSignalTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, ResourceKey: "path:steer-signal"}
}
func (t *steerSignalTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.mailbox.push(runtime.ParentMessage{Kind: "steer", Body: "focus on the schema"})
	return "signaled", nil
}

// steerMailbox is the test's parent→child mailbox: one queued steer,
// drained once.
type steerMailbox struct {
	mu      sync.Mutex
	pending []runtime.ParentMessage
}

func (m *steerMailbox) push(msg runtime.ParentMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending = append(m.pending, msg)
}

func (m *steerMailbox) drain() []runtime.ParentMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.pending
	m.pending = nil
	return out
}

func (m *steerMailbox) hasPending() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending) > 0
}

// TestSubagentParentSteerDeliveredAtBoundaryOnSDKBackend pins the
// BeforeStep carrier end to end on the default (SDK) backend: a
// parent steer queued while the subagent runs is delivered framed as
// a user-role message at the next step boundary, the model sees it in
// the request for that iteration, and the run completes normally
// (no errSteerInterrupt).
func TestSubagentParentSteerDeliveredAtBoundaryOnSDKBackend(t *testing.T) {
	mailbox := &steerMailbox{}
	reg := tools.NewRegistry()
	reg.Register(&steerSignalTool{mailbox: mailbox})
	comp := &steerSinkCompleter{}
	h := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Model:        "test-model",
		MaxSteps:     4,
	}
	ctx := runtime.ContextWithMailboxAccess(context.Background(), runtime.MailboxAccess{
		Drain:   mailbox.drain,
		Pending: mailbox.hasPending,
	})
	result, err := h.Invoke(ctx, runtime.Request{
		ID: "task-1", Name: "multi_step", Kind: runtime.Subagent,
		Input: json.RawMessage(`"do the work"`),
	})
	if err != nil {
		t.Fatalf("steered subagent run must complete, got %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "completed" {
		t.Fatalf("status = %v, want completed (payload %s)", payload["status"], result)
	}
	if out, _ := payload["output"].(string); !strings.Contains(out, "recovered after steer") {
		t.Fatalf("output = %v, want the post-steer final text (payload %s)", payload["output"], result)
	}
	requests := comp.seen()
	if len(requests) < 2 {
		t.Fatalf("model calls = %d, want >= 2 (tool step plus post-steer step)", len(requests))
	}
	// The steer was queued DURING the tool batch, so it cannot be in
	// the first request and must be in the second, framed.
	if sawFramedSteer(requests[0]) {
		t.Fatal("steer leaked into the pre-batch request; it was queued mid-run")
	}
	if !sawFramedSteer(requests[1]) {
		t.Fatalf("framed steer missing from the boundary request: %+v", requests[1].Messages)
	}
}

func sawFramedSteer(req provider.Request) bool {
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "<parent-message>") && strings.Contains(m.Content, "focus on the schema") {
			return true
		}
	}
	return false
}
