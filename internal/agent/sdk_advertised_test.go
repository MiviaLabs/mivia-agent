package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkprovider "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// advertisedSpec builds one OpenAI-shaped ToolSpec, the shape
// Options.AdvertisedToolSpecs and Surface.ToolSpecs carry.
func advertisedSpec(name, description string) provider.ToolSpec {
	return provider.ToolSpec{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": description,
			"parameters":  map[string]any{"type": "object"},
		},
	}
}

// toolNames extracts the function names from a request's ToolSpec list.
func toolNames(specs []provider.ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		fn, _ := s["function"].(map[string]any)
		name, _ := fn["name"].(string)
		names = append(names, name)
	}
	return names
}

// containsTool reports whether the request advertised the named tool.
func containsTool(t *testing.T, specs []provider.ToolSpec, want string) bool {
	t.Helper()
	for _, name := range toolNames(specs) {
		if name == want {
			return true
		}
	}
	return false
}

// requestAt returns the i-th recorded ChatTurn request.
func (c *recordingCompleter) requestAt(t *testing.T, i int) provider.Request {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.reqs) {
		t.Fatalf("completer saw %d requests, want index %d", len(c.reqs), i)
	}
	return c.reqs[i]
}

func (c *recordingCompleter) requestCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

func advertisedToolCall(id, name string) provider.ToolCall {
	call := provider.ToolCall{}
	call.Type = "function"
	call.ID = id
	call.Function.Name = name
	call.Function.Arguments = "{}"
	return call
}

