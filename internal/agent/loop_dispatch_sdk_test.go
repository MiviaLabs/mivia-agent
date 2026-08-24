package agent

// Regression tests for the SDK-backed dispatch path: history write-back
// on error, turn-scoped last-text fallback, the step-cap error, and
// CreatedAt preservation across turns. Each test pins a finding from
// the post-flip bug audit (commit series after 72400837).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// scriptedTurnCompleter serves a fixed list of ChatTurn results; after
// the list is exhausted it keeps returning the last entry so a run can
// grind into the step cap.
type scriptedTurnCompleter struct {
	steps []provider.Response
	calls int
}

func (s *scriptedTurnCompleter) Name() string { return "scripted" }
func (s *scriptedTurnCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}
func (s *scriptedTurnCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}
func (s *scriptedTurnCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	i := s.calls
	if i >= len(s.steps) {
		i = len(s.steps) - 1
	}
	s.calls++
	r := s.steps[i]
	return &r, nil
}

// failTurnCompleter succeeds on the first call and fails every later
// one, exercising the mid-turn error path.
type failTurnCompleter struct {
	calls int
	err   error
}

func (f *failTurnCompleter) Name() string { return "failer" }
func (f *failTurnCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}
func (f *failTurnCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}
func (f *failTurnCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	f.calls++
	if f.calls == 1 {
		return &provider.Response{
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{tc("1", "noop_tool", `{}`)},
		}, nil
	}
	return nil, f.err
}

type noopTool struct{}

