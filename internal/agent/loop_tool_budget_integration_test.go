package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestLoopQueuedToolInBatchStillExecutes drives a realistic over-subscribed
// batch through the real provider transport: the model asks for more tools
// than there are workers, so the trailing call sits in the queue longer than
// its own per-call budget. It must still run, and - whatever happens - every
// tool_call_id must come back with exactly one tool result message, or the
// next provider request is malformed.
func TestLoopQueuedToolInBatchStillExecutes(t *testing.T) {
	started := new(atomic.Int32)
	calls := []provider.ToolCall{
		tc("call_slow_1", "batch_slow", `{}`),
		tc("call_slow_2", "batch_slow", `{}`),
		tc("call_slow_3", "batch_slow", `{}`),
		tc("call_slow_4", "batch_slow", `{}`),
		tc("call_queued", "batch_queued", `{}`),
	}
	h := newIntegrationHelper(t, []scriptedStep{
		{content: "running the batch", toolCalls: calls},
		{content: "batch complete"},
	})
	h.reg.Register(&scheduledTestTool{name: "batch_slow", class: tools.ExecutionRead, delay: 300 * time.Millisecond})
	h.reg.Register(&scheduledTestTool{name: "batch_queued", class: tools.ExecutionRead, delay: time.Millisecond, started: started})

	loop := h.newLoop()
	if _, err := loop.Run(context.Background(), "run the batch", Options{
		Model:              "integration-model",
		MaxSteps:           5,
		MaxConcurrentTools: 4,
		ToolTimeout:        200 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}

	if got := started.Load(); got != 1 {
		t.Fatalf("queued tool executions=%d, want 1 (budget burned while queued)", got)
	}

	seen := make(map[string]int, len(calls))
	for _, msg := range loop.Messages {
		if msg.Role == provider.RoleTool {
			seen[msg.ToolCallID]++
		}
	}
	for _, call := range calls {
		if seen[call.ID] != 1 {
			t.Fatalf("tool_call_id %q got %d result messages, want exactly 1", call.ID, seen[call.ID])
		}
	}
	if len(seen) != len(calls) {
		t.Fatalf("tool result ids=%v, want exactly %d", seen, len(calls))
	}
	if body := toolResultBody(loop.Messages, "call_queued"); !strings.Contains(body, "secret-result") {
		t.Fatalf("queued tool result=%q, want the tool's own output", body)
	}
}

// TestLoopToolInputEventsRedactCredentialsWithConfiguredPolicy asserts that
// with a policy configured, the event stream carries no argv credential even
// with the opt-in flag off: Event.Input fans out to every EventBus sink and
// log, so the flag is not what decides whether the patterns apply.
//
// It also pins the other half - the model-visible tool result is untouched, so
// the file really is written with the secret in it.
func TestLoopToolInputEventsRedactCredentialsWithConfiguredPolicy(t *testing.T) {
	installTestRedactionPolicy(t)
	tools.SetRedactToolArgs(false)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })
	const secret = "zzz-super-secret-value"
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "writing the env file",
			toolCalls: []provider.ToolCall{
				tc("call_env", "write_file", `{"path":".env.local","content":"API_KEY=`+secret+`\nPORT=8080"}`),
			},
		},
		{content: "wrote it"},
	})

	loop := h.newLoop()
	var (
		mu     sync.Mutex
		inputs []string
	)
	if _, err := loop.Run(context.Background(), "write the env file", Options{Model: "integration-model",
		MaxSteps:           5,
		MaxConcurrentTools: 2,
		ToolTimeout:        5 * time.Second,
		OnEvent: func(e Event) {
			if e.Kind == EventToolStart && e.Input != "" {
				mu.Lock()
				inputs = append(inputs, e.Input)
				mu.Unlock()
			}
		},
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(inputs) == 0 {
		t.Fatal("expected a tool_start event carrying an input preview")
	}
	for _, input := range inputs {
		if strings.Contains(input, secret) {
			t.Fatalf("tool input event leaked credential: %q", input)
		}
		if !strings.Contains(input, ".env.local") {
			t.Fatalf("tool input event over-redacted, want the path visible: %q", input)
		}
	}

	// The model-visible tool result is untouched by preview redaction: the file
	// must actually contain the secret it was asked to write.
	data, err := h.reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":".env.local"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, secret) {
		t.Fatalf("redaction altered real tool behaviour, file=%q", data)
	}
}

// TestLoopUnconfiguredWorkspaceRedactsNothingEndToEnd is the documented
// default after plan 10: a workspace that configures no policy sends tool
// previews through untouched. This fails open on purpose - what counts as a
// secret is a property of a workspace - and the cost is stated in plan 10 §5.
// If this test starts failing, a pattern list has grown back into the binary.
func TestLoopUnconfiguredWorkspaceRedactsNothingEndToEnd(t *testing.T) {
	redact.SetPolicy(nil)
	t.Cleanup(func() { redact.SetPolicy(nil) })
	tools.SetRedactToolArgs(false)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })
	const secret = "sk-ant-zzzsupersecretvalue"
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "writing the env file",
			toolCalls: []provider.ToolCall{
				tc("call_env", "write_file", `{"path":".env.local","content":"API_KEY=`+secret+`\nPORT=8080"}`),
			},
		},
		{content: "wrote it"},
	})

	loop := h.newLoop()
	var (
		mu     sync.Mutex
		inputs []string
	)
	if _, err := loop.Run(context.Background(), "write the env file", Options{Model: "integration-model",
		MaxSteps:           5,
		MaxConcurrentTools: 2,
		ToolTimeout:        5 * time.Second,
		OnEvent: func(e Event) {
			if e.Kind == EventToolStart && e.Input != "" {
				mu.Lock()
				inputs = append(inputs, e.Input)
				mu.Unlock()
			}
		},
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(inputs) == 0 {
		t.Fatal("expected a tool_start event carrying an input preview")
	}
	for _, input := range inputs {
		if !strings.Contains(input, secret) {
			t.Fatalf("unconfigured workspace redacted a credential it was never told about: %q", input)
		}
	}
}

// toolResultBody returns the tool result message body for a tool_call_id.
func toolResultBody(messages []provider.Message, id string) string {
	for _, msg := range messages {
		if msg.Role == provider.RoleTool && msg.ToolCallID == id {
			return msg.Content
		}
	}
	return ""
}
