package agent

// Tests for the B.2 #8 part 2 commit 3 surface: the tool-registry
// converter, the full options mapping, the request translator, and
// the RunAgentLoopOnce helper.

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// TestBuildAgentLoopOptionsPassesValidate asserts the full mapping
// produces Options that satisfy the SDK's Validate: completer
// wrapped, registry converted, MaxIterations positive.
func TestBuildAgentLoopOptionsPassesValidate(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	got, err := buildAgentLoopOptions(l, Options{MaxSteps: 5})
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("converted Options failed Validate: %v", err)
	}
}

// TestBuildAgentLoopOptionsFailClosed runs one subtest per CLI
// Options field the SDK path cannot carry. Each subtest asserts the
// error names the field so an opt-in caller learns the boundary.
func TestBuildAgentLoopOptionsFailClosed(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{"MaxConcurrentTools", Options{MaxConcurrentTools: 2}, "MaxConcurrentTools"},
		{"Surface", Options{Surface: func() Surface { return Surface{} }}, "Surface"},
		{"BeforeStep", Options{BeforeStep: func() []provider.Message { return nil }}, "BeforeStep"},
		{"StagedToolMessage", Options{StagedToolMessage: func(string) (string, bool) { return "", false }}, "StagedToolMessage"},
		{"RefOnlyTools", Options{RefOnlyTools: []string{"x"}}, "RefOnlyTools"},
		{"NegativeBatchBudget", Options{BatchResultBudgetBytes: -1}, "BatchResultBudgetBytes"},
		{"PreserveWorkLimits", Options{PreserveWorkLimits: true}, "PreserveWorkLimits"},
		{"RequireFinalText", Options{RequireFinalText: true}, "RequireFinalText"},
		{"MaxContextTokens", Options{MaxContextTokens: 1000}, "MaxContextTokens"},
		{"MailboxPendingInterrupt", Options{MailboxPendingInterrupt: func() bool { return false }}, "MailboxPendingInterrupt"},
		{"FinalWriter", Options{FinalWriter: io.Discard}, "FinalWriter"},
	}
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildAgentLoopOptions(l, tt.opts)
			if err == nil {
				t.Fatalf("Options with %s set passed; want fail-closed error", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to name %q", err, tt.want)
			}
		})
	}
}

// TestTranslateRequestMapsFields asserts the SDK-to-CLI request
// translator: pass-through scalars, message conversion with tool
// calls, and the effort-to-level inverse.
func TestTranslateRequestMapsFields(t *testing.T) {
	req := sdkshape.Request{
		Model:     "m1",
		SessionID: "s1",
		Messages: []sdkshape.Message{
			{
				Role:    sdkshape.RoleAssistant,
				Content: "calling",
				ToolCalls: []sdkshape.ToolCall{
					{ID: "c1", Name: "read_file", Arguments: []byte(`{"path":"/x"}`)},
				},
			},
			{Role: sdkshape.RoleTool, Content: "result", ToolCallID: "c1"},
		},
		ReasoningEffort: sdkshape.ReasoningEffortHigh,
	}
	got := translateAgentLoopRequest(req)
	if got.Model != "m1" || got.SessionID != "s1" {
		t.Fatalf("Model/SessionID = %q/%q, want m1/s1", got.Model, got.SessionID)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("Messages len = %d, want 2", len(got.Messages))
	}
	first := got.Messages[0]
	if len(first.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(first.ToolCalls))
	}
	tc := first.ToolCalls[0]
	if tc.ID != "c1" || tc.Type != "function" || tc.Function.Name != "read_file" {
		t.Fatalf("ToolCall = %+v, want id c1 function read_file", tc)
	}
	if tc.Function.Arguments != `{"path":"/x"}` {
		t.Fatalf("Arguments = %q, want the raw JSON string", tc.Function.Arguments)
	}
	if got.ReasoningLevel != "high" {
		t.Fatalf("ReasoningLevel = %q, want high", got.ReasoningLevel)
	}
}