// TestRunAgentLoopOnce_Request0CarriesAdvertisedUnion pins the chunk-2
// carrier: a host-pinned AdvertisedToolSpecs snapshot (containing a
// deferred tool the registry does not hold) must reach the wire on
// request 0, the legacy initialToolSpecs contract. The SDK path used
// to derive request 0's tools from the registry only, so "grep" never
// hit the wire until iteration 2's surface bridge.
func TestRunAgentLoopOnce_Request0CarriesAdvertisedUnion(t *testing.T) {
	comp := &recordingCompleter{steps: []provider.Response{
		{Content: "done", FinishReason: "stop"},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	l := &Loop{Completer: comp, Tools: reg}

	_, err := l.Run(context.Background(), "question", Options{MaxSteps: 5,
		AdvertisedToolSpecs: []provider.ToolSpec{
			advertisedSpec("echo", "echo tool"),
			advertisedSpec("grep", "deferred grep tool"),
		},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	req := comp.requestAt(t, 0)
	if !containsTool(t, req.Tools, "grep") {
		t.Fatalf("request 0 tools = %v, want the pinned union including \"grep\"", toolNames(req.Tools))
	}
	if !containsTool(t, req.Tools, "echo") {
		t.Fatalf("request 0 tools = %v, want the pinned union including \"echo\"", toolNames(req.Tools))
	}
	if got := len(req.Tools); got != 2 {
		t.Fatalf("request 0 advertised %d tools, want exactly the 2-spec pinned union (replace, not append)", got)
	}
}

// TestRunAgentLoopOnce_RotationUpdatesAdvertisedUnion pins the
// keep-rule: a surface rotation's non-nil ToolSpecs replace the
// advertised snapshot from the next request on.
func TestRunAgentLoopOnce_RotationUpdatesAdvertisedUnion(t *testing.T) {
	calls := 0
	rot := &Surface{ToolSpecs: []provider.ToolSpec{
		advertisedSpec("echo", "rotated echo"),
		advertisedSpec("rot", "rotation-only tool"),
	}}
	comp := &recordingCompleter{steps: []provider.Response{
		{ToolCalls: []provider.ToolCall{advertisedToolCall("1", "echo")}, FinishReason: "tool_calls"},
		{Content: "done", FinishReason: "stop"},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	l := &Loop{Completer: comp, Tools: reg}

	_, err := l.Run(context.Background(), "question", Options{MaxSteps: 5,
		Surface: func() Surface {
			calls++
			if calls == 1 {
				return *rot
			}
			return Surface{}
		},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	req := comp.requestAt(t, 1)
	if !containsTool(t, req.Tools, "rot") {
		t.Fatalf("request 1 tools = %v, want the rotated union including \"rot\"", toolNames(req.Tools))
	}
	if got := len(req.Tools); got != 2 {
		t.Fatalf("request 1 advertised %d tools, want exactly the rotated 2-spec union", got)
	}
}

// TestRunAgentLoopOnce_NilAdvertisedKeepsRegistryDefs guards the
// subagent/workflow path: without a pinned snapshot the request's
// tools stay the SDK registry-derived definitions.
func TestRunAgentLoopOnce_NilAdvertisedKeepsRegistryDefs(t *testing.T) {
	comp := &recordingCompleter{steps: []provider.Response{
		{Content: "done", FinishReason: "stop"},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	l := &Loop{Completer: comp, Tools: reg}

	_, err := l.Run(context.Background(), "question", Options{MaxSteps: 5})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	req := comp.requestAt(t, 0)
	if !containsTool(t, req.Tools, "echo") {
		t.Fatalf("request 0 tools = %v, want the registry-derived \"echo\"", toolNames(req.Tools))
	}
	if got := len(req.Tools); got != 1 {
		t.Fatalf("request 0 advertised %d tools, want exactly the one registry tool", got)
	}
}

// TestBridgeSurface_NilToolSpecsKeepsSDKSurface pins amendment A: a
// surface rotation that carries no ToolSpecs (and no registry) must
// keep the SDK's prior surface. The bridge used to return a non-nil
// empty Surface, whose apply cleared defs and schemas wholesale, so
// every post-rotation tool call failed with ErrToolNotOffered.
func TestBridgeSurface_NilToolSpecsKeepsSDKSurface(t *testing.T) {
	comp := &recordingCompleter{steps: []provider.Response{
		{ToolCalls: []provider.ToolCall{advertisedToolCall("1", "echo")}, FinishReason: "tool_calls"},
		{ToolCalls: []provider.ToolCall{advertisedToolCall("2", "echo")}, FinishReason: "tool_calls"},
		{Content: "done", FinishReason: "stop"},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	l := &Loop{Completer: comp, Tools: reg}

	_, err := l.Run(context.Background(), "question", Options{MaxSteps: 5,
		Surface: func() Surface { return Surface{} },
	})
	if err != nil {
		t.Fatalf("run failed after a nil-specs rotation: %v", err)
	}
	if got := comp.requestCount(); got != 3 {
		t.Fatalf("completer calls = %d, want 3 (the post-rotation tool call must resolve)", got)
	}
	// The post-rotation request keeps the registry-derived surface.
	req := comp.requestAt(t, 1)
	if !containsTool(t, req.Tools, "echo") {
		t.Fatalf("request 1 tools = %v, want the kept \"echo\" surface", toolNames(req.Tools))
	}
}

// TestBuildAgentLoopOptions_NeverWiresWindow pins the amendment-B
// gate: the SDK's prompt-too-long recovery request
// (agentloop/compaction.go recoverPromptTooLong, which re-sends
// Tools: l.defs through the same completer) runs only when
// Options.Window is non-nil, and the host never wires one. A nil
// Window makes the recovery path unreachable, so the advertised
// override cannot stamp a compaction request.
func TestBuildAgentLoopOptions_NeverWiresWindow(t *testing.T) {
	l := &Loop{Completer: &recordingCompleter{}, Tools: tools.NewRegistry()}
	out, _, err := buildAgentLoopOptions(l, Options{}, "hi")
	if err != nil {
		t.Fatalf("buildAgentLoopOptions failed: %v", err)
	}
	if out.Compaction.Window != nil {
		t.Fatal("buildAgentLoopOptions wired an SDK Window; the prompt-too-long recovery gate relies on Window staying nil")
	}
}

// TestBridgeSurface_RegistryOnlyRotationKeepsRegistryTools is the sibling
// TestBridgeSurface_NilToolSpecsKeepsSDKSurface was missing: a rotation that
// carries a REGISTRY but no ToolSpecs. Surface.Advertised replaces the
// offered set wholesale, so the bridge's non-nil Surface with a nil
// Advertised cleared every tool definition from step 2 on. A host that
// installs a Surface hook and pins no snapshot - headless automation
// sessions and pooled TUI sessions; internal/chat's hook is the only
// producer of Options.Surface, and a loop that installs none keeps the SDK's
// build-time defs - was handed an empty tools[] on every step after the
// first and ended the turn in prose after one tool roundtrip.
func TestBridgeSurface_RegistryOnlyRotationKeepsRegistryTools(t *testing.T) {
	comp := &recordingCompleter{steps: []provider.Response{
		{ToolCalls: []provider.ToolCall{advertisedToolCall("1", "echo")}, FinishReason: "tool_calls"},
		{ToolCalls: []provider.ToolCall{advertisedToolCall("2", "echo")}, FinishReason: "tool_calls"},
		{Content: "done", FinishReason: "stop"},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	l := &Loop{Completer: comp, Tools: reg}

	// No AdvertisedToolSpecs, and a rotation carrying only the registry -
	// the exact shape internal/chat's Surface hook produces for a session
	// that pinned nothing.
	_, err := l.Run(context.Background(), "question", Options{MaxSteps: 5,
		Surface: func() Surface { return Surface{Registry: reg} },
	})
	if err != nil {
		t.Fatalf("run failed after a registry-only rotation: %v", err)
	}
	if got := comp.requestCount(); got != 3 {
		t.Fatalf("completer calls = %d, want 3", got)
	}
	for i := 0; i < 3; i++ {
		req := comp.requestAt(t, i)
		if len(req.Tools) == 0 {
			t.Fatalf("request %d advertised no tools: a registry-only rotation must restate the offered set, not clear it", i)
		}
		if !containsTool(t, req.Tools, "echo") {
			t.Fatalf("request %d tools = %v, want the rotated registry's \"echo\"", i, toolNames(req.Tools))
		}
	}
	if got, want := len(comp.requestAt(t, 1).Tools), len(comp.requestAt(t, 0).Tools); got != want {
		t.Fatalf("post-rotation request advertised %d tools, want the same %d as the pre-rotation request", got, want)
	}
}

// TestBridgeSurface_DerivedAdvertisedNeverPinsTurnState pins that the
// derived set above is a WIRE value only. turn.currentAdvertised feeds
// context preparation (sdk_prepare.go) and loop adoption
// (agentloop_adoption.go); seeding it from a rotation would change those
// inputs on every step boundary for hosts that deliberately pin nothing.
func TestBridgeSurface_DerivedAdvertisedNeverPinsTurnState(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	l := &Loop{Completer: &recordingCompleter{}, Tools: reg}

	disp := surfaceHookDispatcher(t, reg)
	opts := Options{MaxSteps: 5, Dispatcher: disp,
		Surface: func() Surface { return Surface{Registry: reg, Dispatcher: disp} }}
	sdkOpts, turn, err := buildAgentLoopOptions(l, opts, "hi")
	if err != nil {
		t.Fatalf("buildAgentLoopOptions failed: %v", err)
	}
	hook := sdkExtensions(&sdkOpts).Surface
	if hook == nil {
		t.Fatal("buildAgentLoopOptions wired no Surface hook")
	}
	out := hook()
	if out == nil || len(out.Advertised) == 0 {
		t.Fatal("registry-only rotation produced no advertised set")
	}
	if specs := turn.currentAdvertised(); specs != nil {
		t.Fatalf("rotation pinned %d specs onto the turn state; the derived set must stay wire-only", len(specs))
	}
}

// TestBridgeSurface_DefinitionsFailureFailsTheRotation pins that a registry
// whose definitions cannot be computed fails the run through
// recordBridgeError instead of degrading into a surface with no tools -
// the exact silent outcome this branch exists to prevent.
func TestBridgeSurface_DefinitionsFailureFailsTheRotation(t *testing.T) {
	prev := rotatedRegistryDefinitions
	t.Cleanup(func() { rotatedRegistryDefinitions = prev })
	wantErr := errors.New("no schema for tool")
	rotatedRegistryDefinitions = func(*sdktools.Registry, *sdktools.Scope) ([]sdkprovider.ToolDefinition, error) {
		return nil, wantErr
	}

	comp := &recordingCompleter{steps: []provider.Response{
		{ToolCalls: []provider.ToolCall{advertisedToolCall("1", "echo")}, FinishReason: "tool_calls"},
		{Content: "done", FinishReason: "stop"},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	disp := surfaceHookDispatcher(t, reg)
	l := &Loop{Completer: comp, Tools: reg}

	_, err := l.Run(context.Background(), "question", Options{MaxSteps: 5, Dispatcher: disp,
		Surface: func() Surface { return Surface{Registry: reg, Dispatcher: disp} },
	})
	if err == nil {
		t.Fatal("run succeeded after the rotation could not advertise its registry; it must fail instead of running on with no tools")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("run error = %v, want it to carry the Definitions failure %v", err, wantErr)
	}
}