func (noopTool) Name() string               { return "noop_tool" }
func (noopTool) Description() string        { return "no-op test tool" }
func (noopTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (noopTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "noop result", nil
}

// TestRunOnceSDKStepCapIsError pins the legacy parity rule: exhausting
// the iteration cap on the SDK path is a hard error naming the cap,
// not a graceful partial answer.
func TestRunOnceSDKStepCapIsError(t *testing.T) {
	step := provider.Response{
		FinishReason: "tool_calls",
		ToolCalls:    []provider.ToolCall{tc("1", "noop_tool", `{}`)},
	}
	comp := &scriptedTurnCompleter{steps: []provider.Response{step, step, step}}
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	loop := &Loop{Completer: comp, Tools: reg}
	_, err := loop.Run(context.Background(), "work", Options{Model: "m", MaxSteps: 2})
	if err == nil || !strings.Contains(err.Error(), "exceeded max_steps (2)") {
		t.Fatalf("err = %v, want max_steps error naming the cap", err)
	}
}

// TestRunOnceSDKKeepsPartialHistoryOnError pins the history-durability
// contract: when the completer fails mid-turn, the loop's Messages
// still carry this turn's user message and the completed step's tool
// result, like the legacy path.
func TestRunOnceSDKKeepsPartialHistoryOnError(t *testing.T) {
	comp := &failTurnCompleter{err: context.DeadlineExceeded}
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	loop := &Loop{Completer: comp, Tools: reg}
	_, err := loop.Run(context.Background(), "do work", Options{Model: "m", MaxSteps: 5})
	if err == nil {
		t.Fatal("want mid-turn error")
	}
	var gotUser, gotTool bool
	for _, m := range loop.Messages {
		if m.Role == provider.RoleUser && m.Content == "do work" {
			gotUser = true
		}
		if m.Role == provider.RoleTool && m.ToolCallID == "1" {
			gotTool = true
		}
	}
	if !gotUser || !gotTool {
		t.Fatalf("partial history lost: user=%v tool=%v, messages=%+v", gotUser, gotTool, loop.Messages)
	}
}

// TestRunOnceSDKPreservesCarriedCreatedAt pins the timestamp contract:
// converting the SDK history back must not zero CreatedAt on messages
// that already carried one.
func TestRunOnceSDKPreservesCarriedCreatedAt(t *testing.T) {
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	comp := &scriptedTurnCompleter{steps: []provider.Response{{Content: "hi", FinishReason: "stop"}}}
	loop := &Loop{Completer: comp, Tools: reg, Messages: []provider.Message{
		{Role: provider.RoleUser, Content: "earlier", CreatedAt: stamp},
	}}
	if _, err := loop.Run(context.Background(), "next", Options{Model: "m", MaxSteps: 3}); err != nil {
		t.Fatal(err)
	}
	if len(loop.Messages) == 0 || !loop.Messages[0].CreatedAt.Equal(stamp) {
		t.Fatalf("carried message CreatedAt = %v, want %v", loop.Messages[0].CreatedAt, stamp)
	}
}

// TestFinalizeSDKTurnUsesTurnWideLastText pins the RequireFinalText
// contract ("no assistant text ANYWHERE in the turn"): a zero Final
// with text in an earlier iteration must reach FinalWriter and must
// not trip the refusal. A prior turn's text (below startLen) must not
// count.
func TestFinalizeSDKTurnUsesTurnWideLastText(t *testing.T) {
	prior := sdkshape.Message{Role: sdkshape.RoleAssistant, Content: "prior turn answer"}
	user := sdkshape.Message{Role: sdkshape.RoleUser, Content: "question"}
	textStep := sdkshape.Message{Role: sdkshape.RoleAssistant, Content: "partial answer"}
	res := sdkagentloop.Result{
		Stop:    sdkagentloop.StopMaxIterations,
		History: []sdkshape.Message{prior, user, textStep},
	}
	var buf bytes.Buffer
	err := finalizeSDKTurn(Options{RequireFinalText: true, FinalWriter: &buf}, res, "question")
	if err != nil {
		t.Fatalf("finalizeSDKTurn: %v", err)
	}
	if buf.String() != "partial answer" {
		t.Fatalf("FinalWriter got %q, want the turn's last assistant text", buf.String())
	}
}

// TestFinalizeSDKTurnPriorTurnTextDoesNotSatisfyRequire pins the
// turn-scoped bound: with no assistant text in THIS turn, the refusal
// fires even though an earlier turn's answer sits in the history.
func TestFinalizeSDKTurnPriorTurnTextDoesNotSatisfyRequire(t *testing.T) {
	prior := sdkshape.Message{Role: sdkshape.RoleAssistant, Content: "prior turn answer"}
	user := sdkshape.Message{Role: sdkshape.RoleUser, Content: "question"}
	res := sdkagentloop.Result{
		Stop:    sdkagentloop.StopMaxIterations,
		History: []sdkshape.Message{prior, user},
	}
	err := finalizeSDKTurn(Options{RequireFinalText: true}, res, "question")
	if err == nil || !strings.Contains(err.Error(), "no assistant text") {
		t.Fatalf("err = %v, want the empty-turn refusal", err)
	}
}

// TestFinalizeSDKTurnSteeredStopSkipsRequire pins the steer identity:
// a steered stop must not be converted into the RequireFinalText
// error; the dispatcher maps it to errSteerInterrupt instead.
func TestFinalizeSDKTurnSteeredStopSkipsRequire(t *testing.T) {
	res := sdkagentloop.Result{Stop: sdkagentloop.StopSteered}
	if err := finalizeSDKTurn(Options{RequireFinalText: true}, res, ""); err != nil {
		t.Fatalf("steered stop must not fail finalize: %v", err)
	}
}

// TestRefOnlyShimKeepsToolInSDKDefinitions pins the High audit finding:
// the shim must implement SchemaTool, otherwise the SDK's Definitions
// silently drops the ref-only tool from the offered set.
func TestRefOnlyShimKeepsToolInSDKDefinitions(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&refOnlyTestTool{name: "bigtool", body: strings.Repeat("x", BatchDegradeFloorBytes+1)})
	reg.Register(noopTool{})
	sdkReg, err := sdkadapter.ConvertToolRegistry(reg)
	if err != nil {
		t.Fatal(err)
	}
	spool, _ := testSpool(t)
	applyRefOnlyShim(sdkReg, reg, []string{"bigtool"}, spool, BatchDegradeFloorBytes, "principal-defs", nil)
	wrapped, ok := sdkReg.Get("bigtool")
	if !ok {
		t.Fatal("bigtool missing from SDK registry")
	}
	st, ok := wrapped.(sdktools.SchemaTool)
	if !ok {
		t.Fatal("shim does not implement SchemaTool")
	}
	if string(st.ParameterSchema()) == "" {
		t.Fatal("shim dropped the parameter schema")
	}
	if _, err := st.DecodeArguments([]byte(`{}`)); err != nil {
		t.Fatalf("shim DecodeArguments: %v", err)
	}
	defs, _, err := sdkagentloop.Definitions(sdkReg, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range defs {
		if d.Name == "bigtool" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ref-only tool missing from SDK definitions: %+v", defs)
	}
}

// TestRefOnlyShimSkipsEphemeralTool pins the ephemeral contract: a
// tool implementing tools.EphemeralResultTool must never be spooled
// durably, mirroring refOnlyTier's p.ephemeral skip.
func TestRefOnlyShimSkipsEphemeralTool(t *testing.T) {
	reg := tools.NewRegistry()
	eph := &ephemeralRefOnlyTool{body: strings.Repeat("y", BatchDegradeFloorBytes+1)}
	reg.Register(eph)
	sdkReg, err := sdkadapter.ConvertToolRegistry(reg)
	if err != nil {
		t.Fatal(err)
	}
	spool, store := testSpool(t)
	applyRefOnlyShim(sdkReg, reg, []string{"eph_tool"}, spool, BatchDegradeFloorBytes, "principal-eph", nil)
	tool, ok := sdkReg.Get("eph_tool")
	if !ok {
		t.Fatal("eph_tool missing from SDK registry")
	}
	out, err := tool.Run(context.Background(), sdktools.InOut{Value: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if store.Len() != 0 {
		t.Fatalf("ephemeral body spooled: store holds %d bodies, want 0", store.Len())
	}
	if s, _ := out.Value.(string); s != eph.body {
		t.Fatalf("ephemeral body altered: %q", s[:min(40, len(s))])
	}
}

type ephemeralRefOnlyTool struct{ body string }

func (e *ephemeralRefOnlyTool) Name() string               { return "eph_tool" }
func (e *ephemeralRefOnlyTool) Description() string        { return "ephemeral ref-only test tool" }
func (e *ephemeralRefOnlyTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (e *ephemeralRefOnlyTool) Execute(context.Context, json.RawMessage) (string, error) {
	return e.body, nil
}
func (e *ephemeralRefOnlyTool) EphemeralResultMarker(json.RawMessage) string { return "ephemeral" }

// TestEffectiveSDKMaxIterations pins the cap the step-cap error names:
// unset MaxSteps maps to the documented default, and a stricter
// MaxTurns wins in both the set and unset MaxSteps cases.
func TestEffectiveSDKMaxIterations(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want int
	}{
		{"unset", Options{}, defaultSDKMaxIterations},
		{"set", Options{MaxSteps: 7}, 7},
		{"turns under steps", Options{MaxSteps: 20, WorkLimits: runtime.WorkLimits{MaxTurns: 10}}, 10},
		{"turns over steps", Options{MaxSteps: 20, WorkLimits: runtime.WorkLimits{MaxTurns: 30}}, 20},
		{"turns with unset steps", Options{WorkLimits: runtime.WorkLimits{MaxTurns: 40}}, 40},
	}
	for _, c := range cases {
		if got := effectiveSDKMaxIterations(c.opts); got != c.want {
			t.Fatalf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// errWriter fails on the first Write, pinning the FinalWriter error
// branch of finalizeSDKTurn.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, context.DeadlineExceeded }

func TestFinalizeSDKTurnFinalWriterErrorIsHard(t *testing.T) {
	// A zero Final: the text comes from history, so finalize is the
	// writer's only delivery path and the write error surfaces. (A
	// non-empty Final streamed live and is never rewritten.)
	res := sdkagentloop.Result{History: []sdkshape.Message{
		{Role: sdkshape.RoleUser, Content: "q"},
		{Role: sdkshape.RoleAssistant, Content: "answer"},
	}}
	err := finalizeSDKTurn(Options{FinalWriter: errWriter{}}, res, "q")
	if err == nil || !strings.Contains(err.Error(), "write final text") {
		t.Fatalf("err = %v, want the final-text write error", err)
	}
}
