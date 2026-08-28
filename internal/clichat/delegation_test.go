package clichat

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
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

func TestDispatchTasksCapabilityExtendsBeyondDefaultToolTimeout(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{name: "test", response: "ok"})
	cfg := config.DefaultSubagentConfig // DefaultTimeout 0 → safety ceiling
	dispatch := cliorchestrate.NewDispatchTasksToolConfigured(d, cfg, nil, nil)

	// Explicit timeout_seconds must raise the parent tool budget above the
	// effective default, and the call budget must OUTLIVE the longest task
	// rather than equal it: the agent loop arms the call's clock first, so equal
	// deadlines meant the outer one always fired first and the batch reported an
	// error instead of its results.
	args := json.RawMessage(`{"tasks":[{"id":"t1","prompt":"x","timeout_seconds":90000}],"timeout_seconds":100}`)
	cap := dispatch.Capability(args)
	if cap.Timeout <= 90000*time.Second {
		t.Fatalf("dispatch capability=%s must exceed the 90000s task budget", cap.Timeout)
	}

	// An explicit short timeout MUST actually bound the call — this is the
	// regression that caused stuck runs: EffectiveTimeoutSec was raise-only,
	// so timeout_seconds:5 was silently ignored and every dispatch_tasks call
	// got a 12h budget. Now RequestedTimeoutSec honors the explicit value, so
	// a 5s override yields a ~20s call budget (5 + slack), NOT the 12h floor.
	short := dispatch.Capability(json.RawMessage(`{"tasks":[{"id":"t1","prompt":"x"}],"timeout_seconds":5}`))
	wantShort := 5*time.Second + time.Duration(cliorchestrate.DispatchOrchestrationSlackSec)*time.Second
	if short.Timeout != wantShort {
		t.Fatalf("short dispatch capability=%s must honor the explicit 5s override (want %s); before the fix it was silently floored to the 12h default", short.Timeout, wantShort)
	}
	if short.Timeout >= time.Duration(config.DefaultOrchestrationTimeoutSec)*time.Second {
		t.Fatalf("short dispatch capability=%s must NOT be floored to the 12h default when an explicit override is present", short.Timeout)
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
	tool := cliorchestrate.NewDispatchTasksToolConfigured(d, config.SubagentConfig{DefaultTimeout: 1, InlineOutputBytes: config.DefaultSubagentConfig.InlineOutputBytes}, nil, testAgentRegistry(t, "oneshot"))
	start := time.Now()
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

func TestDispatchTasksToolValid(t *testing.T) {
	d := newTestDelegateDispatcher(&mockDelegateCompleter{
		name:     "test",
		response: "{\"output\":\"analysis result\"}",
	})
	tool := cliorchestrate.NewDispatchTasksToolConfigured(d, config.DefaultSubagentConfig, nil, testAgentRegistry(t, "oneshot"))

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
	tool := cliorchestrate.NewDispatchTasksToolConfigured(d, config.DefaultSubagentConfig, nil, testAgentRegistry(t, "oneshot"))

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
	tool := cliorchestrate.NewDispatchTasksToolConfigured(d, config.DefaultSubagentConfig, nil, testAgentRegistry(t, "oneshot"))

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
	tool := cliorchestrate.NewDispatchTasksToolConfigured(d, config.DefaultSubagentConfig, nil, testAgentRegistry(t, "oneshot"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
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

func TestDispatchTasksErrorEnvelopeOmitsUnstoredReference(t *testing.T) {
	tool := cliorchestrate.NewDispatchTasksToolConfigured(runtime.New(runtime.Policy{}), config.DefaultSubagentConfig, nil, testAgentRegistry(t, "worker"))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":[{"id":"t1","agent":"worker","prompt":"x","depends_on":["missing"]}]}`))
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

func TestNewSessionDispatcherRegistersDispatchTasksTool(t *testing.T) {
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

	if _, ok := reg.Get("dispatch_tasks"); !ok {
		t.Fatal("dispatch_tasks tool not registered in registry")
	}
	if !d.Has(runtime.Tool, "dispatch_tasks") {
		t.Fatal("dispatch_tasks is not executable in dispatcher")
	}
}

func TestSessionToolsImplementPrivilegedTool(t *testing.T) {
	for _, tool := range []tools.Tool{
		cliorchestrate.NewDispatchTasksToolForAdvertising(nil),
		cliorchestrate.NewInspectAgentsToolZero(),
		cliorchestrate.NewJoinRunToolZero(),
		cliorchestrate.NewCancelRunToolZero(),
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

	if !d.Has(runtime.Subagent, cliorchestrate.HandlerMultiStep) {
		t.Fatal("multi_step handler not registered in dispatcher")
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
