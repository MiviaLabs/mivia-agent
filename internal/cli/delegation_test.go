package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// mockDelegateCompleter implements provider.Completer for testing delegation tools.
type mockDelegateCompleter struct {
	name     string
	response string
	err      error
	calls    atomic.Int32
}

type loopDelegationCompleter struct {
	calls int
}

func (c *loopDelegationCompleter) Name() string { return "loop-delegation-test" }
func (c *loopDelegationCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "subagent result", nil
}
func (c *loopDelegationCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *loopDelegationCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.calls++
	if c.calls == 1 {
		var call provider.ToolCall
		call.ID = "delegate-call-1"
		call.Type = "function"
		call.Function.Name = "delegate"
		call.Function.Arguments = `{"task":"analyze"}`
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	}
	return &provider.Response{Content: "delegation complete", FinishReason: "stop"}, nil
}

func (m *mockDelegateCompleter) Name() string { return m.name }
func (m *mockDelegateCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	m.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

func TestDelegateToolRepeatedCallsUseIndependentInvocationKeys(t *testing.T) {
	comp := &mockDelegateCompleter{name: "test", response: "independent"}
	d := newTestDelegateDispatcher(comp)
	tool := &delegateTool{dispatcher: d, cfg: config.DefaultSubagentConfig}
	for _, task := range []string{"first", "second"} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"task":%q}`, task))); err != nil {
			t.Fatal(err)
		}
	}
	if got := comp.calls.Load(); got != 2 {
		t.Fatalf("subagent calls=%d, want 2", got)
	}
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
	output, ok := parsed["output_ref"].(string)
	if !ok || output == "" {
		t.Fatalf("result missing 'output_ref' field: %s", result)
	}
	if !strings.HasPrefix(output, "ref:output:") {
		t.Fatalf("output reference has unexpected format: %s", output)
	}
	structured, ok := parsed["output"].(map[string]any)
	if !ok {
		t.Fatalf("result missing structured output: %s", result)
	}
	if structured["output"] != "Analysis: the auth module uses JWT tokens with 1h expiry." {
		t.Fatalf("structured output=%v, want subagent reply", structured)
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
	// Model-visible body with cancel status; nil transport error so the agent
	// loop does not wipe the payload to a bare "error: …" string.
	body, err := tool.Execute(ctx, json.RawMessage(`{"task":"test"}`))
	if err != nil {
		t.Fatalf("transport err should be nil, got %v", err)
	}
	if !strings.Contains(body, "cancel") && !strings.Contains(body, "error") {
		t.Fatalf("expected cancel/error in body, got %q", body)
	}
}

func TestDelegateAndDispatchCapabilityExtendsBeyondDefaultToolTimeout(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{name: "test", response: "ok"})
	cfg := config.DefaultSubagentConfig // DefaultTimeout 0 → safety ceiling
	delegate := &delegateTool{dispatcher: d, cfg: cfg}
	dispatch := &dispatchTasksTool{dispatcher: d, cfg: cfg}

	dCap := delegate.Capability(json.RawMessage(`{"task":"x"}`))
	if dCap.Timeout < time.Hour {
		t.Fatalf("delegate capability timeout %s too short for multi-step work", dCap.Timeout)
	}
	if dCap.Timeout != time.Duration(config.DefaultOrchestrationTimeoutSec)*time.Second {
		t.Fatalf("delegate capability=%s want %ds ceiling", dCap.Timeout, config.DefaultOrchestrationTimeoutSec)
	}

	// Explicit timeout_seconds must raise the parent tool budget, and the call
	// budget must OUTLIVE the longest task rather than equal it: the agent loop
	// arms the call's clock first, so equal deadlines meant the outer one always
	// fired first and the batch reported an error instead of its results.
	args := json.RawMessage(`{"tasks":[{"id":"t1","prompt":"x","timeout_seconds":9000}],"timeout_seconds":100}`)
	cap := dispatch.Capability(args)
	if cap.Timeout <= 9000*time.Second {
		t.Fatalf("dispatch capability=%s must exceed the 9000s task budget", cap.Timeout)
	}

	// Short override must be honored: it tracks the request rather than snapping to
	// the ceiling. It still carries the headroom above, so assert the band, not an
	// exact equality that the headroom would break.
	short := dispatch.Capability(json.RawMessage(`{"tasks":[{"id":"t1","prompt":"x"}],"timeout_seconds":5}`))
	if short.Timeout <= 5*time.Second {
		t.Fatalf("short dispatch capability=%s must exceed its 5s task budget", short.Timeout)
	}
	if short.Timeout >= time.Duration(config.DefaultOrchestrationTimeoutSec)*time.Second {
		t.Fatalf("short dispatch capability=%s is stuck at the ceiling", short.Timeout)
	}
}

func TestDispatchTasksTimeoutReturnsStructuredStatus(t *testing.T) {
	// Handler blocks until ctx done - pool task timeout must surface timed_out.
	d := runtime.New(runtime.Policy{MaxDepth: 3})
	_ = d.Register(runtime.Subagent, "oneshot", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-time.After(5 * time.Second):
			return json.RawMessage(`{"output":"late"}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	tool := &dispatchTasksTool{dispatcher: d, cfg: config.DefaultSubagentConfig, agentReg: testAgentRegistry(t, "oneshot")}
	start := time.Now()
	// timeout_seconds is integer seconds; use 1s budget vs 5s work.
	body, err := tool.Execute(context.Background(), json.RawMessage(`{
		"timeout_seconds": 1,
		"tasks": [{"id":"t1","agent":"oneshot","prompt":"block","timeout_seconds":1}]
	}`))
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Fatalf("dispatch hang: %s", elapsed)
	}
	if err != nil {
		t.Fatalf("transport err should be nil, got %v", err)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("body not array: %v body=%s", err, body)
	}
	if len(parsed) != 1 || parsed[0]["status"] != "timed_out" {
		t.Fatalf("parsed=%v want status timed_out body=%s", parsed, body)
	}
	if _, ok := parsed[0]["error"].(string); !ok {
		t.Fatalf("parsed=%v want model-visible task error body=%s", parsed, body)
	}
}

