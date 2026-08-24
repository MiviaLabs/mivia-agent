package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// trimPrepManager counts Prepare calls and passes messages through
// unchanged, so a test can pin HOW OFTEN preparation runs without
// caring what it does.
type trimPrepManager struct {
	calls int
}

func (m *trimPrepManager) Prepare(_ context.Context, in contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	m.calls++
	return contextmgr.Preparation{Messages: in.Messages}, nil
}

func (m *trimPrepManager) Discard(contextmgr.Preparation) {}

// TestSDKTrimRunsPreparationEachIteration pins the per-iteration
// contract: a two-step SDK turn (one tool-call step, one final step)
// runs the host-side preparation TWICE, not once — the legacy
// per-step fidelity the old one-shot prepareSDKHistory lost.
func TestSDKTrimRunsPreparationEachIteration(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{
		name: "read_a", class: tools.ExecutionRead, key: "path:a",
	})
	comp := &scriptCompleter{steps: []provider.Response{
		{
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{tc("1", "read_a", `{"path":"a.txt"}`)},
		},
		{Content: "done", FinishReason: "stop"},
	}}
	mgr := &trimPrepManager{}
	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{Model: "m", MaxSteps: 5, PreparationManager: mgr}
	if _, err := loop.Run(context.Background(), "hello", opts); err != nil {
		t.Fatal(err)
	}
	if mgr.calls < 2 {
		t.Fatalf("Prepare calls = %d, want >= 2 (one per Completer call, iteration 1 included)", mgr.calls)
	}
}

// TestSDKTrimNilManagerKeepsHistory pins the zero-value contract: a
// nil PreparationManager installs no Trim and the run behaves exactly
// as before the Trim hook existed.
func TestSDKTrimNilManagerKeepsHistory(t *testing.T) {
	trim := sdkPrepareTrim(&Loop{}, Options{}, nil)
	if trim != nil {
		t.Fatalf("sdkPrepareTrim with nil PreparationManager returned a non-nil closure")
	}
}

// TestSDKTrimPrepareFailureFailsRun pins the error contract: a Trim
// preparation failure fails the run and records PreparationErr with
// the failure's identity, like a legacy step error.
func TestSDKTrimPrepareFailureFailsRun(t *testing.T) {
	reg := tools.NewRegistry()
	comp := &scriptCompleter{steps: []provider.Response{
		{Content: "unreached", FinishReason: "stop"},
	}}
	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{Model: "m", MaxSteps: 5, PreparationManager: failingPrepManager{}}
	_, err := loop.Run(context.Background(), "hello", opts)
	if err == nil || !strings.Contains(err.Error(), "prep exploded") {
		t.Fatalf("err = %v, want the Trim preparation failure to fail the run", err)
	}
	if loop.PreparationErr == nil || !strings.Contains(loop.PreparationErr.Error(), "prep exploded") {
		t.Fatalf("PreparationErr = %v, want the preparation failure identity", loop.PreparationErr)
	}
}

type errPrep struct{}

func (errPrep) Error() string { return "prep boom" }

// TestSDKTrimPreparationUsesRotatedToolSpecs pins that when dynamic tool surface
// rotation occurs mid-turn, the Trim preparation pass prices the rotated tool specs.
func TestSDKTrimPreparationUsesRotatedToolSpecs(t *testing.T) {
	specA := provider.ToolSpec{"type": "function", "function": map[string]any{"name": "initial_tool"}}
	specB := provider.ToolSpec{"type": "function", "function": map[string]any{"name": "rotated_tool"}}

	var recordedTools [][]provider.ToolSpec
	mgr := &customPrepManager{
		prep: func(_ context.Context, in contextmgr.PrepareInput) (contextmgr.Preparation, error) {
			recordedTools = append(recordedTools, in.Tools)
			return contextmgr.Preparation{Messages: in.Messages}, nil
		},
	}

	turn := &sdkTurnState{}
	turn.setAdvertised([]provider.ToolSpec{specA})

	loop := &Loop{}
	opts := Options{PreparationManager: mgr, AdvertisedToolSpecs: []provider.ToolSpec{specA}}

	trim := sdkPrepareTrim(loop, opts, turn)
	if trim == nil {
		t.Fatal("sdkPrepareTrim returned nil")
	}

	// First Trim call (iteration 1): uses specA
	if _, err := trim(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(recordedTools) != 1 || len(recordedTools[0]) != 1 || recordedTools[0][0]["function"].(map[string]any)["name"] != "initial_tool" {
		t.Fatalf("first trim Tools = %v, want initial_tool", recordedTools)
	}

	// Surface rotation mid-turn installs specB
	turn.setAdvertised([]provider.ToolSpec{specB})

	// Second Trim call (iteration 2): uses specB
	if _, err := trim(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(recordedTools) != 2 || len(recordedTools[1]) != 1 || recordedTools[1][0]["function"].(map[string]any)["name"] != "rotated_tool" {
		t.Fatalf("second trim Tools = %v, want rotated_tool", recordedTools)
	}
}

type customPrepManager struct {
	prep func(context.Context, contextmgr.PrepareInput) (contextmgr.Preparation, error)
}

func (m *customPrepManager) Prepare(ctx context.Context, in contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	return m.prep(ctx, in)
}

func (m *customPrepManager) Discard(contextmgr.Preparation) {}
