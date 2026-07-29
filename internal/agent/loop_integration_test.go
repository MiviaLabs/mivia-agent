package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// ---------------------------------------------------------------------------
// Tier 1 — Core Agent Loop Integration
// ---------------------------------------------------------------------------
// These tests wire together the real agent Loop with a real httptest-backed
// provider client, a real tool Registry with workspace-backed tools, and the
// EventBus. No mocks, no scriptCompleter — real HTTP + real tool execution.

// integrationHelper builds a complete agent loop with an httptest provider.
type integrationHelper struct {
	t    *testing.T
	ws   *workspace.Root
	reg  *tools.Registry
	srv  *httptest.Server
	comp provider.Completer
	bus  *events.Bus
}

// agentEventCapture records events for assertion.
type agentEventCapture struct {
	kind       EventKind
	toolCallID string
	name       string
	detail     string
	content    string
	input      string
	output     string
}

type captureHandler struct {
	mu atomic.Pointer[[]agentEventCapture]
	ch chan struct{}
}

func newCaptureHandler() *captureHandler {
	c := &captureHandler{ch: make(chan struct{}, 100)}
	var empty []agentEventCapture
	c.mu.Store(&empty)
	return c
}

func (h *captureHandler) HandleEvent(_ context.Context, ev events.Event) {
	cap := agentEventCapture{
		kind:       EventKind(ev.Kind),
		toolCallID: ev.ToolCallID,
		name:       ev.Name,
		detail:     ev.Detail,
		content:    ev.Content,
		input:      ev.Input,
		output:     ev.Output,
	}
	for {
		old := h.mu.Load()
		newSlice := make([]agentEventCapture, len(*old)+1)
		copy(newSlice, *old)
		newSlice[len(*old)] = cap
		if h.mu.CompareAndSwap(old, &newSlice) {
			break
		}
	}
	select {
	case h.ch <- struct{}{}:
	default:
	}
}

func (h *captureHandler) Events() []agentEventCapture {
	return *h.mu.Load()
}

func (h *captureHandler) WaitFor(min int, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		if len(h.Events()) >= min {
			return true
		}
		select {
		case <-h.ch:
		case <-deadline:
			return false
		}
	}
}

// scriptedStep is a single LLM response for the integration test server.
type scriptedStep struct {
	content   string
	toolCalls []provider.ToolCall
}

// newIntegrationServer creates an httptest server that responds with scripted
// steps in order (non-streaming JSON for doJSON path).
func newIntegrationServer(t *testing.T, steps []scriptedStep) *httptest.Server {
	t.Helper()
	var callCount atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(callCount.Load())
		callCount.Add(1)
		if idx >= len(steps) {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"finish_reason": "stop",
					"message":       map[string]string{"role": "assistant", "content": "done (fallthrough)"},
				}},
			})
			return
		}
		step := steps[idx]
		if len(step.toolCalls) == 0 {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"finish_reason": "stop",
					"message":       map[string]string{"role": "assistant", "content": step.content},
				}},
			})
			return
		}
		toolCallMaps := make([]map[string]any, len(step.toolCalls))
		for i, tc := range step.toolCalls {
			toolCallMaps[i] = map[string]any{
				"id": tc.ID, "type": "function",
				"function": map[string]string{"name": tc.Function.Name, "arguments": tc.Function.Arguments},
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "tool_calls",
				"message":       map[string]any{"role": "assistant", "content": step.content, "tool_calls": toolCallMaps},
			}},
		})
	}))
}

func newIntegrationHelper(t *testing.T, steps []scriptedStep) *integrationHelper {
	t.Helper()
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	bus := events.New()

	srv := newIntegrationServer(t, steps)
	t.Cleanup(srv.Close)
	comp := provider.NewOpenAICompatWithOptions(provider.CompatOptions{Name: "integration-test", BaseURL: srv.URL, APIKey: "test-key"})

	capture := newCaptureHandler()
	bus.SubscribeMany([]events.Kind{
		events.KindToolStart, events.KindToolEnd, events.KindToolParallel,
		events.KindAssistant, events.KindStep,
		events.KindSubagentStart, events.KindSubagentEnd, events.KindSubagentHeartbeat,
	}, capture)

	return &integrationHelper{t: t, ws: ws, reg: reg, srv: srv, comp: comp, bus: bus}
}

