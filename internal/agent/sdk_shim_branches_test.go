package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// bareSDKTool implements only sdktools.Tool: no SchemaTool face. It
// exists to exercise the wrappers' non-SchemaTool fallbacks.
type bareSDKTool struct {
	body string
	err  error
	// nonString makes Run return a non-string Out value.
	nonString bool
}

func (t *bareSDKTool) Name() string { return "bare_tool" }
func (t *bareSDKTool) Run(ctx context.Context, in sdktools.InOut) (sdktools.Out, error) {
	if t.err != nil {
		return sdktools.Out{}, t.err
	}
	if t.nonString {
		return sdktools.Out{Value: 42}, nil
	}
	return sdktools.Out{Value: t.body}, nil
}

// cliBareTool implements the CLI tools.Tool without a schema.
type cliBareTool struct{ body string }

func (t *cliBareTool) Name() string               { return "cli_bare" }
func (t *cliBareTool) Description() string        { return "bare" }
func (t *cliBareTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *cliBareTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.body, nil
}

// newTurnShapeWrapperForTest wraps one inner tool with the turn
// shaping wrapper exactly as applyTurnShaping does.
func newTurnShapeWrapperForTest(t *testing.T, inner sdktools.Tool, budget int) *turnShapeWrapper {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(&cliBareTool{})
	sdkReg := sdktools.New()
	if err := sdkReg.Add(inner); err != nil {
		t.Fatal(err)
	}
	counter := newTurnShapeCounter()
	return &turnShapeWrapper{
		inner: inner, budget: budget, counter: counter,
		env: newShapeEnv(nil, "test-session"), toolName: inner.Name(),
		turn: newSDKTurnState(),
	}
}

// TestTurnShapeWrapperPassesThroughInnerErrorAndNonString covers the
// wrapper's two early returns: an errored inner Run passes the partial
// Out and the error through unchanged, and a non-string Out value
// passes through with no shaping.
func TestTurnShapeWrapperPassesThroughInnerErrorAndNonString(t *testing.T) {
	w := newTurnShapeWrapperForTest(t, &bareSDKTool{err: errors.New("boom")}, 1024)
	if _, err := w.Run(context.Background(), sdktools.InOut{}); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
	// A non-SchemaTool inner also exercises the wrapper's fallback
	// faces for ParameterSchema and DecodeArguments.
	if got := w.ParameterSchema(); got != nil {
		t.Fatalf("ParameterSchema = %v, want nil for a non-SchemaTool inner", got)
	}
	in, err := w.DecodeArguments([]byte(`{"a":1}`))
	if err != nil || string(in.Value.([]byte)) != `{"a":1}` {
		t.Fatalf("DecodeArguments = %v, %v; want byte pass-through", in, err)
	}
	w2 := newTurnShapeWrapperForTest(t, &bareSDKTool{nonString: true}, 1024)
	out, err := w2.Run(context.Background(), sdktools.InOut{})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := out.Value.(int); !ok || v != 42 {
		t.Fatalf("Out.Value = %v, want the untouched 42", out.Value)
	}
}

