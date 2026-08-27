package chat

// Session-loop regression tests for the SDK-backed agent loop (the
// only backend now - see internal/agent/loop_dispatch.go).
// runSDKSessionTurn mirrors sendAgent's turn construction
// (internal/chat/session.go sendAgent) field for field, so the
// session-loop turn path - beginAgentTurn, surfaceForTurnStart,
// wireStepBoundaryAdmission, commitTurnToken, finishAgentTurn - is
// exercised the same way production traffic exercises it. Coverage:
// streaming to FinalWriter with tool-call revoke, prompt-too-long
// retry, WorkLimits reservation fail-closed, and the step-boundary
// admission surface rotation.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// runSDKSessionTurn is sendAgent with the backend flipped to "sdk".
// It must stay field-for-field aligned with sendAgent's Options
// construction so the flip itself changes nothing but the pin.
func runSDKSessionTurn(t *testing.T, s *Session, userText string, w io.Writer, mutate func(*agent.Options)) (string, error) {
	t.Helper()
	ctx := context.Background()
	snapshot, done, err := s.beginAgentTurn(userText, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	toolRegistry, turnDispatcher, turnToken, turnMessages := s.surfaceForTurnStart(snapshot, nil)
	snapshot.token = turnToken
	snapshot.messages = turnMessages
	loop := &agent.Loop{
		Completer:   snapshot.binding.Completer,
		Tools:       toolRegistry,
		Messages:    snapshot.messages,
		Calibration: snapshot.Calibration,
	}
	if snapshot.toolTimeout <= 0 {
		snapshot.toolTimeout = agent.DefaultToolTimeout
	}
	opts := agent.Options{
		Model: snapshot.binding.Model, Temperature: snapshot.temperature, MaxTokens: snapshot.maxTokens,
		Reasoning: config.ModelReasoning(snapshot.binding.Profile),
		MaxSteps:  snapshot.maxSteps, MaxContextTokens: snapshot.contextBudget,
		MaxToolResultChars:     snapshot.maxToolResult,
		BatchResultBudgetBytes: snapshot.batchResultBudget,
		RefOnlyTools:           snapshot.refOnlyTools,
		RemainderSpool:         snapshot.remainderSpool,
		RequestTimeout:         DefaultRequestTimeout,
		ToolTimeout:            snapshot.toolTimeout,
		ParentID:               "session",
		TurnID:                 fmt.Sprintf("turn:%d", snapshot.myTurn), SessionID: snapshot.sessionID,
		FinalWriter: w, OnEvent: snapshot.onEvent, EventBus: snapshot.eventBus, EventIdentity: snapshot.identity,
		RequireFinalText:    true,
		AdvertisedToolSpecs: s.AdvertisedToolSpecs(),
	}
	if turnDispatcher != nil {
		opts.Dispatcher = turnDispatcher
	}
	s.wireStepBoundaryAdmission(&opts, nil)
	if mutate != nil {
		mutate(&opts)
	}
	reply, err := loop.Run(ctx, userText, opts)
	commitToken := s.commitTurnToken(uint64(snapshot.myTurn), snapshot.token)
	if persistErr := s.finishAgentTurn(ctx, loop, loop.Tools, userText, userText, commitToken, nil, snapshot.context, err); persistErr != nil && !errors.Is(persistErr, ErrStaleOperation) {
		return reply, persistErr
	}
	return reply, err
}

// revokeBuffer is a FinalWriter that records RevokeStream calls, the
// same contract internal/agent's TestLoopRevokesStreamOnToolCalls
// pins: the events bridge revokes the optimistic stream once when a
// turn's first tool call starts.
type revokeBuffer struct {
	mu      sync.Mutex
	written strings.Builder
	revoked string
	revokeN int
}

func (b *revokeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written.Write(p)
}

func (b *revokeBuffer) RevokeStream() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.revokeN++
	b.revoked = b.written.String()
	return b.revoked
}

func (b *revokeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written.String()
}

// sdkScriptCompleter drives scripted ChatTurn responses and streams
// each response's content to the stream writer, the shape a real
// streaming provider has on the SDK path.
type sdkScriptCompleter struct {
	mu    sync.Mutex
	steps []provider.Response
	calls int
}

