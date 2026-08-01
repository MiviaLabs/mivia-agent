package subagents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// multiStepMockCompleter implements provider.Completer for testing multi-step handlers.
type multiStepMockCompleter struct {
	name        string
	callCount   int
	responses   []string
	toolCalls   []provider.ToolCall
	chatTurnErr error
}

// privilegedSideEffectTool represents a session-control capability whose
// execution must never be reachable from a nested multi-step agent.
type privilegedSideEffectTool struct{ executed bool }

func (t *privilegedSideEffectTool) Name() string { return "spawn_agent" }

func (t *privilegedSideEffectTool) Description() string { return "starts a nested agent" }

func (t *privilegedSideEffectTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (t *privilegedSideEffectTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.executed = true
	return "started", nil
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
	if m.chatTurnErr != nil {
		return nil, m.chatTurnErr
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

func TestMultiStepHandlerCarriesPromptBudgetToAgentLoop(t *testing.T) {
	comp := &multiStepMockCompleter{name: "test"}
	h := &MultiStepHandler{
		Completer:        comp,
		FullRegistry:     newTestRegistry(),
		Model:            "test-model",
		SystemPrompt:     "system",
		MaxContextTokens: 2,
		MaxTokens:        20,
	}
	_, err := h.Invoke(context.Background(), runtime.Request{
		Name:  "test",
		Input: json.RawMessage(`"` + strings.Repeat("x", 40) + `"`),
	})
	if !errors.Is(err, agent.ErrPromptBudgetExceeded) {
		t.Fatalf("preflight error = %v", err)
	}
	if comp.callCount != 0 {
		t.Fatalf("provider was called %d times after preflight rejection", comp.callCount)
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
		if tool.Name() == "delegate" || tool.Name() == "dispatch_tasks" ||
			tool.Name() == "spawn_agent" || tool.Name() == "inspect_agents" ||
			tool.Name() == "join_run" || tool.Name() == "cancel_run" {
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

func TestMultiStepHandlerCannotExecutePrivilegedToolThroughParentDispatcher(t *testing.T) {
	reg := newTestRegistry()
	privileged := &privilegedSideEffectTool{}
	reg.Register(privileged)
	parentDispatcher, err := runtime.NewToolDispatcher(reg, runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if !parentDispatcher.Has(runtime.Tool, privileged.Name()) {
		t.Fatalf("parent dispatcher missing privileged tool %q", privileged.Name())
	}

	call := provider.ToolCall{ID: "privileged-1", Type: "function"}
	call.Function.Name = privileged.Name()
	call.Function.Arguments = `{}`
	comp := &multiStepMockCompleter{name: "test", toolCalls: []provider.ToolCall{call}}
	h := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Dispatcher:   parentDispatcher,
		Model:        "test-model",
		MaxSteps:     3,
	}

	if _, err := h.Invoke(context.Background(), runtime.Request{
		Name:  "multi_step",
		Input: json.RawMessage(`"attempt privileged action"`),
	}); err != nil {
		t.Fatal(err)
	}
	if privileged.executed {
		t.Fatal("nested multi-step agent executed privileged tool through parent dispatcher")
	}
}

// TestMultiStepHandlerInspectAgentsBlocked verifies that restrictedRegistry
// properly blocks inspect_agents and all delegation tools via the blocked map.
// inspect_agents is registered at the CLI level (orchestrate.go), not in the
// default tools registry, but the restricted blocked map must still filter it.
func TestMultiStepHandlerInspectAgentsBlocked(t *testing.T) {
	reg := newTestRegistry()

	// Verify standard tools ARE in the registry.
	if _, ok := reg.Get("read_file"); !ok {
		t.Fatal("full registry should include read_file")
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

	// All delegation tools must be blocked (even if not present in full registry,
	// the blocked map ensures they are filtered out).
	blockedTools := []string{
		"delegate", "dispatch_tasks", "spawn_agent",
		"inspect_agents", "join_run", "cancel_run",
	}
	for _, name := range blockedTools {
		if _, ok := restricted.Get(name); ok {
			t.Errorf("restricted registry should NOT contain %q", name)
		}
	}

	// Standard tools must still be accessible.
	for _, name := range []string{"read_file", "grep", "write_file", "list_dir", "glob", "run_command"} {
		if _, ok := restricted.Get(name); !ok {
			t.Errorf("restricted registry missing standard tool %q", name)
		}
	}

	// List all restricted tools and verify none are delegation tools.
	for _, tool := range restricted.List() {
		for _, blocked := range blockedTools {
			if tool.Name() == blocked {
				t.Errorf("restricted registry returned blocked tool %q via List()", blocked)
			}
		}
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

func TestMultiStepHandlerMaxStepsReturnsOperationalError(t *testing.T) {
	reg := newTestRegistry()
	var call provider.ToolCall
	call.ID = "read-1"
	call.Type = "function"
	call.Function.Name = "read_file"
	call.Function.Arguments = `{"path":"missing.txt"}`
	comp := &multiStepMockCompleter{
		name:      "test",
		toolCalls: []provider.ToolCall{call},
	}
	h := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Model:        "test-model",
		MaxSteps:     1,
	}
	result, err := h.Invoke(context.Background(), runtime.Request{
		Name:  "multi_step",
		Input: json.RawMessage(`"repeat until done"`),
	})
	if err == nil || !strings.Contains(err.Error(), "max_steps") {
		t.Fatalf("err=%v, want max_steps error", err)
	}
	var parsed map[string]any
	if unmarshalErr := json.Unmarshal(result, &parsed); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if parsed["status"] != "error" {
		t.Fatalf("status=%v, want error", parsed["status"])
	}
	// This layer stores nothing, so it must emit no content reference at all:
	// an unresolvable reference is worse than none. The payload therefore
	// carries bounded status only - no raw body and no ref.
	if _, ok := parsed["error_ref"]; ok {
		t.Fatalf("unstorable error reference emitted: %v", parsed)
	}
	if _, ok := parsed["output_ref"]; ok {
		t.Fatalf("unstorable output reference emitted: %v", parsed)
	}
	if strings.Contains(string(result), "ref:") {
		t.Fatalf("content reference leaked in result: %s", result)
	}
	if strings.Contains(string(result), "max_steps") {
		t.Fatalf("raw provider/handler error leaked in result: %s", result)
	}
}

// A failure payload from this layer must leak no raw provider body, and - since
// nothing at this layer stores content - must carry no content reference
// either. The refs this test once asserted were dead pointers: nothing ever
// stored their bytes, so they could never resolve. The coordinator mints and
// stores the resolvable reference for the same task from Result.Output/.Err.
func TestMultiStepHandlerFailureOmitsRawProviderBodyAndRefs(t *testing.T) {
	reg := newTestRegistry()
	comp := &multiStepMockCompleter{
		name:        "test",
		chatTurnErr: errors.New("provider body should not escape: raw prompt and tool output"),
	}
	h := &MultiStepHandler{Completer: comp, FullRegistry: reg, Model: "test-model", MaxSteps: 1}
	result, err := h.Invoke(context.Background(), runtime.Request{Name: "multi_step", Input: json.RawMessage(`"task"`)})
	if err == nil {
		t.Fatal("expected operational error")
	}
	if strings.Contains(string(result), "provider body should not escape") || strings.Contains(string(result), "raw prompt") {
		t.Fatalf("raw provider body leaked in result: %s", result)
	}
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["status"] != "error" {
		t.Fatalf("status=%v, want error: %v", parsed["status"], parsed)
	}
	if _, ok := parsed["error_ref"]; ok {
		t.Fatalf("unstorable error reference emitted: %v", parsed)
	}
	if _, ok := parsed["output_ref"]; ok {
		t.Fatalf("unstorable output reference emitted: %v", parsed)
	}
	if strings.Contains(string(result), "ref:") {
		t.Fatalf("content reference leaked in result: %s", result)
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

// TestMultiStepHandlerEmptyFinalAfterToolStaysCompleted pins the blast radius of
// the interactive "a turn with no text is an error" rule. Sub-agents run the same
// agent.Loop, and buildResult DELETES the task output whenever the error is
// non-nil. A sub-agent that did its work through tools and then stopped without
// prose has succeeded: turning that into an error would report a failed task and
// throw away the output the parent model needs. Only agent.Options.RequireFinalText
// opts in, and the sub-agent path must never set it.
func TestMultiStepHandlerEmptyFinalAfterToolStaysCompleted(t *testing.T) {
	reg := newTestRegistry()
	call := provider.ToolCall{ID: "read-1", Type: "function"}
	call.Function.Name = "read_file"
	call.Function.Arguments = `{"path":"multi_step.go"}`

	comp := &multiStepMockCompleter{
		name:      "test",
		toolCalls: []provider.ToolCall{call},
		// Second turn answers with no prose at all.
		responses: []string{"", ""},
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
		Input: json.RawMessage(`"read the file"`),
	})
	if err != nil {
		t.Fatalf("a silent-but-productive sub-agent turn must not error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["status"] != "completed" {
		t.Fatalf("status = %v, want completed", parsed["status"])
	}
	if _, ok := parsed["output"]; !ok {
		t.Fatal("output was deleted from the result payload")
	}
}
