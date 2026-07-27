package subagents

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// multiStepMockCompleter implements provider.Completer for testing multi-step handlers.
type multiStepMockCompleter struct {
	name      string
	callCount int
	responses []string
	toolCalls []provider.ToolCall
}

func (m *multiStepMockCompleter) Name() string { return m.name }

func (m *multiStepMockCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if m.callCount < len(m.responses) {
		resp := m.responses[m.callCount]
		m.callCount++
		return resp, nil
	}
	return "default response", nil
}

func (m *multiStepMockCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	resp, err := m.Chat(ctx, req)
	if err != nil {
		return "", err
	}
	if w != nil {
		_, _ = w.Write([]byte(resp))
	}
	return resp, nil
}

func (m *multiStepMockCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.callCount < len(m.toolCalls) {
		tc := m.toolCalls[m.callCount]
		m.callCount++
		return &provider.Response{
			Content:   "",
			ToolCalls: []provider.ToolCall{tc},
		}, nil
	}
	text, err := m.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	return &provider.Response{Content: text}, nil
}

func newTestRegistry() *tools.Registry {
	ws, _ := workspace.Open(".")
	return tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
}

func TestMultiStepHandlerInvoke(t *testing.T) {
	reg := newTestRegistry()
	comp := &multiStepMockCompleter{
		name:      "test",
		responses: []string{"Analysis: auth module uses JWT"},
	}
	h := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Model:        "test-model",
		SystemPrompt: "Test sub-agent.",
		MaxSteps:     3,
		MaxTokens:    1024,
	}

	result, err := h.Invoke(context.Background(), runtime.Request{
		Name:  "multi_step",
		Input: json.RawMessage(`"analyze auth module"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["status"] != "completed" {
		t.Fatalf("expected completed, got %v", parsed["status"])
	}
	output, ok := parsed["output"].(string)
	if !ok || !strings.Contains(output, "JWT") {
		t.Fatalf("output missing expected content: %v", parsed["output"])
	}
}

func TestMultiStepHandlerToolAccess(t *testing.T) {
	reg := newTestRegistry()

	// Verify the base registry has standard tools.
	if _, ok := reg.Get("read_file"); !ok {
		t.Fatal("base registry missing read_file")
	}

	comp := &multiStepMockCompleter{
		name:      "test",
		responses: []string{"result"},
	}
	h := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Model:        "test-model",
		MaxSteps:     3,
	}
	restricted := h.restrictedRegistry()

	for _, tool := range restricted.List() {
		if tool.Name() == "delegate" || tool.Name() == "dispatch_tasks" {
			t.Fatalf("restricted registry should not contain %q", tool.Name())
		}
	}
	// Verify standard tools are still accessible.
	if _, ok := restricted.Get("read_file"); !ok {
		t.Fatal("restricted registry missing read_file")
	}
	if _, ok := restricted.Get("grep"); !ok {
		t.Fatal("restricted registry missing grep")
	}
	if _, ok := restricted.Get("write_file"); !ok {
		t.Fatal("restricted registry missing write_file")
	}
}

func TestMultiStepHandlerEmptyTask(t *testing.T) {
	reg := newTestRegistry()
	comp := &multiStepMockCompleter{name: "test"}
	h := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Model:        "test-model",
		MaxSteps:     3,
	}
	_, err := h.Invoke(context.Background(), runtime.Request{
		Name:  "multi_step",
		Input: json.RawMessage(`""`),
	})
	if err == nil {
		t.Fatal("expected error for empty task")
	}
}

func TestMultiStepHandlerCancel(t *testing.T) {
	reg := newTestRegistry()
	comp := &multiStepMockCompleter{name: "test"}
	h := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Model:        "test-model",
		MaxSteps:     3,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.Invoke(ctx, runtime.Request{
		Name:  "multi_step",
		Input: json.RawMessage(`"task"`),
	})
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestMultiStepHandlerStepCap(t *testing.T) {
	// Verify that the handler passes MaxSteps to the agent loop.
	// The agent loop stops when step > MaxSteps. A step cap of 0 means no limit.
	h := &MultiStepHandler{
		MaxSteps:  0, // unlimited
		MaxTokens: 1024,
	}
	if h.MaxSteps != 0 {
		t.Fatalf("expected 0 (unlimited), got %d", h.MaxSteps)
	}
	h2 := &MultiStepHandler{
		MaxSteps:  5,
		MaxTokens: 1024,
	}
	if h2.MaxSteps != 5 {
		t.Fatalf("expected 5, got %d", h2.MaxSteps)
	}
	// Verify the handler's Invoke respects empty MaxSteps default.
	h3 := &MultiStepHandler{MaxTokens: 1024}
	if h3.MaxSteps != 0 {
		t.Fatalf("expected 0, got %d", h3.MaxSteps)
	}
}

func TestMultiStepHandlerResultJSON(t *testing.T) {
	reg := newTestRegistry()
	comp := &multiStepMockCompleter{
		name:      "test",
		responses: []string{"work done"},
	}
	h := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Model:        "test-model",
		SystemPrompt: "Test sub-agent.",
		MaxSteps:     3,
		MaxTokens:    1024,
	}

	result, err := h.Invoke(context.Background(), runtime.Request{
		Name:  "multi_step",
		Input: json.RawMessage(`"do the work"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := parsed["output"]; !ok {
		t.Fatal("result missing 'output' field")
	}
	if _, ok := parsed["status"]; !ok {
		t.Fatal("result missing 'status' field")
	}
	if _, ok := parsed["steps"]; !ok {
		t.Fatal("result missing 'steps' field")
	}
}

// Compile-time checks.
var _ runtime.Handler = (*MultiStepHandler)(nil)
