package agent

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

func toolcallctxWithID(ctx context.Context, id, name string, index int) context.Context {
	return toolcallctx.WithToolCall(ctx, sdkshape.ToolCall{ID: id, Name: name, Index: index, Arguments: []byte(`{}`)})
}

func sdkInOutFor(t *testing.T, args string) sdktools.InOut {
	t.Helper()
	return sdktools.InOut{Value: json.RawMessage(args)}
}

// countingPrivilegedTool records its call context's call ID and counts
// invocations. Capability is ExecutionWrite so the approval gate fires;
// Privileged is set so the test mirrors load_tools's privilege level.
type countingPrivilegedTool struct {
	mu      sync.Mutex
	called  atomic.Int32
	gotID   string
	gotArgs string
}

func (*countingPrivilegedTool) Name() string               { return "load_tools" }
func (*countingPrivilegedTool) Description() string        { return "load deferred tools" }
func (*countingPrivilegedTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *countingPrivilegedTool) Privileged()              {}
func (t *countingPrivilegedTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionWrite, ResourceKey: "session:tool-surface"}
}
func (t *countingPrivilegedTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	t.called.Add(1)
	t.mu.Lock()
	t.gotArgs = string(raw)
	t.mu.Unlock()
	return "loaded: foo", nil
}