func (c *sdkScriptCompleter) Name() string { return "sdk-script" }

func (c *sdkScriptCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}

func (c *sdkScriptCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	if w != nil && r.Content != "" {
		_, _ = io.WriteString(w, r.Content)
	}
	return r.Content, nil
}

func (c *sdkScriptCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls >= len(c.steps) {
		return nil, errors.New("sdkScriptCompleter: no scripted step left")
	}
	step := c.steps[c.calls]
	c.calls++
	// Simulate streaming content to the FinalWriter when requested,
	// the same shape internal/agent's scriptCompleter uses.
	if req.Stream && req.StreamWriter != nil && step.Content != "" {
		_, _ = io.WriteString(req.StreamWriter, step.Content)
	}
	return &step, nil
}

func (c *sdkScriptCompleter) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// countingTool records executions so a test can prove the rotated
// surface actually resolved and ran the tool.
type countingTool struct {
	name string
	runs atomic.Int32
}

func (t *countingTool) Name() string               { return t.name }
func (t *countingTool) Description() string        { return "counting test tool" }
func (t *countingTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *countingTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead}
}
func (t *countingTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.runs.Add(1)
	return t.name + " ran", nil
}

// TestSDKSessionTurnStreamsFinalAndRevokesOnToolCall pins the
// streaming contract the legacy pin protected, at the session-loop
// level: streamed preamble before a tool call is revoked exactly
// once, the tool executes, and the final answer still lands on the
// FinalWriter.
func TestSDKSessionTurnStreamsFinalAndRevokesOnToolCall(t *testing.T) {
	reg := tools.NewRegistry()
	tool := &countingTool{name: "read_a"}
	reg.Register(tool)
	s := NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, &sdkScriptCompleter{steps: []provider.Response{
		{
			Content:      "I will read the file first",
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{sdkTC("1", "read_a", `{}`)},
		},
		{Content: "found it", FinishReason: "stop"},
	}})
	s.UseTools = true
	s.Tools = reg
	s.MaxSteps = 5

	var fw revokeBuffer
	reply, err := runSDKSessionTurn(t, s, "read a", &fw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "found it" {
		t.Fatalf("reply = %q, want %q", reply, "found it")
	}
	if tool.runs.Load() != 1 {
		t.Fatalf("tool ran %d times, want 1", tool.runs.Load())
	}
	if fw.revokeN != 1 {
		t.Fatalf("RevokeStream called %d times, want 1", fw.revokeN)
	}
	if !strings.Contains(fw.revoked, "I will read") {
		t.Fatalf("revoked text = %q, want the streamed preamble", fw.revoked)
	}
	if !strings.Contains(fw.String(), "found it") {
		t.Fatalf("final stream content = %q, want the final answer", fw.String())
	}
}

