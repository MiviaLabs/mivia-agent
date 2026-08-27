package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// prepFailManager fails Prepare with the given error and never falls
// back on its own; it records the ctx each call saw.
type prepCallRecorder struct {
	mu    sync.Mutex
	ctxs  []context.Context
	fail  error
	calls int
}

func (p *prepCallRecorder) Prepare(ctx context.Context, in contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	p.mu.Lock()
	p.calls++
	p.ctxs = append(p.ctxs, ctx)
	p.mu.Unlock()
	if ctx.Err() != nil {
		return contextmgr.Preparation{}, ctx.Err()
	}
	if p.fail != nil {
		return contextmgr.Preparation{}, p.fail
	}
	return contextmgr.Preparation{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "prepared"}},
	}, nil
}

func (p *prepCallRecorder) Discard(contextmgr.Preparation) {}

// TestSDKPrepareOnceRecordsPreparationErr pins that the first
// Prepare failure sets loop.PreparationErr (identity, not a wrap) so
// the turn commit can carry it, while the error still propagates -
// matching legacy prepareStep (context.go:27).
func TestSDKPrepareOnceRecordsPreparationErr(t *testing.T) {
	boom := errors.New("prep exploded")
	mgr := &prepCallRecorder{fail: boom}
	loop := &Loop{}
	opts := Options{PreparationManager: mgr}
	_, err := prepareSDKOnce(context.Background(), loop, opts, nil, nil)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the preparation failure propagated", err)
	}
	if loop.PreparationErr != boom {
		t.Fatalf("loop.PreparationErr = %v, want the same error identity as Prepare returned", loop.PreparationErr)
	}
}

// TestSDKPrepareOnceFallbackGateMatchesLegacy pins the legacy
// fallback gate (context.go:28): an interrupted ctx on a fresh loop
// with zero WorkLimits retries Prepare once on context.Background();
// success clears PreparationErr, and the fallback ran on a live ctx.
func TestSDKPrepareOnceFallbackGateMatchesLegacy(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	mgr := &prepCallRecorder{}
	loop := &Loop{}
	opts := Options{PreparationManager: mgr}
	msgs, err := prepareSDKOnce(canceled, loop, opts, nil, nil)
	if err != nil {
		t.Fatalf("err = %v, want the fallback Prepare to succeed", err)
	}
	if mgr.calls != 2 {
		t.Fatalf("Prepare calls = %d, want 2 (interrupted attempt plus background fallback)", mgr.calls)
	}
	fallbackCtx := mgr.ctxs[1]
	if fallbackCtx == nil || fallbackCtx.Err() != nil || fallbackCtx == canceled {
		t.Fatal("fallback Prepare did not run on a live context.Background ctx")
	}
	if loop.PreparationErr != nil {
		t.Fatalf("loop.PreparationErr = %v, want nil after a successful fallback", loop.PreparationErr)
	}
	if !loop.HasPreparation {
		t.Fatal("loop.HasPreparation = false, want the fallback preparation recorded")
	}
	if len(msgs) == 0 {
		t.Fatal("msgs empty, want the prepared history converted for the SDK")
	}
}

type prepInputCaptureManager struct {
	mu     sync.Mutex
	inputs []contextmgr.PrepareInput
}

func (m *prepInputCaptureManager) Prepare(_ context.Context, in contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	m.mu.Lock()
	m.inputs = append(m.inputs, in)
	m.mu.Unlock()
	return contextmgr.Preparation{Messages: in.Messages}, nil
}

func (m *prepInputCaptureManager) Discard(contextmgr.Preparation) {}