// TestRunAgentLoopOnceCompletesOneTurn is the end-to-end smoke: a
// fake completer whose ChatTurn returns plain content, an empty
// registry, MaxSteps 1. RunAgentLoopOnce must return the content as
// the final message and stop with the SDK's no-tool-calls reason.
func TestRunAgentLoopOnceCompletesOneTurn(t *testing.T) {
	l := &Loop{
		Completer: &fakeCompleter{name: "fake", chatTurnOut: &provider.Response{Content: "done", FinishReason: "stop"}},
		Tools:     tools.NewRegistry(),
	}
	res, err := RunAgentLoopOnce(context.Background(), l, Options{Model: "m", MaxSteps: 1}, nil)
	if err != nil {
		t.Fatalf("RunAgentLoopOnce: %v", err)
	}
	if res.Final.Content != "done" {
		t.Fatalf("Final.Content = %q, want %q", res.Final.Content, "done")
	}
	if res.Stop != sdkagentloop.StopNoToolCalls {
		t.Fatalf("Stop = %q, want %q", res.Stop, sdkagentloop.StopNoToolCalls)
	}
}

// TestRunAgentLoopOnceSteerTriggers asserts the steer bridge: a
// blocking completer plus a fired InterruptCh must let
// RunAgentLoopOnce return rather than hang. The test wraps the call
// in a 2-second guard; the assertion is no-hang, not a specific stop
// reason, because the steer may land before or during the first
// completion depending on scheduling.
func TestRunAgentLoopOnceSteerTriggers(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	fired := make(chan struct{})
	close(fired)
	l := &Loop{
		Completer: &fakeCompleter{name: "fake", blocksChat: release, chatTurnOut: &provider.Response{Content: "x"}},
		Tools:     tools.NewRegistry(),
	}
	opts := Options{
		MaxSteps:    1,
		InterruptCh: func() <-chan struct{} { return fired },
	}
	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		_, err = RunAgentLoopOnce(context.Background(), l, opts, nil)
	}()
	select {
	case <-done:
		if err != nil && !strings.Contains(err.Error(), "context") && err != context.Canceled {
			// A steer-abort error is acceptable; the contract is return-without-hang.
			t.Logf("RunAgentLoopOnce returned err (acceptable for steer test): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAgentLoopOnce hung for 2s; steer bridge did not unblock the call")
	}
}

// echoTool is the minimal CLI tool the event-translation test
// registers: its Execute returns a fixed string.
type echoTool struct{}

func (echoTool) Name() string               { return "echo" }
func (echoTool) Description() string        { return "echo" }
func (echoTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (echoTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "tool-result", nil
}

// TestRunAgentLoopOnceTranslatesEvents asserts the event bridge: a
// two-step SDK turn (one tool call, then a final answer) emits the
// CLI event sequence step, tool_start, tool_end through the caller's
// OnEvent - the same surface the legacy path publishes. The SDK's
// label payloads ride on Detail.
func TestRunAgentLoopOnceTranslatesEvents(t *testing.T) {
	call := provider.ToolCall{}
	call.Type = "function"
	call.Function.Name = "echo"
	call.Function.Arguments = "{}"
	comp := &scriptCompleter{steps: []provider.Response{
		{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"},
		{Content: "final answer", FinishReason: "stop"},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	l := &Loop{Completer: comp, Tools: reg}

	var mu sync.Mutex
	var got []Event
	opts := Options{
		Model:    "m",
		MaxSteps: 3,
		Backend:  "sdk",
		OnEvent: func(e Event) {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		},
	}
	out, err := l.Run(context.Background(), "hi", opts)
	if err != nil {
		t.Fatalf("Run(sdk): %v", err)
	}
	if out != "final answer" {
		t.Fatalf("Run output = %q, want %q", out, "final answer")
	}
	mu.Lock()
	defer mu.Unlock()
	var kinds []EventKind
	for _, e := range got {
		kinds = append(kinds, e.Kind)
	}
	for _, want := range []EventKind{EventStep, EventToolStart, EventToolEnd} {
		found := false
		for _, k := range kinds {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("event kinds %v missing %q", kinds, want)
		}
	}
	// The tool events must carry the SDK label, which names the tool.
	for _, e := range got {
		if e.Kind == EventToolStart && !strings.Contains(e.Detail, "echo") {
			t.Fatalf("tool_start Detail = %q, want it to name echo", e.Detail)
		}
	}
}
