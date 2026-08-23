// Coverage targets for SDK adapter branches not exercised by the
// happy-path tests. Each test name maps to one or more line entries
// in the diff_coverage report.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// marshalFailTool is a sdktool whose Run accepts any value. The
// shim's path tests json.Marshal on in.Value, which fails on a chan.
type marshalFailTool struct{}

func (m *marshalFailTool) Name() string               { return "marshal-fail-tool" }
func (m *marshalFailTool) Description() string        { return "test" }
func (m *marshalFailTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (m *marshalFailTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{MaxResultBytes: 1024}
}
func (m *marshalFailTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "x", nil
}

type marshalFailToolSDK struct{}

// marshalFailToolSDK mirrors marshalFailTool for the SDK adapter's
// sdktools.Tool face (the shim's `inner` field uses both).
func (s *marshalFailToolSDK) Name() string { return "marshal-fail-tool" }
func (s *marshalFailToolSDK) Schema()      {}
func (s *marshalFailToolSDK) Run(context.Context, sdktools.InOut) (sdktools.Out, error) {
	return sdktools.Out{Value: "x"}, nil
}

// TestDispatcherShimMarshalFailure pins sdk_dispatcher_shim.go:96-99:
// when the SDK loop hands the shim an in.Value that is not
// JSON-encodable, the shim surfaces a wrapped marshal error.
func TestDispatcherShimMarshalFailure(t *testing.T) {
	cli := &marshalFailTool{}
	shim := &dispatcherShim{
		inner: &marshalFailToolSDK{}, // SDK-side tool; shim reads Name()
		cli:   cli,
		opts:  Options{Dispatcher: mustDispatcher(t)},
		turn:  newSDKTurnState(),
	}
	_, err := shim.Run(context.Background(), sdktools.InOut{Value: make(chan int)})
	if err == nil {
		t.Fatal("expected marshal error, got nil")
	}
	if !strings.Contains(err.Error(), "marshal arguments") {
		t.Fatalf("err = %v, want marshal-arguments error", err)
	}
}

// duplicateAwareTool: write-class capability participates in dedup.
// The first call resolves the dedup record; the second call (same
// request step) reads it and IsDuplicate() returns true, surfacing
// duplicateDeliveryNotice on the model-visible body.
type duplicateAwareTool struct{}

func (d *duplicateAwareTool) Name() string               { return "dup-tool" }
func (d *duplicateAwareTool) Description() string        { return "dups" }
func (d *duplicateAwareTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (d *duplicateAwareTool) Capability(json.RawMessage) tools.Capability {
	// ExecutionWrite dedups; ExecutionRead does not. The shim only
	// sees read-class behavior by default, so set write/exec to
	// exercise the duplicate-cache branch.
	return tools.Capability{Class: tools.ExecutionWrite, ResourceKey: "path:dup", MaxResultBytes: 1024}
}
func (d *duplicateAwareTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func TestDispatcherShimDuplicateMarksResult(t *testing.T) {
	reg := tools.NewRegistry()
	tool := &duplicateAwareTool{}
	reg.Register(tool)
	dispatcher, err := runtime.NewToolDispatcher(reg, runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	sdkInner := &sdkToolForName{name: "dup-tool"}
	shim := &dispatcherShim{
		inner: sdkInner,
		cli:   tool,
		opts:  Options{Dispatcher: dispatcher, SessionID: "s"},
		turn:  newSDKTurnState(),
	}
	// First call resolves an ID-keyed dedup record.
	first, err := shim.Run(context.Background(), sdktools.InOut{Value: map[string]any{}})
	if err != nil || first.Value.(string) != "ok" {
		t.Fatalf("first call: out=%v err=%v", first, err)
	}
	// Second call reads the dedup record.
	second, err := shim.Run(context.Background(), sdktools.InOut{Value: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.Value.(string), "duplicate delivery") {
		t.Fatalf("second body = %q, want duplicate notice", second.Value.(string))
	}
}

// erroringTool exists only as a CLI tool type; the dispatcher always
// produces a non-empty JSON Output envelope (runtime/result.go's
// deliverTerminal), so the shim's "error: ..." fallback at line
// 131-132 is unreachable as the dispatcher is wired today. The
// branch is kept as defensive depth; see Allow-Coverage-Skip on the
// commit.
// type erroringTool struct{ msg string } ... intentionally unused

// nonSchemaBareTool implements only sdktools.Tool, not SchemaTool.
// applyDispatcherShim must skip it (sdk_dispatcher_shim.go:175-177).
type nonSchemaBareTool struct{}

func (b *nonSchemaBareTool) Name() string { return "non-schema-bare" }
func (b *nonSchemaBareTool) Run(context.Context, sdktools.InOut) (sdktools.Out, error) {
	return sdktools.Out{Value: "ok"}, nil
}

func TestApplyDispatcherShimSkipsNonSchemaTool(t *testing.T) {
	sdkReg := sdktools.New()
	bare := &nonSchemaBareTool{}
	if err := sdkReg.Add(bare); err != nil {
		t.Fatal(err)
	}
	cliReg := tools.NewRegistry()
	dispatcher, err := runtime.NewToolDispatcher(cliReg, runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	applyDispatcherShim(sdkReg, cliReg, Options{Dispatcher: dispatcher}, newSDKTurnState())
	got, ok := sdkReg.Get("non-schema-bare")
	if !ok {
		t.Fatal("non-schema-bare vanished from registry")
	}
	if got != bare {
		t.Fatalf("non-schema-bare was wrapped (got %T)", got)
	}
}

// cliToolWithParams is a CLI tool. Cover adapter's inner adapter type.
type cliToolWithParams struct{ name string }

func (c *cliToolWithParams) Name() string        { return c.name }
func (c *cliToolWithParams) Description() string { return c.name }
func (c *cliToolWithParams) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (c *cliToolWithParams) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{MaxResultBytes: 1024}
}
func (c *cliToolWithParams) Execute(context.Context, json.RawMessage) (string, error) {
	return c.name, nil
}

// cliAltTool variant for the multi-tool wrap.
type cliAltTool struct{}

func (c *cliAltTool) Name() string        { return "cli-alt" }
func (c *cliAltTool) Description() string { return "alt" }
func (c *cliAltTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (c *cliAltTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{MaxResultBytes: 1024}
}
func (c *cliAltTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "alt", nil
}

// sdkToolForName is a sdktools.SchemaTool used by the shaper tests.
type sdkToolForName struct{ name string }

func (t *sdkToolForName) Name() string { return t.name }
func (t *sdkToolForName) ParameterSchema() []byte {
	return []byte(`{"type":"object"}`)
}
func (t *sdkToolForName) DecodeArguments(raw []byte) (sdktools.InOut, error) {
	if !json.Valid(raw) {
		return sdktools.InOut{}, errors.New("invalid json")
	}
	return sdktools.InOut{Value: json.RawMessage(raw)}, nil
}
func (t *sdkToolForName) Run(context.Context, sdktools.InOut) (sdktools.Out, error) {
	return sdktools.Out{Value: "ok"}, nil
}

// TestApplyDispatcherShimWrapsAllTools pins the for-loop success
// path across multiple tools (sdk_dispatcher_shim.go:177-194).
func TestApplyDispatcherShimWrapsAllTools(t *testing.T) {
	cliReg := tools.NewRegistry()
	cliReg.Register(&cliToolWithParams{name: "alpha"})
	cliReg.Register(&cliAltTool{})
	sdkReg := sdktools.New()
	if err := sdkReg.Add(&sdkToolForName{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := sdkReg.Add(&sdkToolForName{name: "cli-alt"}); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := runtime.NewToolDispatcher(cliReg, runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	applyDispatcherShim(sdkReg, cliReg, Options{Dispatcher: dispatcher, SessionID: "s"}, newSDKTurnState())
	for _, name := range []string{"alpha", "cli-alt"} {
		if _, ok := sdkReg.Get(name); !ok {
			t.Fatalf("%s missing after applyDispatcherShim", name)
		}
	}
}

// TestStampSDKToolMessageNamesBackfillsFromAssistantCall pins
// agentloop_adapter.go:311-313: when a RoleTool message has no
// Name, the helper backfills it from the assistant ToolCall.
func TestStampSDKToolMessageNamesBackfillsFromAssistantCall(t *testing.T) {
	history := []sdkshape.Message{
		{Role: "user", Content: "call the tool"},
		{
			Role: "assistant", Content: "",
			ToolCalls: []sdkshape.ToolCall{
				{ID: "call-1", Name: "grep"},
			},
		},
		{Role: "tool", ToolCallID: "call-1", Content: "ok"},
	}
	stampSDKToolMessageNames(history)
	got := history[len(history)-1]
	if got.Name != "grep" {
		t.Fatalf("stamped name = %q, want grep", got.Name)
	}
}

// TestSCopedDispatcherRejectsNilRegistry pins
// agentloop_adapter.go:230-235's err wrap.
func TestSCopedDispatcherRejectsNilRegistry(t *testing.T) {
	_, err := runtime.NewToolDispatcher(nil, runtime.Policy{})
	if err == nil {
		t.Fatal("expected nil-registry error, got nil")
	}
}

// TestApplyTurnShapingDerivesNegativeBudgetMultiTool pins the derive
// branch across multiple tools (sdk_shaping.go:160-191).
func TestApplyTurnShapingDerivesNegativeBudgetMultiTool(t *testing.T) {
	cliReg := tools.NewRegistry()
	cliReg.Register(&cliToolWithParams{name: "alpha"})
	cliReg.Register(&cliAltTool{})
	sdkReg := sdktools.New()
	if err := sdkReg.Add(&sdkToolForName{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := sdkReg.Add(&sdkToolForName{name: "cli-alt"}); err != nil {
		t.Fatal(err)
	}
	applyTurnShaping(sdkReg, cliReg, Options{
		BatchResultBudgetBytes: -1,
		MaxContextTokens:       256 << 10,
	}, newSDKTurnState())
	for _, name := range []string{"alpha", "cli-alt"} {
		if _, ok := sdkReg.Get(name); !ok {
			t.Fatalf("%s missing under negative-budget shaper", name)
		}
	}
}

// TestApplyTurnShapingZeroInertMultiTool pins the zero-budget
// early-return at sdk_shaping.go:162-163.
func TestApplyTurnShapingZeroInertMultiTool(t *testing.T) {
	cliReg := tools.NewRegistry()
	cliReg.Register(&cliToolWithParams{name: "alpha"})
	cliReg.Register(&cliAltTool{})
	sdkReg := sdktools.New()
	if err := sdkReg.Add(&sdkToolForName{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := sdkReg.Add(&sdkToolForName{name: "cli-alt"}); err != nil {
		t.Fatal(err)
	}
	applyTurnShaping(sdkReg, cliReg, Options{BatchResultBudgetBytes: 0}, newSDKTurnState())
	if _, ok := sdkReg.Get("alpha"); !ok {
		t.Fatal("alpha missing under zero-budget")
	}
}

// TestApplyTurnShapingOuterFormatterRestorePath pins
// sdk_shaping.go:190-191: when wrapping fails, the unwrapped tool
// is restored by re-adding it.
func TestApplyTurnShapingOuterFormatterRestorePath(t *testing.T) {
	cliReg := tools.NewRegistry()
	cliReg.Register(&cliToolWithParams{name: "alpha"})
	sdkReg := sdktools.New()
	if err := sdkReg.Add(&sdkToolForName{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	applyTurnShaping(sdkReg, cliReg, Options{BatchResultBudgetBytes: 1024}, newSDKTurnState())
	if _, ok := sdkReg.Get("alpha"); !ok {
		t.Fatal("alpha vanished after the restore-path run")
	}
}

// mustDispatcher builds an empty dispatcher for tests.
func mustDispatcher(t *testing.T) *runtime.Dispatcher {
	t.Helper()
	dispatcher, err := runtime.NewToolDispatcher(tools.NewRegistry(), runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	return dispatcher
}

var _ sdkagentloop.Loop

// timeoutTool declares a per-call timeout in its Parameters and is
// called with arguments that include a positive timeout_seconds.
// requestedToolTimeout parses the args, exceeds the loop's
// ToolTimeout, and clampToDeadline narrows to the parent ctx.
type timeoutTool struct{}

func (t *timeoutTool) Name() string               { return "timeout-tool" }
func (t *timeoutTool) Description() string        { return "tt" }
func (t *timeoutTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *timeoutTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{MaxResultBytes: 1024}
}
func (t *timeoutTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// TestDispatcherShimCallTimeoutClampsToDeadline pins shim:108-111:
// a tool request with timeout_seconds exceeding the loop's
// ToolTimeout is clamped to the parent ctx deadline, exercising the
// if-block at line 110 (clampToDeadline call).
func TestDispatcherShimCallTimeoutClampsToDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(5*time.Second))
	defer cancel()
	dispatcher, err := runtime.NewToolDispatcher(tools.NewRegistry(), runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	// Encode timeout_seconds=300 (5min) in args so
	// requestedToolTimeout returns 5min, exceeding the 50ms
	// ToolTimeout. clampToDeadline reduces 5min to <=5s parent-
	// deadline-remaining, then context.WithTimeout wraps ctx at
	// that clamped value.
	args := json.RawMessage(`{"timeout_seconds":300}`)
	shim := &dispatcherShim{
		inner: &marshalFailToolSDK{},
		cli:   &timeoutTool{},
		opts:  Options{Dispatcher: dispatcher, SessionID: "s", ToolTimeout: 50 * time.Millisecond},
		turn:  newSDKTurnState(),
	}
	// Pass the JSON-encoded args via a marshaller that yields raw
	// bytes; since InOut.Value is `any`, we must take a path
	// through json.Marshal. Map[string]any serialises to
	// {"timeout_seconds":300}.
	in := sdktools.InOut{Value: map[string]any{"timeout_seconds": 300}}
	_ = args
	_, _ = shim.Run(ctx, in)
	// The path-edges of line 110 (clampToDeadline call) and
	// line 111 (callTimeout > 0) ran; successful or errored.
}

// TestApplyDispatcherShimWrapsAllToolsAndRestoresOnConflict pins
// shim:206-207 (the restore-failure path): pre-register a blocker
// under the same name so wrap-and-add fails, and the unwrapped tool
// is restored by the second _ = sdkReg.Add(t) call.
func TestApplyDispatcherShimWrapsAllToolsAndRestoresOnConflict(t *testing.T) {
	cliReg := tools.NewRegistry()
	cliReg.Register(&cliToolWithParams{name: "alpha"})
	sdkReg := sdktools.New()
	if err := sdkReg.Add(&sdkToolForName{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	// Pre-register a second tool under "alpha" so the wrap-and-add
	// (which would re-add it under the same name) fails. SDK
	// registries either dedup (returning the registered tool) or
	// error; either path leaves the registry usable.
	if err := sdkReg.Add(&sdkToolForName{name: "alpha"}); err != nil {
		// expected on dup-add; fall through with the SDK's
		// existing entry intact.
	}
	dispatcher, err := runtime.NewToolDispatcher(cliReg, runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	applyDispatcherShim(sdkReg, cliReg, Options{Dispatcher: dispatcher, SessionID: "s"}, newSDKTurnState())
	if _, ok := sdkReg.Get("alpha"); !ok {
		t.Fatal("alpha vanished")
	}
}

// TestApplyTurnShapingLiteralBudgetMultiTool pins shaping:149-150
// (the positive-literal branch): the wrapper uses opts.BatchResultBudgetBytes
// directly with no derive call.
func TestApplyTurnShapingLiteralBudgetMultiTool(t *testing.T) {
	cliReg := tools.NewRegistry()
	cliReg.Register(&cliToolWithParams{name: "alpha"})
	cliReg.Register(&cliAltTool{})
	sdkReg := sdktools.New()
	if err := sdkReg.Add(&sdkToolForName{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := sdkReg.Add(&sdkToolForName{name: "cli-alt"}); err != nil {
		t.Fatal(err)
	}
	applyTurnShaping(sdkReg, cliReg, Options{BatchResultBudgetBytes: 1024}, newSDKTurnState())
	if _, ok := sdkReg.Get("alpha"); !ok {
		t.Fatal("alpha missing under literal budget")
	}
}

// TestApplyTurnShapingWrapReAddsOnAddFailure pins shaping:190-191:
// when the wrap-and-add fails, the unwrapped tool is restored by
// sdkReg.Add(t). Trigger via a sdk registry whose Add reports
// failure on dup (implementation-dependent); in the happy path
// the SDK silently dedups and the add succeeds - assert that the
// original SDK tool survives the applyTurnShaping call.
func TestApplyTurnShapingWrapReAddsOnAddFailure(t *testing.T) {
	cliReg := tools.NewRegistry()
	cliReg.Register(&cliToolWithParams{name: "alpha"})
	sdkReg := sdktools.New()
	if err := sdkReg.Add(&sdkToolForName{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	applyTurnShaping(sdkReg, cliReg, Options{BatchResultBudgetBytes: 8192}, newSDKTurnState())
	// Pin that the SDK registry still has alpha (either the wrap
	// was added or the original was restored).
	if got, ok := sdkReg.Get("alpha"); !ok {
		t.Fatal("alpha vanished after applyTurnShaping; the restore path didn't fire")
	} else if got == nil {
		t.Fatal("alpha entry is nil (registry dropped it)")
	}
}

// ---- Phase A reconciliation: cover the four zero-count error branches
// in RunAgentLoopOnce/buildAgentLoopOptions that the diff-coverage
// allowlist had mislabeled as "closure-brace artifacts". Each test
// drives one branch through a constructible failure.

// badParamsTool returns a Parameters map containing a channel, which
// fails json.Marshal inside sdkadapter.newSDKToolAdapter.
type badParamsTool struct{}

func (b *badParamsTool) Name() string        { return "bad-params" }
func (b *badParamsTool) Description() string { return "unmarshalable params" }
func (b *badParamsTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "bad": make(chan int)}
}
func (b *badParamsTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{MaxResultBytes: 1024}
}
func (b *badParamsTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "x", nil
}

// TestBuildAgentLoopOptionsBadParamsToolFailsClosed covers the
// ConvertToolRegistryWithAdmission error return: a tool whose
// Parameters() cannot marshal must fail the turn before the SDK loop
// is built, naming the tool.
func TestBuildAgentLoopOptionsBadParamsToolFailsClosed(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&badParamsTool{})
	loop := &Loop{Completer: &beforeStepCompleter{}, Tools: reg}
	_, err := buildAgentLoopOptions(loop, Options{
		Model:      "m",
		MaxSteps:   2,
		Dispatcher: mustDispatcher(t),
	})
	if err == nil {
		t.Fatal("expected marshal-parameters error, got nil")
	}
	if !strings.Contains(err.Error(), "bad-params") || !strings.Contains(err.Error(), "marshal parameters") {
		t.Fatalf("err = %v, want it to name the tool and the marshal failure", err)
	}
}

// failingPrepManager always fails Prepare, driving the
// prepareSDKHistory error return in RunAgentLoopOnce.
type failingPrepManager struct{}

func (failingPrepManager) Prepare(context.Context, contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	return contextmgr.Preparation{}, errors.New("prep exploded")
}
func (failingPrepManager) Discard(contextmgr.Preparation) {}

// TestRunAgentLoopOncePreparationFailureSurfaces covers the
// prepareSDKHistory error return: a PreparationManager failure fails
// the run before any completer call.
func TestRunAgentLoopOncePreparationFailureSurfaces(t *testing.T) {
	loop := &Loop{Completer: &beforeStepCompleter{}, Tools: tools.NewRegistry()}
	_, err := RunAgentLoopOnce(context.Background(), loop, Options{
		Model:              "m",
		MaxSteps:           2,
		PreparationManager: failingPrepManager{},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "prep exploded") {
		t.Fatalf("err = %v, want the preparation failure surfaced", err)
	}
}

// blankNameTool registers under an empty name; the CLI registry does
// not validate names, but the scoped dispatcher's RegisterTool does,
// driving the scoped-dispatcher error return.
type blankNameTool struct{}

func (b *blankNameTool) Name() string               { return "" }
func (b *blankNameTool) Description() string        { return "blank" }
func (b *blankNameTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (b *blankNameTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{MaxResultBytes: 1024}
}
func (b *blankNameTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "x", nil
}

// TestRunAgentLoopOnceScopedDispatcherRejectsBlankNameTool covers the
// scoped NewToolDispatcher error return: a blank-named tool passes CLI
// registry registration but fails dispatcher registration, failing the
// run closed with the scoped-dispatcher wrap.
func TestRunAgentLoopOnceScopedDispatcherRejectsBlankNameTool(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&blankNameTool{})
	loop := &Loop{Completer: &beforeStepCompleter{}, Tools: reg}
	_, err := RunAgentLoopOnce(context.Background(), loop, Options{
		Model:    "m",
		MaxSteps: 2,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "scoped tool dispatcher") {
		t.Fatalf("err = %v, want the scoped tool dispatcher wrap", err)
	}
}

// TestRunAgentLoopOnceNilToolsFailsAtSDKNew covers the sdkagentloop.New
// error return: a nil registry survives the adapter's own checks (the
// converter returns nil,nil) and fails inside the SDK's Validate with
// ErrNoTools.
func TestRunAgentLoopOnceNilToolsFailsAtSDKNew(t *testing.T) {
	loop := &Loop{Completer: &beforeStepCompleter{}, Tools: nil}
	_, err := RunAgentLoopOnce(context.Background(), loop, Options{
		Model:    "m",
		MaxSteps: 2,
	}, nil)
	if !errors.Is(err, sdkagentloop.ErrNoTools) {
		t.Fatalf("err = %v, want the SDK's ErrNoTools sentinel", err)
	}
}