// handlerFunc adapts a function to runtime.Handler for tests in this package.
type handlerFunc func(context.Context, runtime.Request) (json.RawMessage, error)

func (f handlerFunc) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	return f(ctx, req)
}

func TestDispatchTasksToolValid(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{
		name:     "test",
		response: "{\"output\":\"analysis result\"}",
	})
	tool := &dispatchTasksTool{dispatcher: d, cfg: config.DefaultSubagentConfig, agentReg: testAgentRegistry(t, "oneshot")}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{
		"tasks": [
			{"id": "t1", "agent":"oneshot", "prompt": "analyze auth"},
			{"id": "t2", "agent":"oneshot", "prompt": "analyze db"}
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
	for _, task := range parsed {
		structured, ok := task["output"].(map[string]any)
		if !ok || structured["output"] != `{"output":"analysis result"}` {
			t.Fatalf("task result missing structured output: %v", task)
		}
	}
}

func TestDispatchTasksToolEmpty(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{
		name: "test", response: "ok",
	})
	tool := &dispatchTasksTool{dispatcher: d, cfg: config.DefaultSubagentConfig, agentReg: testAgentRegistry(t, "oneshot")}

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
	tool := &dispatchTasksTool{dispatcher: d, cfg: config.DefaultSubagentConfig, agentReg: testAgentRegistry(t, "oneshot")}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{
		"tasks": [
			{"id": "research", "agent":"oneshot", "prompt": "find patterns"},
			{"id": "summary", "agent":"oneshot", "prompt": "summarize findings", "depends_on": ["research"]}
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
	tool := &dispatchTasksTool{dispatcher: d, cfg: config.DefaultSubagentConfig, agentReg: testAgentRegistry(t, "oneshot")}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Structured body + nil transport err (agent loop keeps the payload).
	body, err := tool.Execute(ctx, json.RawMessage(`{
		"tasks": [{"id": "t1", "agent":"oneshot", "prompt": "test"}]
	}`))
	if err != nil {
		t.Fatalf("transport err should be nil, got %v", err)
	}
	if !strings.Contains(body, "cancel") && !strings.Contains(body, "error") {
		t.Fatalf("expected cancel/error status in body, got %q", body)
	}
}

// TestDispatchTasksErrorEnvelopeOmitsUnstoredReference guards INV-AG-10 on the
// run-level failure path: a validation error is never a task's recorded error,
// so no content is ever stored under its digest. The envelope must therefore
// carry the error text inline and no error_ref at all, rather than a reference
// that resolves to nothing.
//
// Regression: INV-AG-10
func TestDispatchTasksErrorEnvelopeOmitsUnstoredReference(t *testing.T) {
	tool := &dispatchTasksTool{dispatcher: runtime.New(runtime.Policy{}), cfg: config.DefaultSubagentConfig, agentReg: testAgentRegistry(t, "worker")}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":[{"id":"t1","agent":"worker","prompt":"x","depends_on":["missing"]}]}`))
	// Missing dependency: empty results + model-visible JSON envelope, nil transport err.
	if err != nil {
		t.Fatalf("transport err should be nil, got %v", err)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("invalid error envelope: %q", out)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("error envelope is not an object: %v", err)
	}
	if parsed["status"] != "failed" {
		t.Fatalf("status=%q, want failed: %q", parsed["status"], out)
	}
	if _, ok := parsed["error_ref"]; ok {
		t.Fatalf("run-level envelope carried a dead error_ref: %q", out)
	}
	if !strings.Contains(parsed["error"], `depends on unknown task "missing"`) {
		t.Fatalf("error=%q, want full coordinator error: %q", parsed["error"], out)
	}
}

// contentStoreFailingRepo is a ledger repository whose content writes always
// fail. Everything else behaves like the in-memory repository.
type contentStoreFailingRepo struct {
	*ledger.MemoryLedgerRepository
}

func (contentStoreFailingRepo) StoreContent(_ context.Context, _ string, _ []byte) error {
	return errors.New("content store unavailable")
}

// TestDelegateReturnsOutputWhenContentStoreFails pins the rule that a
// persistence failure must not destroy a result the sub-agent actually
// produced. The task completes; only the content write fails, which the
// coordinator joins into the run error. delegate must still report the output
// and a non-failed status, and must omit the output_ref that was never stored.
func TestDelegateReturnsOutputWhenContentStoreFails(t *testing.T) {
	repo := contentStoreFailingRepo{MemoryLedgerRepository: ledger.NewMemoryLedgerRepository()}
	d := newTestDelegateDispatcher(&mockDelegateCompleter{name: "test", response: "sub-agent findings"})
	tool := &delegateTool{dispatcher: d, cfg: config.DefaultSubagentConfig, repo: repo}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"analyze"}`))
	if err != nil {
		t.Fatalf("transport err should be nil, got %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v\nresult: %s", err, out)
	}
	if parsed["status"] == "failed" {
		t.Fatalf("successful task reported as failed because content persistence failed: %s", out)
	}
	if parsed["output"] == nil {
		t.Fatalf("sub-agent output discarded on content store failure: %s", out)
	}
	if !strings.Contains(out, "sub-agent findings") {
		t.Fatalf("sub-agent output text missing: %s", out)
	}
	if ref, ok := parsed["output_ref"]; ok {
		t.Fatalf("output_ref=%v handed to the model despite the failed content write: %s", ref, out)
	}
}

func TestDelegateToolReturnsSubagentErrorToCaller(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{
		name: "test",
		err:  errors.New("subagent tool failed: unique_tool is unavailable"),
	})
	tool := &delegateTool{dispatcher: d, cfg: config.DefaultSubagentConfig}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"run the subtask"}`))
	if err != nil {
		t.Fatalf("transport err should be nil, got %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	errorText, ok := parsed["error"].(string)
	if !ok || !strings.Contains(errorText, "subagent tool failed: unique_tool is unavailable") {
		t.Fatalf("error=%q, want full subagent error: %q", parsed["error"], out)
	}
	errorRef, ok := parsed["error_ref"].(string)
	if !ok || !strings.HasPrefix(errorRef, "ref:error:") {
		t.Fatalf("error_ref=%q, want reference", parsed["error_ref"])
	}
	if _, ok := parsed["output"].(map[string]any); !ok {
		t.Fatalf("output=%v, want structured failure payload", parsed["output"])
	}
}

func TestNewSessionDispatcherRegistersDelegationTools(t *testing.T) {
	ws, err := workspace.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	comp := &mockDelegateCompleter{name: "test", response: "ok"}

	d, err := newSessionDispatcherMinimal(reg, comp, "test-model", config.DefaultSubagentConfig, 0)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := reg.Get("delegate"); !ok {
		t.Fatal("delegate tool not registered in registry")
	}
	if _, ok := reg.Get("dispatch_tasks"); !ok {
		t.Fatal("dispatch_tasks tool not registered in registry")
	}
	if !d.Has(runtime.Tool, "delegate") || !d.Has(runtime.Tool, "dispatch_tasks") {
		t.Fatal("delegation tools are not executable in dispatcher")
	}
	result := d.Invoke(context.Background(), runtime.Request{
		ID:    "tool-delegate-1",
		Kind:  runtime.Tool,
		Name:  "delegate",
		Input: json.RawMessage(`{"task":"test"}`),
	})
	if result.Err != nil {
		t.Fatalf("dispatcher did not invoke registered delegate tool: %v", result.Err)
	}
}

func TestSessionToolsImplementPrivilegedTool(t *testing.T) {
	for _, tool := range []tools.Tool{
		&delegateTool{},
		&dispatchTasksTool{},
		&spawnAgentTool{skillReg: nil},
		&inspectAgentTool{},
		&joinRunTool{},
		&cancelRunTool{},
	} {
		if _, ok := tool.(tools.PrivilegedTool); !ok {
			t.Errorf("%q does not implement PrivilegedTool", tool.Name())
		}
	}
}

func TestSessionDispatcherDelegationThroughAgentLoop(t *testing.T) {
	ws, err := workspace.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	comp := &loopDelegationCompleter{}
	d, err := newSessionDispatcherMinimal(reg, comp, "test-model", config.DefaultSubagentConfig, 0)
	if err != nil {
		t.Fatal(err)
	}
	loop := &agent.Loop{Completer: comp, Tools: reg}
	reply, err := loop.Run(context.Background(), "delegate", agent.Options{
		Model:          "test-model",
		MaxSteps:       3,
		Dispatcher:     d,
		RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "delegation complete" || comp.calls != 2 {
		t.Fatalf("reply=%q calls=%d", reply, comp.calls)
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

	d, err := newSessionDispatcherMinimal(reg, comp, "test-model", config.DefaultSubagentConfig, 0)
	if err != nil {
		t.Fatal(err)
	}

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

func TestSessionDispatcherRoutesPermissionedSkillThroughDispatchTasks(t *testing.T) {
	ws, err := workspace.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	skillReg := skills.NewRegistry()
	if err := skillReg.Register(skills.Definition{
		Name: "review", Permission: "read",
	}); err != nil {
		t.Fatal(err)
	}
	d, err := NewSessionDispatcher(SessionDispatcherOpts{Registry: reg, Completer: &mockDelegateCompleter{name: "test", response: "ok"}, Model: "test-model", Config: config.DefaultSubagentConfig, SkillReg: skillReg, AgentRegistry: testAgentRegistry(t, "worker")})
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Get("dispatch_tasks")
	if !ok {
		t.Fatal("dispatch_tasks is not registered")
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":[{"id":"r1","agent":"worker","skill":"review","prompt":"check"}]}`))
	if err != nil {
		t.Fatalf("permissioned skill dispatch failed: %v (%s)", err, out)
	}
	if !strings.Contains(out, "output_ref") {
		t.Fatalf("unexpected result: %s", out)
	}
	if !d.Has(runtime.Subagent, "review") {
		t.Fatal("skill was not registered as a subagent")
	}
}

func TestMarkdownSkillReachesProductionDispatcherPath(t *testing.T) {
	ws, err := workspace.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "review", "SKILL.md"), []byte("---\nname: review\n---\nReview with evidence."), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	comp := &mockDelegateCompleter{name: "test", response: "reviewed"}
	skillReg, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewSessionDispatcher(SessionDispatcherOpts{Registry: reg, Completer: comp, Model: "test-model", Config: config.DefaultSubagentConfig, SkillReg: skillReg, AgentRegistry: testAgentRegistry(t, "worker")})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Has(runtime.Subagent, "review") {
		t.Fatal("loaded skill was not registered as a subagent")
	}
	dispatcherTool, ok := reg.Get("dispatch_tasks")
	if !ok {
		t.Fatal("dispatch_tasks is not registered")
	}
	out, err := dispatcherTool.Execute(context.Background(), json.RawMessage(`{"tasks":[{"id":"r1","agent":"worker","skill":"review","prompt":"inspect"}]}`))
	if err != nil || !strings.Contains(out, "output_ref") {
		t.Fatalf("out=%s err=%v", out, err)
	}
}