// TestSDKPrepareOncePassesToolSpecsToPreparation pins that prepareSDKOnce
// passes tool schemas to the preparation manager from AdvertisedToolSpecs,
// Tools registry, live turn rotations, or PreparationInput.Tools fallback.
func TestSDKPrepareOncePassesToolSpecsToPreparation(t *testing.T) {
	specA := provider.ToolSpec{"type": "function", "function": map[string]any{"name": "tool_a"}}
	specB := provider.ToolSpec{"type": "function", "function": map[string]any{"name": "tool_b"}}
	specC := provider.ToolSpec{"type": "function", "function": map[string]any{"name": "tool_c"}}

	// Case 1: AdvertisedToolSpecs takes priority
	mgr1 := &prepInputCaptureManager{}
	loop1 := &Loop{}
	opts1 := Options{
		PreparationManager:  mgr1,
		AdvertisedToolSpecs: []provider.ToolSpec{specA},
	}
	if _, err := prepareSDKOnce(context.Background(), loop1, opts1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(mgr1.inputs) != 1 || len(mgr1.inputs[0].Tools) != 1 || mgr1.inputs[0].Tools[0]["type"] != "function" {
		t.Fatalf("case 1 Tools = %v, want specA", mgr1.inputs[0].Tools)
	}

	// Case 2: Turn advertised state overrides initial tools
	mgr2 := &prepInputCaptureManager{}
	turn := &sdkTurnState{}
	turn.setAdvertised([]provider.ToolSpec{specB})
	loop2 := &Loop{}
	opts2 := Options{
		PreparationManager:  mgr2,
		AdvertisedToolSpecs: []provider.ToolSpec{specA},
	}
	if _, err := prepareSDKOnce(context.Background(), loop2, opts2, turn, nil); err != nil {
		t.Fatal(err)
	}
	if len(mgr2.inputs) != 1 || len(mgr2.inputs[0].Tools) != 1 {
		t.Fatalf("case 2 Tools = %v, want specB", mgr2.inputs[0].Tools)
	}
	if fn := mgr2.inputs[0].Tools[0]["function"].(map[string]any); fn["name"] != "tool_b" {
		t.Fatalf("case 2 function name = %v, want tool_b", fn["name"])
	}

	// Case 3: Fallback to PreparationInput.Tools
	mgr3 := &prepInputCaptureManager{}
	loop3 := &Loop{}
	opts3 := Options{
		PreparationManager: mgr3,
		PreparationInput:   contextmgr.PrepareInput{Tools: []provider.ToolSpec{specC}},
	}
	if _, err := prepareSDKOnce(context.Background(), loop3, opts3, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(mgr3.inputs) != 1 || len(mgr3.inputs[0].Tools) != 1 {
		t.Fatalf("case 3 Tools = %v, want specC", mgr3.inputs[0].Tools)
	}
}

// TestSDKPrepareOnceTriggersCompactionWithToolSchemaCost reproduces the
// bug: history tokens alone are below trigger, but history + tool schemas
// exceed the trigger threshold, properly firing compaction and accounting for tools.
func TestSDKPrepareOnceTriggersCompactionWithToolSchemaCost(t *testing.T) {
	principal, binding := elisionPrincipalBinding(t)

	// Build a history with multiple turns
	history := []provider.Message{
		{Role: provider.RoleSystem, Content: "System prompt."},
		{Role: provider.RoleUser, Content: "User turn 1: " + strings.Repeat("a", 200)},
		{Role: provider.RoleAssistant, Content: "Assistant turn 1: " + strings.Repeat("b", 200)},
		{Role: provider.RoleUser, Content: "User turn 2: " + strings.Repeat("c", 200)},
		{Role: provider.RoleAssistant, Content: "Assistant turn 2: " + strings.Repeat("d", 200)},
		{Role: provider.RoleUser, Content: "User turn 3: " + strings.Repeat("e", 200)},
		{Role: provider.RoleAssistant, Content: "Assistant turn 3: " + strings.Repeat("f", 200)},
		{Role: provider.RoleUser, Content: "Current objective: " + strings.Repeat("g", 50)},
	}

	// Create tool specs
	var toolSpecs []provider.ToolSpec
	for i := 0; i < 5; i++ {
		toolSpecs = append(toolSpecs, provider.ToolSpec{
			"type": "function",
			"function": map[string]any{
				"name":        "test_tool_" + strings.Repeat("x", 10),
				"description": "Tool description " + strings.Repeat("y", 40),
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"param1": map[string]any{"type": "string"},
					},
				},
			},
		})
	}

	historyCost, err := provider.EstimatePromptCost(history, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	totalCost, err := provider.EstimatePromptCost(history, toolSpecs, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}

	// Choose budget such that:
	// 1. historyCost < trigger (80% of budget) -> without tools, no compaction
	// 2. totalCost > trigger (80% of budget)   -> with tools, compaction triggers
	// 3. target (50% of budget) > mandatory set + toolCost -> compaction succeeds
	// Set trigger midway between historyCost and totalCost:
	trigger := historyCost + (totalCost-historyCost)/2
	budget := trigger * 10 / 8

	loop := &Loop{Messages: history}
	opts := Options{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		AdvertisedToolSpecs: toolSpecs,
		PreparationInput: contextmgr.PrepareInput{
			Budget:    budget,
			Principal: principal,
			Binding:   binding,
			Revision:  contextstate.Revision{Session: 1, Durable: 1, Source: 1},
		},
	}

	prepared, err := prepareSDKOnce(context.Background(), loop, opts, nil, history)
	if err != nil {
		t.Fatalf("prepareSDKOnce failed: %v", err)
	}
	if !loop.HasPreparation || !loop.LastPreparation.Compacted {
		t.Fatalf("compaction did not trigger: HasPreparation=%v Compacted=%v (beforeTokens=%d trigger=%d)",
			loop.HasPreparation, loop.LastPreparation.Compacted, loop.LastPreparation.BeforeTokens, loop.LastPreparation.TriggerTokens)
	}
	if loop.LastPreparation.BeforeTokens != totalCost {
		t.Fatalf("BeforeTokens = %d, want totalCost %d", loop.LastPreparation.BeforeTokens, totalCost)
	}
	if len(prepared) >= len(history) {
		t.Fatalf("prepared messages len = %d, want < history len %d", len(prepared), len(history))
	}
}

// TestInitialToolSpecsEdgeCases pins edge case behavior of initialToolSpecs.
func TestInitialToolSpecsEdgeCases(t *testing.T) {
	specA := provider.ToolSpec{"type": "function"}
	specB := provider.ToolSpec{"type": "other"}

	// nil Loop receiver
	var nilLoop *Loop
	if got := nilLoop.initialToolSpecs(Options{AdvertisedToolSpecs: []provider.ToolSpec{specA}}); len(got) != 1 {
		t.Fatal("nil Loop must return AdvertisedToolSpecs when set")
	}
	if got := nilLoop.initialToolSpecs(Options{PreparationInput: contextmgr.PrepareInput{Tools: []provider.ToolSpec{specB}}}); len(got) != 1 {
		t.Fatal("nil Loop must return PreparationInput.Tools fallback when AdvertisedToolSpecs is nil")
	}
	if got := nilLoop.initialToolSpecs(Options{}); got != nil {
		t.Fatal("nil Loop with no tools must return nil")
	}

	// Loop with nil Tools registry
	emptyLoop := &Loop{}
	if got := emptyLoop.initialToolSpecs(Options{}); got != nil {
		t.Fatal("empty Loop must return nil")
	}
}
