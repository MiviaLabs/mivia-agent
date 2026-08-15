package agent

// Plan tools-advertising/01: the serialized tools[] array is pinned per
// binding, so a mid-turn admission (or a scoped-turn registry swap) changes
// EXECUTION authority only, never the wire-advertised array. These tests pin
// that at the Loop level, independent of the chat package's wiring.

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// toolCaptureCompleter captures the raw tools[] array of every request it
// receives, alongside a scripted response sequence.
type toolCaptureCompleter struct {
	mu    sync.Mutex
	steps []provider.Response
	calls int
	tools [][]provider.ToolSpec
}

func (c *toolCaptureCompleter) Name() string { return "recording" }

func (c *toolCaptureCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (c *toolCaptureCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	_, _ = io.WriteString(w, resp.Content)
	return resp.Content, nil
}

func (c *toolCaptureCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tools = append(c.tools, req.Tools)
	index := c.calls
	c.calls++
	if index >= len(c.steps) {
		index = len(c.steps) - 1
	}
	resp := c.steps[index]
	return &resp, nil
}

func (c *toolCaptureCompleter) requestTools() [][]provider.ToolSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]provider.ToolSpec(nil), c.tools...)
}

// TestAdvertisedToolSpecsByteIdenticalAcrossSteps: two consecutive step
// requests serialize a byte-identical tools array, even when the Surface hook
// swaps the execution registry (widened between steps 1 and 2) and the
// dispatcher/registry genuinely differ. Only Options.AdvertisedToolSpecs (the
// host's pinned snapshot) controls what is advertised.
func TestAdvertisedToolSpecsByteIdenticalAcrossSteps(t *testing.T) {
	startReg := tools.NewRegistry()
	startReg.Register(&scheduledTestTool{name: "read_file", class: tools.ExecutionRead, key: "path:a"})

	widerReg := tools.NewRegistry()
	widerReg.Register(&scheduledTestTool{name: "read_file", class: tools.ExecutionRead, key: "path:a"})
	widerReg.Register(&scheduledTestTool{name: "grep", class: tools.ExecutionRead, key: "path:b"})
	widerDisp := surfaceHookDispatcher(t, widerReg)

	pinned := startReg.OpenAITools() // deliberately narrower than widerReg: pinned specs are independent of execution authority

	comp := &toolCaptureCompleter{steps: []provider.Response{
		{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("1", "read_file", `{}`)}},
		{Content: "done", FinishReason: "stop"},
	}}
	loop := &Loop{Completer: comp, Tools: startReg}
	_, err := loop.Run(context.Background(), "run", Options{
		Model:               "m",
		MaxSteps:            5,
		AdvertisedToolSpecs: pinned,
		Surface: func() Surface {
			// Registry/Dispatcher widen mid-turn (simulating a load_tools
			// admission); ToolSpecs is deliberately omitted (nil), so the
			// loop must keep advertising the pinned snapshot, not
			// reg.OpenAITools().
			return Surface{Registry: widerReg, Dispatcher: widerDisp}
		},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	requests := comp.requestTools()
	if len(requests) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(requests))
	}
	first, err := json.Marshal(requests[0])
	if err != nil {
		t.Fatalf("marshal request 0: %v", err)
	}
	second, err := json.Marshal(requests[1])
	if err != nil {
		t.Fatalf("marshal request 1: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("tools[] changed across steps:\nstep0=%s\nstep1=%s", first, second)
	}
	want, err := json.Marshal(pinned)
	if err != nil {
		t.Fatalf("marshal pinned: %v", err)
	}
	if string(first) != string(want) {
		t.Fatalf("advertised tools[] = %s, want the pinned snapshot %s", first, want)
	}
}

// TestAdvertisedToolSpecsFallsBackWhenNil pins that a caller which never sets
// Options.AdvertisedToolSpecs (subagent and workflow-engine loops) keeps
// today's behavior: the live registry's OpenAITools() is what gets advertised.
func TestAdvertisedToolSpecsFallsBackWhenNil(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "read_file", class: tools.ExecutionRead, key: "path:a"})
	comp := &toolCaptureCompleter{steps: []provider.Response{{Content: "done", FinishReason: "stop"}}}
	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "run", Options{Model: "m", MaxSteps: 5}); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	requests := comp.requestTools()
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	got, _ := json.Marshal(requests[0])
	want, _ := json.Marshal(reg.OpenAITools())
	if string(got) != string(want) {
		t.Fatalf("advertised = %s, want the live registry's OpenAITools() %s", got, want)
	}
}
