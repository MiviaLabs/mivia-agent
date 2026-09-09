package agent

// A tool call the SDK rejects BEFORE the dispatcher shim runs must reach the
// operator as a failure, never as a call that ran.
//
// The SDK's decodeAndRun fails a call for an unknown tool, a scope denial, an
// argument-schema violation, or an undecodable payload - all of them before
// dispatcherShim.Run, so nothing records an outcome. Each one is a REPORTED
// failure the SDK counts toward Bounds.MaxConsecutiveToolFailures, and three
// in a row stop the turn ("failure spiral bound"). Meanwhile the no-outcome
// fallback in bridgeToolCallEnd read the missing outcome as a dedup-cache hit
// and emitted "completed (duplicate)", which every surface classifies as
// success: the TUI computed OK = true, the NDJSON writer wrote "ok". An
// operator watched three green dispatch_tasks rows and then a turn killed for
// repeated tool failures, with nothing on screen naming the failing calls.
//
// The SDK-level dedup that fallback was written for is not enabled here (the
// host never sets Extensions.DedupWithinTurn), and a dispatcher-level
// duplicate always records its own outcome, so a missing outcome means the
// call never ran.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdkschema "github.com/MiviaLabs/mivia-ai-sdk/schema"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// TestArgumentValidationFailureIsRecordedAsFailed covers the error class that
// killed a real session: a strict tool schema rejects a call inside the SDK in
// microseconds, before the dispatcher shim can record anything. The reporter
// hook consulted only the two unknown-tool sentinels and returned early for
// everything else, recording nothing. (dispatch_tasks was the tool that hit
// it; its schema has since been relaxed, but any tool publishing
// additionalProperties:false or an enum still takes this path.)
func TestArgumentValidationFailureIsRecordedAsFailed(t *testing.T) {
	turn := newSDKTurnState()
	reporter := sdkToolCallErrorReporter(Options{}, turn)

	// A REAL schema rejection, produced the way decodeAndRun produces one: a
	// schema with additionalProperties:false fails a stray field in compiled
	// validation, before the tool is ever invoked.
	compiled, err := sdkschema.Compile([]byte(`{"type":"object","properties":{"tasks":{"type":"array"}},"required":["tasks"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"tasks":[],"parallel":true}`)
	verr := compiled.Validate(args)
	if verr == nil {
		t.Fatal("fixture schema accepted the stray field; the test proves nothing")
	}
	call := sdkshape.ToolCall{ID: "call-1", Name: "dispatch_tasks", Arguments: args}
	runErr := fmt.Errorf("agentloop: tool call call-1: %w: %w", sdkagentloop.ErrArgumentValidation, verr)

	if _, err := reporter(context.Background(), call, runErr); err != nil {
		t.Fatalf("reporter returned a hard error: %v", err)
	}

	outcome := turn.takeToolCallOutcome("call-1")
	if outcome == nil {
		t.Fatal("no outcome recorded for a rejected call, so the loop falls back to " +
			"\"completed (duplicate)\" and a call that never ran renders as a success")
	}
	if !outcome.failed {
		t.Error("outcome.failed = false; the SDK counted this call toward the failure spiral")
	}
	if detail := sdkToolEndDetail(*outcome); !strings.HasPrefix(detail, "failed") {
		t.Errorf("detail = %q; every surface classifies on this prefix", detail)
	}
	// The operator body must carry the SDK's own corrective rendering, the
	// same text the model is answered with - not a bare "validation failed".
	if want := sdkagentloop.ToolErrorPrefix + sdkschema.Corrective(runErr); outcome.body != want {
		t.Errorf("body = %q, want %q (the body the model was given)", outcome.body, want)
	}
	if !strings.Contains(outcome.body, "parallel") {
		t.Errorf("body = %q, want the offending field named", outcome.body)
	}
}

// TestScopeDenialIsRecordedAsFailed pins the second pre-shim class: a tool
// the run's scope refuses. It is not an unknown name, so it took the same
// silent early return.
func TestScopeDenialIsRecordedAsFailed(t *testing.T) {
	turn := newSDKTurnState()
	reporter := sdkToolCallErrorReporter(Options{}, turn)

	call := sdkshape.ToolCall{ID: "call-2", Name: "run_command", Arguments: json.RawMessage(`{}`)}
	if _, err := reporter(context.Background(), call, fmt.Errorf("agentloop: tool call call-2: %w", sdktools.ErrScopeDenied)); err != nil {
		t.Fatalf("reporter returned a hard error: %v", err)
	}

	outcome := turn.takeToolCallOutcome("call-2")
	if outcome == nil {
		t.Fatal("a scope-denied call recorded no outcome; it renders as a completed duplicate")
	}
	if !outcome.failed {
		t.Error("outcome.failed = false for a scope denial")
	}
}

// TestUnknownToolReportingIsUnchanged holds the behaviour the fix must not
// disturb: the two sentinels keep the legacy denial text and their own
// recorded outcome.
func TestUnknownToolReportingIsUnchanged(t *testing.T) {
	turn := newSDKTurnState()
	reporter := sdkToolCallErrorReporter(Options{}, turn)

	call := sdkshape.ToolCall{ID: "call-3", Name: "ghost_tool", Arguments: json.RawMessage(`{}`)}
	msg, err := reporter(context.Background(), call, fmt.Errorf("wrap: %w", sdktools.ErrUnknownName))
	if err != nil {
		t.Fatalf("reporter: %v", err)
	}
	if !strings.Contains(msg.Content, "is not available to this agent") {
		t.Fatalf("Content = %q, want the legacy not-available denial", msg.Content)
	}
	outcome := turn.takeToolCallOutcome("call-3")
	if outcome == nil || !outcome.failed {
		t.Fatalf("outcome = %+v, want a recorded failure", outcome)
	}
}

// TestBridgeToolCallEndWithNoOutcomeReportsFailure pins the fallback itself.
// The SDK fires ToolCallEnd on every return path of runOneToolCall, including
// the ones that never reach the shim; with no outcome to render, the event
// must not claim the call completed.
func TestBridgeToolCallEndWithNoOutcomeReportsFailure(t *testing.T) {
	var captured []Event
	opts := Options{OnEvent: func(e Event) { captured = append(captured, e) }}
	ctx := sdkagentloop.WithToolCall(context.Background(), sdkshape.ToolCall{ID: "call-4", Name: "dispatch_tasks"})

	bridgeToolCallEnd(opts, newSDKTurnState(), ctx)

	if len(captured) != 1 {
		t.Fatalf("captured %d events, want exactly 1: %+v", len(captured), captured)
	}
	if got := captured[0].Detail; !strings.HasPrefix(got, "failed") {
		t.Fatalf("Detail = %q; a call with no recorded outcome never ran, and "+
			"reporting it as completed is how a rejected call rendered green", got)
	}
}

// envelopeFailureTool answers the way dispatch_tasks answers a whole-batch
// rejection: a JSON error envelope with a NIL Go error, so the caller keeps
// the run_id/hint fields a hard error would discard.
type envelopeFailureTool struct{}

func (envelopeFailureTool) Name() string               { return "dispatch_tasks" }
func (envelopeFailureTool) Description() string        { return "envelope failure fixture" }
func (envelopeFailureTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (envelopeFailureTool) Execute(context.Context, json.RawMessage) (string, error) {
	return `{"error":"caller context already expired; no tasks were started","status":"canceled"}`, nil
}

// TestEnvelopeFailureIsRecordedAsFailed pins the third disagreement: the shim
// judges the failure-spiral counter on r.Err OR the body scan, but recorded
// the OPERATOR outcome on r.Err alone. A dispatch_tasks error envelope fed
// the host's loop breaker while every viewer was told the call completed.
func TestEnvelopeFailureIsRecordedAsFailed(t *testing.T) {
	reg := tools.NewRegistry()
	tool := envelopeFailureTool{}
	reg.Register(tool)
	dispatcher, err := runtime.NewToolDispatcher(reg, runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)

	turn := newSDKTurnState()
	shim := &dispatcherShim{
		inner: &sdkToolForName{name: "dispatch_tasks"},
		cli:   tool,
		opts:  Options{Dispatcher: dispatcher, SessionID: "s"},
		turn:  turn,
	}
	ctx := sdkagentloop.WithToolCall(context.Background(), sdkshape.ToolCall{ID: "call-5", Name: "dispatch_tasks"})
	if _, err := shim.Run(ctx, sdktools.InOut{Value: map[string]any{}}); err != nil {
		t.Fatal(err)
	}

	outcome := turn.takeToolCallOutcome("call-5")
	if outcome == nil {
		t.Fatal("no outcome recorded")
	}
	if !outcome.failed {
		t.Error("outcome.failed = false for a whole-batch rejection envelope")
	}
	if detail := sdkToolEndDetail(*outcome); !strings.HasPrefix(detail, "failed") {
		t.Errorf("detail = %q, want the failed vocabulary", detail)
	}
}

// TestRecordPreShimFailureGuards drives the recorder's degenerate inputs. The
// name fallback is the load-bearing one: call.ID goes empty when a provider
// stream sends the tool-call NAME delta before, or without, the ID delta, and
// an outcome recorded under the wrong key is invisible to bridgeToolCallEnd,
// which looks up the same fallback.
func TestRecordPreShimFailureGuards(t *testing.T) {
	boom := errors.New("boom")

	t.Run("id empty falls back to the name", func(t *testing.T) {
		turn := newSDKTurnState()
		recordPreShimFailure(turn, sdkshape.ToolCall{Name: "dispatch_tasks"}, boom)
		outcome := turn.takeToolCallOutcome("dispatch_tasks")
		if outcome == nil || !outcome.failed {
			t.Fatalf("outcome = %+v, want a failure recorded under the name key", outcome)
		}
	})

	t.Run("an unidentifiable call records nothing anywhere", func(t *testing.T) {
		// takeToolCallOutcome("") answers nil unconditionally, so asking it
		// proves nothing. Assert instead that the map stayed EMPTY: a call
		// with no ID and no name must not leave an outcome under any key, or
		// bridgeToolCallEnd would render a tool_end it cannot attribute.
		turn := newSDKTurnState()
		recordPreShimFailure(turn, sdkshape.ToolCall{}, boom)
		turn.toolMu.Lock()
		stored := len(turn.toolOutcomes)
		turn.toolMu.Unlock()
		if stored != 0 {
			t.Fatalf("recorded %d outcome(s) for a call with no ID and no name", stored)
		}
	})

	t.Run("nil turn and nil error are no-ops", func(t *testing.T) {
		// Both are defensive: the SDK only fires the hook with a non-nil
		// error, and every production caller threads a turn. Neither may
		// panic.
		recordPreShimFailure(nil, sdkshape.ToolCall{ID: "c"}, boom)
		turn := newSDKTurnState()
		recordPreShimFailure(turn, sdkshape.ToolCall{ID: "c"}, nil)
		if outcome := turn.takeToolCallOutcome("c"); outcome != nil {
			t.Fatalf("outcome = %+v, want nothing recorded without an error", outcome)
		}
	})
}