// TestTurnShapeWrapperPass1MissDegradesFromExecutedBody covers the
// take-miss path: an inner tool that is NOT the dispatcher shim left
// no pass-1 record, so the wrapper shapes the executed body as the
// original (single-pass, no ref).
func TestTurnShapeWrapperPass1MissDegradesFromExecutedBody(t *testing.T) {
	big := strings.Repeat("x", 64<<10)
	w := newTurnShapeWrapperForTest(t, &bareSDKTool{body: big}, 8<<10)
	out, err := w.Run(context.Background(), sdktools.InOut{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := out.Value.(string)
	if len(body) >= len(big) {
		t.Fatalf("body = %d bytes, want a degrade below the original %d", len(body), len(big))
	}
	// A second call sees the budget spent (F6: a budget-tier degrade
	// spends the rest of the turn), covering the charge branch.
	if _, err := w.Run(context.Background(), sdktools.InOut{}); err != nil {
		t.Fatal(err)
	}
}

// TestApplyRefOnlyShimSkipsNonSchemaTool covers the shim's defensive
// skip: a registry tool without the SchemaTool face keeps its
// unwrapped form instead of being dropped from the offered set.
func TestApplyRefOnlyShimSkipsNonSchemaTool(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&cliBareTool{})
	sdkReg := sdktools.New()
	bare := &bareSDKTool{body: "keep"}
	if err := sdkReg.Add(bare); err != nil {
		t.Fatal(err)
	}
	applyRefOnlyShim(sdkReg, reg, []string{"bare_tool"}, remainder.NewSpool(remainder.NewMemoryStore()), BatchDegradeFloorBytes, "sess", nil)
	got, ok := sdkReg.Get("bare_tool")
	if !ok {
		t.Fatal("non-SchemaTool vanished from the registry")
	}
	if _, isShim := got.(*refOnlyShim); isShim {
		t.Fatal("non-SchemaTool was wrapped by the ref-only shim")
	}
}

// steerBlockCompleter blocks in ChatTurn until ctx is canceled, then
// returns the ctx error - the shape a soft steer cancel produces.
type steerBlockCompleter struct{ unblock chan struct{} }

func (c *steerBlockCompleter) Name() string { return "steer-block" }
func (c *steerBlockCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}
func (c *steerBlockCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *steerBlockCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	_, err := c.Chat(ctx, req)
	return nil, err
}

// TestRunOnceSDKSteeredStopMapsToErrSteerInterrupt covers the
// dispatcher's StopSteered mapping on the SDK backend: a bare
// InterruptCh (no mailbox gate) is an explicit interrupt, so the run
// stops steered and Run reports errSteerInterrupt.
func TestRunOnceSDKSteeredStopMapsToErrSteerInterrupt(t *testing.T) {
	unblock := make(chan struct{})
	loop := &Loop{Completer: &steerBlockCompleter{unblock: unblock}, Tools: tools.NewRegistry()}
	interrupted := make(chan struct{})
	opts := Options{
		Model:    "m",
		MaxSteps: 2,
		InterruptCh: func() <-chan struct{} {
			return interrupted
		},
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := loop.Run(context.Background(), "hi", opts)
		errCh <- err
	}()
	// Give the run time to enter the completer, then fire the
	// interrupt and unblock the completer body.
	time.Sleep(100 * time.Millisecond)
	close(interrupted)
	select {
	case err := <-errCh:
		if !errors.Is(err, errSteerInterrupt) {
			t.Fatalf("err = %v, want errSteerInterrupt", err)
		}
	case <-time.After(5 * time.Second):
		// The completer only returns once canceled; nudge it so the
		// test fails instead of hanging.
		close(unblock)
		t.Fatal("run never returned")
	}
}

// TestTurnShapeWrapperClampsOverspentBudget covers the remaining<0
// clamp: a counter already charged past the budget must not hand the
// degrade tiers a negative allowance.
func TestTurnShapeWrapperClampsOverspentBudget(t *testing.T) {
	w := newTurnShapeWrapperForTest(t, &bareSDKTool{body: "small"}, 1024)
	w.counter.charged = 4096
	out, err := w.Run(context.Background(), sdktools.InOut{})
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := out.Value.(string); body != "small" {
		t.Fatalf("body = %q, want the tiny body untouched under a spent budget", body)
	}
}

// TestTurnShapeWrapperReattachesPass1HookContext covers the hit path's
// hook-context re-attach: the dispatcher shim stored pass-1 parts
// whose hook context rides ABOVE the shaping, never inside it.
func TestTurnShapeWrapperReattachesPass1HookContext(t *testing.T) {
	delivered := appendHookContext("body-bytes", "formatter touched 1 file")
	w := newTurnShapeWrapperForTest(t, &bareSDKTool{body: delivered}, 64<<10)
	w.turn.pass1.store("call_1", resultParts{
		cappedBody:  "body-bytes",
		hookContext: "formatter touched 1 file",
		totalN:      len("body-bytes"),
		toolName:    "bare_tool",
	})
	ctx := toolcallctx.WithToolCall(context.Background(), sdkshape.ToolCall{ID: "call_1", Name: "bare_tool"})
	out, err := w.Run(ctx, sdktools.InOut{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := out.Value.(string)
	if !strings.Contains(body, "body-bytes") || !strings.Contains(body, "formatter touched 1 file") {
		t.Fatalf("body = %q, want result plus re-attached hook context", body)
	}
}