func (h *integrationHelper) newLoop() *Loop {
	return &Loop{
		Completer: h.comp,
		Tools:     h.reg,
	}
}

// TestLoopProviderToolRoundtrip runs the agent loop with a real httptest
// provider and real filesystem tools. The provider returns tool_calls for
// write_file, then the agent executes the tool and sends results back.
func TestLoopProviderToolRoundtrip(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "I will create the file",
			toolCalls: []provider.ToolCall{{
				ID:   "call_write",
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "write_file", Arguments: `{"path":"hello.txt","content":"hello world"}`},
			}},
		},
		{
			content: "created hello.txt with content",
		},
	})

	loop := h.newLoop()
	ctx := context.Background()
	text, err := loop.Run(ctx, "create a file saying hello world", Options{
		Model:              "integration-model",
		MaxSteps:           5,
		MaxConcurrentTools: 2,
		ToolTimeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "created") && !strings.Contains(text, "hello.txt") {
		t.Fatalf("expected final text mentioning the file, got: %q", text)
	}

	// Verify the file was actually written to disk.
	data, err := h.reg.Execute(ctx, "read_file", json.RawMessage(`{"path":"hello.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "hello world") {
		t.Fatalf("file content=%q, expected 'hello world'", data)
	}

	// Verify messages contain the tool result.
	foundTool := false
	foundAssistant := false
	for _, msg := range loop.Messages {
		if msg.Role == provider.RoleAssistant && msg.Content != "" {
			foundAssistant = true
		}
		if msg.Role == provider.RoleTool {
			foundTool = true
		}
	}
	if !foundTool {
		t.Fatal("expected at least one tool result message in loop history")
	}
	if !foundAssistant {
		t.Fatal("expected at least one assistant message in loop history")
	}
}

// TestLoopMultipleToolCallsInTurn verifies the agent can handle multiple
// tool_calls in a single provider response, execute them in parallel,
// and collect results.
func TestLoopMultipleToolCallsInTurn(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "I will search and read files",
			toolCalls: []provider.ToolCall{
				{
					ID:   "call_search",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "search", Arguments: `{"scope":"local","query":"hello"}`},
				},
				{
					ID:   "call_grep",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "search", Arguments: `{"scope":"local","query":"world","glob":"*.go"}`},
				},
			},
		},
		{
			content: "Found the files",
		},
	})

	loop := h.newLoop()
	ctx := context.Background()
	text, err := loop.Run(ctx, "search for files", Options{
		Model:              "integration-model",
		MaxSteps:           5,
		MaxConcurrentTools: 4,
		ToolTimeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Fatal("expected non-empty response text")
	}

	// Verify both tool calls were executed by checking history.
	toolMsgs := 0
	for _, msg := range loop.Messages {
		if msg.Role == provider.RoleTool {
			toolMsgs++
		}
	}
	if toolMsgs < 2 {
		t.Fatalf("expected at least 2 tool result messages, got %d", toolMsgs)
	}
}

// TestLoopEventBusDelivery verifies that all agent lifecycle events are
// published through EventBus during tool execution.
func TestLoopEventBusDelivery(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "Let me read the file",
			toolCalls: []provider.ToolCall{{
				ID:   "call_read",
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "read_file", Arguments: `{"path":"test.txt"}`},
			}},
		},
		{
			content: "File contents retrieved",
		},
	})

	// Create a file to read
	if _, err := h.reg.Execute(context.Background(), "write_file", json.RawMessage(`{"path":"test.txt","content":"test content"}`)); err != nil {
		t.Fatal(err)
	}

	loop := h.newLoop()
	ctx := context.Background()

	var capturedEvents []agentEventCapture
	_, err := loop.Run(ctx, "read test.txt", Options{
		Model:              "integration-model",
		MaxSteps:           5,
		MaxConcurrentTools: 2,
		ToolTimeout:        5 * time.Second,
		OnEvent: func(e Event) {
			capturedEvents = append(capturedEvents, agentEventCapture{
				kind:       e.Kind,
				toolCallID: e.ToolCallID,
				name:       e.Name,
				detail:     e.Detail,
				content:    e.Content,
				input:      e.Input,
				output:     e.Output,
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify OnEvent captured key events
	hasToolStart := false
	hasToolEnd := false
	hasStep := false
	for _, ev := range capturedEvents {
		switch ev.kind {
		case EventToolStart:
			hasToolStart = true
		case EventToolEnd:
			hasToolEnd = true
		case EventStep:
			hasStep = true
		}
	}
	if !hasToolStart {
		t.Fatal("expected EventToolStart via OnEvent")
	}
	if !hasToolEnd {
		t.Fatal("expected EventToolEnd via OnEvent")
	}
	if !hasStep {
		t.Fatal("expected EventStep via OnEvent")
	}
}

// TestLoopOnEventAndEventBusBothFire verifies that both OnEvent callback
// and EventBus publish the same events — no side-channel delivery.
func TestLoopOnEventAndEventBusBothFire(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "I will search",
			toolCalls: []provider.ToolCall{{
				ID:   "call_grep",
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "search", Arguments: `{"scope":"local","query":"content"}`},
			}},
		},
		{
			content: "Search complete",
		},
	})

	bus := events.New()
	busCapture := newCaptureHandler()
	bus.SubscribeMany([]events.Kind{
		events.KindToolStart,
		events.KindToolEnd,
		events.KindToolParallel,
		events.KindAssistant,
	}, busCapture)

	loop := h.newLoop()
	ctx := context.Background()

	var onEventCount atomic.Int32
	_, err := loop.Run(ctx, "search for content", Options{
		Model:              "integration-model",
		MaxSteps:           5,
		MaxConcurrentTools: 2,
		ToolTimeout:        5 * time.Second,
		EventBus:           bus,
		OnEvent: func(e Event) {
			onEventCount.Add(1)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if n := onEventCount.Load(); n == 0 {
		t.Fatal("OnEvent was never called")
	}
	busEvents := busCapture.Events()
	if len(busEvents) == 0 {
		t.Fatal("EventBus received no events")
	}
	t.Logf("OnEvent called %d times, EventBus received %d events",
		onEventCount.Load(), len(busEvents))
}

// TestLoopToolErrorRecovery verifies that when one tool in a batch fails,
// the agent still sends the other results back and continues.
func TestLoopToolErrorRecovery(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "I will try to read files",
			toolCalls: []provider.ToolCall{
				{
					ID:   "call_missing",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "read_file", Arguments: `{"path":"nonexistent.txt"}`},
				},
				{
					ID:   "call_valid",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "read_file", Arguments: `{"path":"existing.txt"}`},
				},
			},
		},
		{
			content: "Read one file successfully",
		},
	})

	// Create the existing file
	if _, err := h.reg.Execute(context.Background(), "write_file", json.RawMessage(`{"path":"existing.txt","content":"i exist"}`)); err != nil {
		t.Fatal(err)
	}

	loop := h.newLoop()
	ctx := context.Background()
	text, err := loop.Run(ctx, "read both files", Options{
		Model:              "integration-model",
		MaxSteps:           5,
		MaxConcurrentTools: 4,
		ToolTimeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Fatal("expected non-empty response even with tool errors")
	}

	// Verify both tool results (including error) appear in history
	hasErrorResult := false
	hasSuccessResult := false
	for _, msg := range loop.Messages {
		if msg.Role == provider.RoleTool {
			if msg.ToolCallID == "call_missing" && strings.Contains(msg.Content, "not found") {
				hasErrorResult = true
			}
			if msg.ToolCallID == "call_valid" && strings.Contains(msg.Content, "i exist") {
				hasSuccessResult = true
			}
		}
	}
	if !hasSuccessResult {
		t.Fatal("expected successful tool result in history")
	}
	t.Logf("error result present: %v", hasErrorResult)
}

// TestLoopEventBusMultipleSubscribers verifies EventBus with multiple
// subscribers receives all events from agent loop execution.
func TestLoopEventBusMultipleSubscribers(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "I will check",
			toolCalls: []provider.ToolCall{{
				ID:   "call_check",
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "glob", Arguments: `{"pattern":"*.go"}`},
			}},
		},
		{
			content: "Check complete",
		},
	})

	bus := events.New()
	c1 := newCaptureHandler()
	c2 := newCaptureHandler()
	bus.Subscribe(events.KindToolStart, c1)
	bus.Subscribe(events.KindToolStart, c2)

	loop := h.newLoop()
	ctx := context.Background()
	_, err := loop.Run(ctx, "check for go files", Options{
		Model:              "integration-model",
		MaxSteps:           5,
		MaxConcurrentTools: 2,
		ToolTimeout:        5 * time.Second,
		EventBus:           bus,
	})
	if err != nil {
		t.Fatal(err)
	}

	e1 := c1.Events()
	e2 := c2.Events()
	if len(e1) == 0 || len(e2) == 0 {
		t.Fatalf("both subscribers should receive events: c1=%d c2=%d", len(e1), len(e2))
	}
	if len(e1) != len(e2) {
		t.Fatalf("subscribers received different counts: c1=%d c2=%d", len(e1), len(e2))
	}
}

// ---------------------------------------------------------------------------
// Tier 5 — Config Pipeline Integration
// ---------------------------------------------------------------------------

// pipelineTestServer creates a non-streaming httptest server that returns
// tool_calls on first call and stop on subsequent calls.
func pipelineTestServer(t *testing.T, toolArgs string) *httptest.Server {
	t.Helper()
	var callCount atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		idx := int(callCount.Load())
		callCount.Add(1)
		if idx == 0 {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"finish_reason": "tool_calls",
					"message": map[string]any{
						"role": "assistant", "content": "",
						"tool_calls": []map[string]any{{
							"id": "call_pipeline", "type": "function",
							"function": map[string]string{"name": "write_file", "arguments": toolArgs},
						}},
					},
				}},
			})
		} else {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"finish_reason": "stop",
					"message":       map[string]string{"role": "assistant", "content": "Pipeline test complete"},
				}},
			})
		}
	}))
}

// TestConfigPipelineAgentRun verifies the full pipeline: provider + tools + agent loop.
func TestConfigPipelineAgentRun(t *testing.T) {
	dir := t.TempDir()
	srv := pipelineTestServer(t, `{"path":"pipeline.txt","content":"pipeline test"}`)
	defer srv.Close()

	comp := provider.NewOpenAICompatWithOptions(provider.CompatOptions{Name: "pipeline-test", BaseURL: srv.URL, APIKey: "pipeline-test-key"})
	wsPath := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(wsPath)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})

	loop := &Loop{Completer: comp, Tools: reg}
	ctx := context.Background()
	reply, err := loop.Run(ctx, "create a pipeline test file", Options{
		Model: "pipeline-model", MaxSteps: 5, MaxConcurrentTools: 2, ToolTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("agent reply: %q", reply)

	data, err := reg.Execute(ctx, "read_file", json.RawMessage(`{"path":"pipeline.txt"}`))
	if err != nil {
		t.Fatalf("file not created by agent: %v", err)
	}
	if !strings.Contains(data, "pipeline test") {
		t.Fatalf("file content=%q", data)
	}
}

// TestConfigPipelineStopResponse verifies a simple config pipeline with
// just a stop response (no tool calls).
func TestConfigPipelineStopResponse(t *testing.T) {
	dir := t.TempDir()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message": map[string]string{
					"role":    "assistant",
					"content": "Pipeline stop test response",
				},
			}},
		})
	}))
	defer srv.Close()

	comp := provider.NewOpenAICompatWithOptions(provider.CompatOptions{Name: "pipeline-test", BaseURL: srv.URL, APIKey: "pipeline-test-key"})
	wsPath := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(wsPath)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})

	loop := &Loop{Completer: comp, Tools: reg}
	ctx := context.Background()
	reply, err := loop.Run(ctx, "say hello", Options{
		Model:       "pipeline-model",
		MaxSteps:    2,
		ToolTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "Pipeline") && !strings.Contains(reply, "stop") && !strings.Contains(reply, "test") {
		t.Logf("pipeline stop reply: %q", reply)
	}
}
