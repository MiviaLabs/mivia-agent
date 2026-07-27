package cli

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// mockDelegateCompleter implements provider.Completer for testing delegation tools.
type mockDelegateCompleter struct {
	name     string
	response string
	err      error
}

func (m *mockDelegateCompleter) Name() string { return m.name }
func (m *mockDelegateCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}
func (m *mockDelegateCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return m.Chat(ctx, req)
}
func (m *mockDelegateCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	text, err := m.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	return &provider.Response{Content: text}, nil
}

// newTestDelegateDispatcher creates a dispatcher with a OneShotHandler registered
// for testing delegation tools.
func newTestDelegateDispatcher(completer provider.Completer) *runtime.Dispatcher {
	policy := runtime.Policy{MaxDepth: 3}
	d := runtime.New(policy)
	handler := &subagents.OneShotHandler{
		Completer:    completer,
		Model:        "test-model",
		SystemPrompt: "You are a test sub-agent.",
	}
	_ = d.Register(runtime.Subagent, "delegate", handler)
	_ = d.Register(runtime.Subagent, "oneshot", handler)
	return d
}

func TestDelegateToolValid(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{
		name:     "test",
		response: "Analysis: the auth module uses JWT tokens with 1h expiry.",
	})
	tool := &delegateTool{dispatcher: d, cfg: config.DefaultSubagentConfig}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"analyze auth module"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result) == 0 {
		t.Fatal("expected non-empty result")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v\nresult: %s", err, result)
	}
	output, ok := parsed["output"].(string)
	if !ok || output == "" {
		t.Fatalf("result missing 'output' field: %s", result)
	}
	if !strings.Contains(output, "JWT") {
		t.Fatalf("output should contain expected analysis: %s", output)
	}
}

func TestDelegateToolEmptyTask(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{
		name: "test", response: "ok",
	})
	tool := &delegateTool{dispatcher: d, cfg: config.DefaultSubagentConfig}

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":""}`))
	if err == nil {
		t.Fatal("expected error for empty task")
	}
}

func TestDelegateToolMissingTask(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{
		name: "test", response: "ok",
	})
	tool := &delegateTool{dispatcher: d, cfg: config.DefaultSubagentConfig}

	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing task")
	}
}

func TestDelegateToolCanceledContext(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{
		name: "test", response: "should not be reached",
	})
	tool := &delegateTool{dispatcher: d, cfg: config.DefaultSubagentConfig}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Execute(ctx, json.RawMessage(`{"task":"test"}`))
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestDispatchTasksToolValid(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{
		name:     "test",
		response: "{\"output\":\"analysis result\"}",
	})
	tool := &dispatchTasksTool{dispatcher: d, cfg: config.DefaultSubagentConfig}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{
		"tasks": [
			{"id": "t1", "prompt": "analyze auth"},
			{"id": "t2", "prompt": "analyze db"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("result is not valid JSON array: %v\nresult: %s", err, result)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 results, got %d", len(parsed))
	}
	if parsed[0]["task_id"] != "t1" {
		t.Fatalf("first result should be task t1, got %v", parsed[0]["task_id"])
	}
	if parsed[1]["task_id"] != "t2" {
		t.Fatalf("second result should be task t2, got %v", parsed[1]["task_id"])
	}
}

func TestDispatchTasksToolEmpty(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{
		name: "test", response: "ok",
	})
	tool := &dispatchTasksTool{dispatcher: d, cfg: config.DefaultSubagentConfig}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"tasks":[]}` {
		t.Fatalf("expected empty result, got %s", result)
	}
}

func TestDispatchTasksToolWithDependencies(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{
		name:     "test",
		response: "{\"output\":\"dependency result\"}",
	})
	tool := &dispatchTasksTool{dispatcher: d, cfg: config.DefaultSubagentConfig}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{
		"tasks": [
			{"id": "research", "prompt": "find patterns"},
			{"id": "summary", "prompt": "summarize findings", "depends_on": ["research"]}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v\nresult: %s", err, result)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 results, got %d", len(parsed))
	}
	if parsed[0]["task_id"] != "research" {
		t.Fatalf("first should be research, got %v", parsed[0]["task_id"])
	}
	if parsed[1]["task_id"] != "summary" {
		t.Fatalf("second should be summary, got %v", parsed[1]["task_id"])
	}
}

func TestDispatchTasksToolCanceled(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{
		name: "test", response: "will be canceled",
	})
	tool := &dispatchTasksTool{dispatcher: d, cfg: config.DefaultSubagentConfig}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Execute(ctx, json.RawMessage(`{
		"tasks": [{"id": "t1", "prompt": "test"}]
	}`))
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestNewSessionDispatcherRegistersDelegationTools(t *testing.T) {
	ws, err := workspace.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	comp := &mockDelegateCompleter{name: "test", response: "ok"}

	_ = NewSessionDispatcher(reg, comp, "test-model", config.DefaultSubagentConfig)

	if _, ok := reg.Get("delegate"); !ok {
		t.Fatal("delegate tool not registered in registry")
	}
	if _, ok := reg.Get("dispatch_tasks"); !ok {
		t.Fatal("dispatch_tasks tool not registered in registry")
	}
}

func TestDelegateToolMultiStepFalse(t *testing.T) {
	// When multi_step is false (default), delegate uses the one-shot handler.
	d := newTestDelegateDispatcher(&mockDelegateCompleter{
		name:     "test",
		response: `{"output":"one-shot result"}`,
	})
	tool := &delegateTool{dispatcher: d, cfg: config.DefaultSubagentConfig}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"test","multi_step":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result) == 0 {
		t.Fatal("expected result")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
}

func TestDelegateToolMultiStepTrue(t *testing.T) {
	// When multi_step is true, delegate routes to the multi_step handler in the dispatcher.
	// We need a dispatcher that has both "delegate" (one-shot) and "multi_step" handlers.
	policy := runtime.Policy{MaxDepth: 3}
	d := runtime.New(policy)
	// Register one-shot handler for "delegate"
	_ = d.Register(runtime.Subagent, "delegate", &subagents.OneShotHandler{
		Completer:    &mockDelegateCompleter{name: "test", response: "one-shot"},
		Model:        "test-model",
		SystemPrompt: "Test.",
	})
	// Register multi-step handler for "multi_step"
	ws, _ := workspace.Open(".")
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	_ = d.Register(runtime.Subagent, "multi_step", &subagents.MultiStepHandler{
		Completer:    &mockDelegateCompleter{name: "test", response: `{"output":"multi-step result","status":"completed"}`},
		FullRegistry: reg,
		Model:        "test-model",
		MaxSteps:     3,
		MaxTokens:    1024,
	})

	tool := &delegateTool{dispatcher: d, cfg: config.DefaultSubagentConfig}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"test","multi_step":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\nresult: %s", err, result)
	}
}

func TestNewSessionDispatcherRegistersMultiStepHandler(t *testing.T) {
	ws, err := workspace.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	comp := &mockDelegateCompleter{name: "test", response: "ok"}

	d := NewSessionDispatcher(reg, comp, "test-model", config.DefaultSubagentConfig)

	// Verify multi_step handler is registered in the dispatcher.
	// We can verify indirectly by calling delegate with multi_step=true.
	tool := &delegateTool{dispatcher: d, cfg: config.DefaultSubagentConfig}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"test analysis","multi_step":true}`))
	if err != nil {
		t.Fatalf("multi_step delegate failed: %v", err)
	}
	if len(result) == 0 {
		t.Fatal("expected non-empty result")
	}
}