// TestSDKBuildToolRegistryStagedMessageShortCircuits pins the FULL
// build path with a tool the dispatcher does NOT have a handler
// for (deferred/advertised-only). StagedMessage must reach the
// SDK admission layer, otherwise the call falls into the legacy
// "tool not available" denial and the model never sees the staged
// message. The carrier doc claims it is carried; this test pins
// the propagation.
func TestSDKBuildToolRegistryStagedMessageShortCircuits(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&countingPrivilegedTool{})

	sdkReg, err := sdkadapter.ConvertToolRegistryWithAdmission(reg, sdkadapter.AdmissionPredicates{
		StagedMessage: func(name string) (string, bool) {
			return "staged for " + name, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, ok := sdkReg.Get("load_tools")
	if !ok {
		t.Fatal("load_tools missing")
	}
	ctx := toolcallctxWithID(context.Background(), "call-load-1", "load_tools", 0)
	out, err := wrapped.Run(ctx, sdkInOutFor(t, `{"names":["foo"]}`))
	if err != nil {
		t.Fatalf("wrapped.Run err = %v", err)
	}
	if s, _ := out.Value.(string); s != "staged for load_tools" {
		t.Fatalf("wrapped out=%q, want %q", s, "staged for load_tools")
	}
}

// TestSDKBuildToolRegistryDoesNotConsultPredicatesForInRegistryTools
// pins the new (correct) build path: StagedMessage and
// UnadmittedToolHandler must NOT be threaded into the SDK admission
// wrapper. The legacy "reg.Get first" ordering survives: a tool
// that IS in the CLI registry (load_tools) must reach the
// dispatcher and the approval gate, never the staged-message
// short-circuit. Without this, the user sees "tool is staged"
// instead of the approval screen.
func TestSDKBuildToolRegistryDoesNotConsultPredicatesForInRegistryTools(t *testing.T) {
	cli := &countingPrivilegedTool{}
	reg := tools.NewRegistry()
	reg.Register(cli)

	stagedFired := make(chan struct{}, 4)
	staged := func(name string) (string, bool) {
		select {
		case stagedFired <- struct{}{}:
		default:
		}
		return "WRONG staged for " + name, true
	}
	unadmitted := func(_ context.Context, name string) (string, bool) {
		t.Errorf("UnadmittedToolHandler called for %q on the SDK path; load_tools is in the registry", name)
		return "WRONG unadmitted for " + name, true
	}

	opts := Options{
		Model:                 "test-model",
		StagedToolMessage:     staged,
		UnadmittedToolHandler: unadmitted,
	}
	loop := &Loop{Completer: &scriptedTurnCompleter{steps: []provider.Response{{Content: "noop", FinishReason: "stop"}}}, Tools: reg}
	turn := newSDKTurnState()
	sdkReg, err := buildSDKToolRegistry(loop, opts, reg, turn)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, ok := sdkReg.Get("load_tools")
	if !ok {
		t.Fatal("load_tools missing")
	}
	ctx := toolcallctxWithID(context.Background(), "call-load-1", "load_tools", 0)
	out, err := wrapped.Run(ctx, sdkInOutFor(t, `{"names":["foo"]}`))
	if err != nil {
		t.Fatalf("wrapped.Run err = %v", err)
	}
	if s, _ := out.Value.(string); s != "loaded: foo" {
		t.Fatalf("wrapped out=%q, want %q (load_tools must invoke through dispatcher, not short-circuit on StagedMessage)", s, "loaded: foo")
	}
	if cli.called.Load() != 1 {
		t.Fatalf("load_tools ran %d times, want 1", cli.called.Load())
	}
	select {
	case <-stagedFired:
		t.Fatal("StagedMessage fired on the SDK path for load_tools; the predicate must NOT short-circuit an in-registry tool")
	default:
	}
}

// TestSDKAdmissionPredicatesReachAdapterLayer proves that
// Options.StagedToolMessage and Options.UnadmittedToolHandler wire
// through to the SDK adapter. Without them, a tool staged by
// TestSDKLoadToolsApprovalRunsAfterApproval pins the load_tools flow
// through the SDK backend with an approval gate. After the user
// approves, the wrapped CLI tool MUST execute. The audit found a
// class of issues where approval either drops the call ID (so the
// resolver's Resolve("") is a no-op and the gate hangs) or the
// admission layer rewrites the body before the inner dispatcher
// shim runs. This test exercises the full chain.
//
// The test drives the wrapper layers directly (turnShapeWrapper ->
// refOnlyShim (skipped) -> approvalGatedToolAdapter ->
// admissionCheckedToolAdapter (skipped) -> dispatcherShim ->
// sdkToolAdapter -> CLI tool) so a regression in any single layer
// surfaces.
func TestSDKLoadToolsApprovalRunsAfterApproval(t *testing.T) {
	cli := &countingPrivilegedTool{}
	reg := tools.NewRegistry()
	reg.Register(cli)

	gate := func(_ context.Context, _ string, _ json.RawMessage) sdkadapter.ApprovalResult {
		return sdkadapter.ApprovalResult{Approved: true}
	}

	var pendingMu sync.Mutex
	var pendingID string
	pending := func(id, name, detail, input string) {
		pendingMu.Lock()
		defer pendingMu.Unlock()
		pendingID = id
	}

	sdkReg, err := sdkadapter.ConvertToolRegistryWithAdmission(reg, sdkadapter.AdmissionPredicates{
		ApprovalGate: gate,
		EmitPending:  pending,
	})
	if err != nil {
		t.Fatal(err)
	}

	wrapped, ok := sdkReg.Get("load_tools")
	if !ok {
		t.Fatal("load_tools not in SDK registry")
	}

	// Drive the wrapped tool with a tool call id matching the
	// admission predicate's contract: toolcallctx carries the ID so
	// the EmitPending closure receives it and the gate's Resolve
	// round-trips correctly.
	ctx := toolcallctxWithID(context.Background(), "call-load-1", "load_tools", 0)
	out, err := wrapped.Run(ctx, sdkInOutFor(t, `{"names":["foo"]}`))
	if err != nil {
		t.Fatalf("wrapped.Run err = %v", err)
	}
	if s, _ := out.Value.(string); s != "loaded: foo" {
		t.Fatalf("wrapped out=%q, want %q (load_tools did not run after approval)", s, "loaded: foo")
	}
	if cli.called.Load() != 1 {
		t.Fatalf("load_tools Execute ran %d times, want 1 (approval gated the call but did not invoke)", cli.called.Load())
	}
	pendingMu.Lock()
	gotID := pendingID
	pendingMu.Unlock()
	if gotID != "call-load-1" {
		t.Fatalf("EmitPending id=%q, want %q (call ID drop strangles the resolver)", gotID, "call-load-1")
	}
}

// scriptedTurnCompleter drives two iterations: the first emits a
// load_tools tool call, the second emits a stop. The harness approves
// the gate call and verifies the tool ran end-to-end.
func TestSDKLoopLoadToolsApprovalRunsAfterApproval(t *testing.T) {
	cli := &countingPrivilegedTool{}
	reg := tools.NewRegistry()
	reg.Register(cli)

	gate := func(_ context.Context, _ string, _ json.RawMessage) sdkadapter.ApprovalResult {
		return sdkadapter.ApprovalResult{Approved: true}
	}

	comp := &scriptedTurnCompleter{steps: []provider.Response{
		{
			FinishReason: "tool_calls",
			ToolCalls: []provider.ToolCall{{
				ID:   "call-load-1",
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "load_tools", Arguments: `{"names":["foo"]}`},
			}},
		},
		{Content: "loaded deferred tool", FinishReason: "stop"},
	}}

	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{
		Model:        "test-model",
		MaxSteps:     3,
		Backend:      "sdk",
		ApprovalGate: gate,
	}

	res, err := loop.Run(context.Background(), "load foo", opts)
	if err != nil {
		t.Fatalf("loop.Run err = %v", err)
	}
	if res != "loaded deferred tool" {
		t.Fatalf("loop result = %q, want %q", res, "loaded deferred tool")
	}
	if cli.called.Load() != 1 {
		t.Fatalf("load_tools ran %d times, want 1 (call did not invoke through SDK approval path)", cli.called.Load())
	}
	cli.mu.Lock()
	gotArgs := cli.gotArgs
	cli.mu.Unlock()
	if gotArgs != `{"names":["foo"]}` {
		t.Fatalf("load_tools args = %q, want %q", gotArgs, `{"names":["foo"]}`)
	}
}