// TestSDKSessionTurnRetriesAfterPromptTooLong pins the prompt retry
// contract at the session-loop level: a provider prompt-too-long
// rejection compacts once (EventPrune) and the retry's answer is the
// turn's reply.
func TestSDKSessionTurnRetriesAfterPromptTooLong(t *testing.T) {
	wrapped := fmt.Errorf("provider: %w", provider.ErrPromptTooLong)
	comp := &sdkScriptCompleter{steps: []provider.Response{
		{Content: "recovered", FinishReason: "stop"},
	}}
	// First call fails with the sentinel; drive it through a wrapper.
	reg := tools.NewRegistry()
	s := NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, &promptRetryCompleter{inner: comp, err: wrapped, failCalls: 1})
	s.UseTools = true
	s.Tools = reg
	s.MaxSteps = 3
	var events []agent.Event
	s.OnAgentEvent = func(e agent.Event) { events = append(events, e) }

	reply, err := runSDKSessionTurn(t, s, "question", io.Discard, func(o *agent.Options) {
		o.MaxContextTokens = 16 << 10
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "recovered" {
		t.Fatalf("reply = %q, want the retried answer", reply)
	}
	pruned := false
	for _, e := range events {
		if e.Kind == agent.EventPrune {
			pruned = true
		}
	}
	if !pruned {
		t.Fatalf("no EventPrune announced the compaction retry: %+v", events)
	}
}

// promptRetryCompleter fails the first failCalls ChatTurn calls with
// err, then answers from then.
type promptRetryCompleter struct {
	inner     *sdkScriptCompleter
	err       error
	failCalls int
	calls     int
}

func (c *promptRetryCompleter) Name() string { return "prompt-retry" }
func (c *promptRetryCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (c *promptRetryCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	if w != nil && r.Content != "" {
		_, _ = io.WriteString(w, r.Content)
	}
	return r.Content, nil
}
func (c *promptRetryCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if c.calls < c.failCalls {
		c.calls++
		return nil, c.err
	}
	c.calls++
	return c.inner.ChatTurn(ctx, req)
}

// TestSDKSessionTurnFailsClosedOnWorkBudget pins the WorkLimits
// reservation contract at the session-loop level: a MaxOutputTokens
// ceiling the clamped output reserve exceeds fails the turn BEFORE
// any completer call.
func TestSDKSessionTurnFailsClosedOnWorkBudget(t *testing.T) {
	maxTokens := 500
	comp := &sdkScriptCompleter{steps: []provider.Response{{Content: "answer", FinishReason: "stop"}}}
	reg := tools.NewRegistry()
	s := NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, comp)
	s.UseTools = true
	s.Tools = reg
	s.MaxSteps = 3

	_, err := runSDKSessionTurn(t, s, "a question far over one token", io.Discard, func(o *agent.Options) {
		o.MaxTokens = &maxTokens
		o.WorkLimits = runtime.WorkLimits{MaxPromptTokens: 1}
	})
	if err == nil || !strings.Contains(err.Error(), "work limit exceeded: prompt tokens") {
		t.Fatalf("err = %v, want work limit exceeded: prompt tokens", err)
	}
	if comp.callCount() != 0 {
		t.Fatalf("completer called %d times, want 0 (fail-closed before the call)", comp.callCount())
	}
}

// TestSDKSessionTurnRotatesAdmissionAtStepBoundary pins the behavior
// the legacy pin actually protected (wireStepBoundaryAdmission): a
// tool staged mid-turn publishes at the step boundary, and the very
// next model-chosen call to it resolves and executes on the SDK
// backend's rotated surface.
func TestSDKSessionTurnRotatesAdmissionAtStepBoundary(t *testing.T) {
	base := &countingTool{name: "base_tool"}
	reg := tools.NewRegistry()
	reg.Register(base)
	full := tools.NewRegistry()
	full.Register(base)
	staged := &countingTool{name: "staged_tool"}
	full.Register(staged)

	comp := &sdkScriptCompleter{steps: []provider.Response{
		{Content: "starting", FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{sdkTC("1", "base_tool", `{}`)}},
		{Content: "now the staged one", FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{sdkTC("2", "staged_tool", `{}`)}},
		{Content: "done", FinishReason: "stop"},
	}}
	s := NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, comp)
	s.UseTools = true
	s.Tools = reg
	s.MaxSteps = 5
	widener := &replayWidener{sess: s}
	s.SetSurfaceWidener(widener.fn)
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	// Stage for the turn runSDKSessionTurn is about to start, the
	// production shape load_tools has inside that turn.
	s.mu.RLock()
	next := s.turnID + 1
	s.mu.RUnlock()
	if _, err := s.StageToolAdmission([]string{"staged_tool"}, next); err != nil {
		t.Fatalf("stage: %v", err)
	}

	reply, err := runSDKSessionTurn(t, s, "use the staged tool", io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "done" {
		t.Fatalf("reply = %q, want %q", reply, "done")
	}
	if staged.runs.Load() != 1 {
		t.Fatalf("staged tool ran %d times, want 1 (the rotated surface must resolve it)", staged.runs.Load())
	}
	if base.runs.Load() != 1 {
		t.Fatalf("base tool ran %d times, want 1", base.runs.Load())
	}
	if _, ok := s.PendingAdmission(); ok {
		t.Fatal("the step-boundary publication left the stage pending")
	}
}

func sdkTC(id, name, args string) provider.ToolCall {
	var c provider.ToolCall
	c.ID = id
	c.Type = "function"
	c.Function.Name = name
	c.Function.Arguments = args
	return c
}
