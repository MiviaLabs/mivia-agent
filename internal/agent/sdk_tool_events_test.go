package agent

// SDK-path tool-event synthesis tests: the legacy wire shape (one
// "queued" tool_start per call from the pre-tool hook, one "running"
// tool_start from the dispatcher shim, one tool_end carrying the
// legacy completed/failed Detail vocabulary, name, call id, and
// redacted output) must survive on the SDK backend. The decisive
// consumer is internal/cli's characterization suite; these tests pin
// the same contract at the agent package boundary.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// failingTool always errors, for the failed tool_end variant.
type failingTool struct{}

func (failingTool) Name() string               { return "fail_tool" }
func (failingTool) Description() string        { return "always fails" }
func (failingTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (failingTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", context.DeadlineExceeded
}

// runSDKToolTurn drives one Backend "sdk" turn whose first step issues
// toolCalls and whose second step answers, returning the collected
// events.
func runSDKToolTurn(t *testing.T, reg *tools.Registry, calls []provider.ToolCall, extra func(*Options)) []Event {
	t.Helper()
	steps := []provider.Response{
		{ToolCalls: calls, FinishReason: "tool_calls"},
		{Content: "final answer", FinishReason: "stop"},
	}
	comp := &scriptCompleter{steps: steps}
	l := &Loop{Completer: comp, Tools: reg}
	var mu sync.Mutex
	var got []Event
	opts := Options{
		Model:    "m",
		MaxSteps: 5,
		OnEvent: func(e Event) {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		},
	}
	if extra != nil {
		extra(&opts)
	}
	if _, err := l.Run(context.Background(), "go", opts); err != nil {
		t.Fatalf("Run(sdk): %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return got
}

func toolEventsOf(events []Event, kind EventKind) []Event {
	var out []Event
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// TestBridgeEvents_ToolStartQueuedThenRunning pins the two-starts-per-
// call wire shape: the first tool_start is "queued" with the redacted
// Input preview, the second is "running" with no Input of its own, and
// both carry the call id and tool name.
func TestBridgeEvents_ToolStartQueuedThenRunning(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	events := runSDKToolTurn(t, reg, []provider.ToolCall{tc("call_1", "echo", `{"argv":["echo","ok"]}`)}, nil)

	starts := toolEventsOf(events, EventToolStart)
	if len(starts) != 2 {
		t.Fatalf("tool_start count = %d, want 2 (queued + running)", len(starts))
	}
	queued, running := starts[0], starts[1]
	if queued.Detail != "queued" {
		t.Fatalf("first tool_start Detail = %q, want queued", queued.Detail)
	}
	if queued.ToolCallID != "call_1" || queued.Name != "echo" {
		t.Fatalf("queued tool_start = id %q name %q, want call_1/echo", queued.ToolCallID, queued.Name)
	}
	if want := redactToolInput(`{"argv":["echo","ok"]}`); queued.Input != want {
		t.Fatalf("queued tool_start Input = %q, want redacted preview %q", queued.Input, want)
	}
	if running.Detail != "running" {
		t.Fatalf("second tool_start Detail = %q, want running", running.Detail)
	}
	if running.ToolCallID != "call_1" || running.Name != "echo" {
		t.Fatalf("running tool_start = id %q name %q, want call_1/echo", running.ToolCallID, running.Name)
	}
	if running.Input != "" {
		t.Fatalf("running tool_start Input = %q, want empty", running.Input)
	}
}

// TestBridgeEvents_ToolEndCarriesNameIDOutputStatus pins the tool_end
// wire shape for both outcomes: the ok variant carries the legacy
// "completed" Detail vocabulary (so uiadapter derives status ok) with
// the redacted output; the failed variant carries a "failed"-prefixed
// Detail so the status derives as failed.
func TestBridgeEvents_ToolEndCarriesNameIDOutputStatus(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	events := runSDKToolTurn(t, reg, []provider.ToolCall{tc("call_ok", "echo", `{}`)}, nil)
	ends := toolEventsOf(events, EventToolEnd)
	if len(ends) != 1 {
		t.Fatalf("ok turn tool_end count = %d, want 1", len(ends))
	}
	ok := ends[0]
	if ok.ToolCallID != "call_ok" || ok.Name != "echo" {
		t.Fatalf("ok tool_end = id %q name %q, want call_ok/echo", ok.ToolCallID, ok.Name)
	}
	if ok.Detail != "completed" {
		t.Fatalf("ok tool_end Detail = %q, want completed", ok.Detail)
	}
	if ok.Output != "tool-result" {
		t.Fatalf("ok tool_end Output = %q, want tool-result", ok.Output)
	}

	freg := tools.NewRegistry()
	freg.Register(failingTool{})
	fevents := runSDKToolTurn(t, freg, []provider.ToolCall{tc("call_bad", "fail_tool", `{}`)}, nil)
	fends := toolEventsOf(fevents, EventToolEnd)
	if len(fends) != 1 {
		t.Fatalf("failed turn tool_end count = %d, want 1", len(fends))
	}
	bad := fends[0]
	if bad.ToolCallID != "call_bad" || bad.Name != "fail_tool" {
		t.Fatalf("failed tool_end = id %q name %q, want call_bad/fail_tool", bad.ToolCallID, bad.Name)
	}
	if !strings.HasPrefix(bad.Detail, "failed") {
		t.Fatalf("failed tool_end Detail = %q, want the legacy failed vocabulary", bad.Detail)
	}
	if !strings.Contains(bad.Output, "error") {
		t.Fatalf("failed tool_end Output = %q, want the error body", bad.Output)
	}
}

// TestBridgeEvents_StagedCallEmitsSingleQueuedStart pins the chunk-1
// denial mirror's event shape: a call the staged-tool admission denies
// gets ONE queued tool_start (PointPreTool fires before the SDK's
// decode), no running start (the dispatcher shim never runs), and a
// failed tool_end from the recorded synthesized-denial outcome.
func TestBridgeEvents_StagedCallEmitsSingleQueuedStart(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	events := runSDKToolTurn(t, reg, []provider.ToolCall{tc("call_9", "staged_tool", `{}`)}, func(o *Options) {
		o.StagedToolMessage = func(name string) (string, bool) {
			if name == "staged_tool" {
				return "tool staged_tool is not admitted yet", true
			}
			return "", false
		}
	})
	starts := toolEventsOf(events, EventToolStart)
	if len(starts) != 1 {
		t.Fatalf("staged tool_start count = %d, want 1 (queued only)", len(starts))
	}
	if starts[0].Detail != "queued" {
		t.Fatalf("staged tool_start Detail = %q, want queued", starts[0].Detail)
	}
	ends := toolEventsOf(events, EventToolEnd)
	if len(ends) != 1 {
		t.Fatalf("staged tool_end count = %d, want 1", len(ends))
	}
	if !strings.HasPrefix(ends[0].Detail, "failed") {
		t.Fatalf("staged tool_end Detail = %q, want failed", ends[0].Detail)
	}
	if !strings.Contains(ends[0].Output, "staged_tool") {
		t.Fatalf("staged tool_end Output = %q, want the denial body", ends[0].Output)
	}
}

// TestBridgeEvents_StreamRevokeStillOncePerIteration pins the moved
// revoke gate: two tool calls inside one iteration revoke the
// optimistic stream exactly once, and the revoke still happens (the
// queued emission ordering) before the tools run.
func TestBridgeEvents_StreamRevokeStillOncePerIteration(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	var fw revokeBuffer
	runSDKToolTurn(t, reg, []provider.ToolCall{
		tc("1", "echo", `{}`),
		tc("2", "echo", `{}`),
	}, func(o *Options) { o.FinalWriter = &fw })
	if fw.revokeN != 1 {
		t.Fatalf("RevokeStream called %d times, want 1 per iteration", fw.revokeN)
	}
}
