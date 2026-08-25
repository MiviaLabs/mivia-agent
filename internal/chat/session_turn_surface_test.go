package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestScopedTurnAdvertisesTheSessionSnapshot pins plan tools-advertising/01's
// skill-scope decision: a scoped turn (TurnOptions non-nil, the skill-turn
// path) narrows EXECUTION (Registry/Dispatcher come from turn.Tools/turn.
// Dispatcher when set) but still advertises the session's pinned snapshot,
// not the narrower scoped registry - skill activation mid-turn must not
// change the advertised array.
func TestScopedTurnAdvertisesTheSessionSnapshot(t *testing.T) {
	s := prefixResetSession(t)

	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "read_file"})
	full.Register(fixedBodyTool{name: "grep"})
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	scoped := tools.NewRegistry()
	scoped.Register(fixedBodyTool{name: "read_file"})
	turn := &TurnOptions{Tools: scoped}

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, turn)
	surf := opts.Surface()

	if surf.Registry != scoped {
		t.Fatalf("scoped turn execution registry = %v, want the turn-scoped registry", surf.Registry)
	}
	wantAdvertised := full.OpenAITools()
	if len(surf.ToolSpecs) != len(wantAdvertised) {
		t.Fatalf("scoped turn advertised %d tools, want %d (the full session snapshot, not the %d-tool scoped registry)",
			len(surf.ToolSpecs), len(wantAdvertised), len(scoped.List()))
	}

	// A root (unscoped) turn advertises the identical snapshot.
	var rootOpts agent.Options
	s.wireStepBoundaryAdmission(&rootOpts, nil)
	rootSurf := rootOpts.Surface()
	if len(rootSurf.ToolSpecs) != len(surf.ToolSpecs) {
		t.Fatalf("root and scoped turns advertised different counts: %d vs %d", len(rootSurf.ToolSpecs), len(surf.ToolSpecs))
	}
}

// TestUnadmittedAdvertisedToolAutoStagesAndRefuses pins the model-behavior
// risk resolution for plan tools-advertising/01: advertising the whole
// admissible union means a deferred tool now LOOKS callable before load_tools
// ever ran for it. A call to such a tool must be refused (never executed) but
// also auto-staged, so the model recovers in exactly one extra step instead
// of learning to call load_tools first. A call to a name outside the
// advertised snapshot (hallucinated) must fall through unrecognized.
func TestUnadmittedAdvertisedToolAutoStagesAndRefuses(t *testing.T) {
	s := prefixResetSession(t)

	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "read_file"})
	full.Register(fixedBodyTool{name: "grep"})
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	// A nonzero live turn id, matching a real in-flight turn - the exact
	// state a real UnadmittedToolHandler call runs under.
	s.mu.Lock()
	s.turnID = 7
	s.mu.Unlock()

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)
	if opts.UnadmittedToolHandler == nil {
		t.Fatal("UnadmittedToolHandler not wired")
	}

	// A bare context.Background(), exactly as executeToolTask's "tool not
	// found" branch supplies (it never reaches Dispatcher.Invoke, so no
	// runtime.Caller is ever stamped onto task.callCtx at this call site) -
	// TurnIDFromContext(ctx) would return (0, false) here.
	result := opts.UnadmittedToolHandler(context.Background(), "grep", nil)
	if !result.Handled {
		t.Fatal("advertised-but-unadmitted tool was not recognized")
	}
	if result.Ran {
		t.Fatal("no dispatcher/resolver wired in this test - the call must fall back to the staged denial, not report Ran")
	}
	if result.Content == "" {
		t.Fatal("refusal message is empty")
	}
	stage, has := s.PendingAdmission()
	if !has {
		t.Fatal("the call did not auto-stage grep for admission")
	}
	if len(stage.Names) != 1 || stage.Names[0] != "grep" {
		t.Fatalf("staged names = %v, want [grep]", stage.Names)
	}
	// The stage must be owned by the session's REAL live turn (7), never 0:
	// turn 0 means "no owning turn" to StageToolAdmission, so
	// dropPendingAdmissionForTurn could never discard this stage if turn 7
	// later errors or is superseded - it would be pinned forever regardless
	// of that turn's outcome.
	if _, has7 := stage.nameOwners[0][7]; !has7 {
		t.Fatalf("staged owners = %v, want the stage owned by turn 7 (not turn 0)", stage.nameOwners[0])
	}
	if _, has0 := stage.nameOwners[0][0]; has0 {
		t.Fatal("staged owners must not include turn 0 (a stage that no turn boundary can ever drop)")
	}

	// A hallucinated name outside the advertised snapshot is not recognized:
	// the caller falls through to the generic denial, and nothing is staged.
	if result := opts.UnadmittedToolHandler(context.Background(), "no_such_tool", nil); result.Handled {
		t.Fatal("a name outside the advertised snapshot must not be recognized")
	}
}

// TestUnadmittedToolHandlerDoesNotAutoStageForScopedTurns pins that a scoped
// skill turn does not own the session's admission state: an unadmitted call
// there is refused but never staged, matching the Surface hook's own
// turn == nil gate for PublishPendingAdmissionAtStepBoundary.
func TestUnadmittedToolHandlerDoesNotAutoStageForScopedTurns(t *testing.T) {
	s := prefixResetSession(t)

	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "read_file"})
	full.Register(fixedBodyTool{name: "grep"})
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	scoped := tools.NewRegistry()
	scoped.Register(fixedBodyTool{name: "read_file"})
	turn := &TurnOptions{Tools: scoped}

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, turn)
	result := opts.UnadmittedToolHandler(context.Background(), "grep", nil)
	if !result.Handled {
		t.Fatal("advertised tool must still be recognized (refused) in a scoped turn")
	}
	if result.Ran {
		t.Fatal("a scoped turn must never execute synchronously - only refuse")
	}
	if _, has := s.PendingAdmission(); has {
		t.Fatal("a scoped turn must not auto-stage into the session's admission state")
	}
}

// TestUnadmittedToolHandlerServesTheCallSynchronously is the regression test
// for the fix: when a dispatcher AND a ToolBaseResolver are wired (the real
// session-construction shape - see cliagents' scopeAttachedToolSurface), a
// call to a deferred-but-authorized tool must return the tool's REAL result
// with Ran=true, not the "queued to load... retry next turn" denial text.
// The tool is still auto-staged too, so it becomes natively admitted at the
// next step boundary exactly as before - this call just no longer has to
// wait for that to get an answer.
func TestUnadmittedToolHandlerServesTheCallSynchronously(t *testing.T) {
	s := prefixResetSession(t)

	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "read_file"})
	full.Register(fixedBodyTool{name: "grep", body: "main.go:1:package main"})
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	dispatcher := runtime.New(runtime.Policy{})
	t.Cleanup(dispatcher.Close)
	s.SetDispatcher(dispatcher)
	s.ToolBaseResolver = func() *tools.Registry { return full }

	s.mu.Lock()
	s.turnID = 7
	s.mu.Unlock()

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)

	result := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{"pattern":"package"}`))
	if !result.Handled {
		t.Fatal("advertised-but-unadmitted tool was not recognized")
	}
	if !result.Ran {
		t.Fatalf("expected the call to be served synchronously (Ran=true), got denial: %q", result.Content)
	}
	if result.Content != "main.go:1:package main" {
		t.Fatalf("Content = %q, want the tool's real, unprefixed result", result.Content)
	}
	if strings.Contains(result.Content, "error:") || strings.Contains(result.Content, "next step") {
		t.Fatalf("synchronous result must carry no denial/error framing, got %q", result.Content)
	}

	// Still auto-staged, so it becomes natively admitted at the next step
	// boundary - the synchronous path is additive, not a replacement for it.
	stage, has := s.PendingAdmission()
	if !has || len(stage.Names) != 1 || stage.Names[0] != "grep" {
		t.Fatalf("expected grep still staged for native admission, got %+v (has=%v)", stage, has)
	}

	// A second call in the same turn dedups against the first (same turn,
	// same tool, same input) rather than re-running the tool.
	result2 := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{"pattern":"package"}`))
	if !result2.Ran || result2.Content != result.Content {
		t.Fatalf("second identical call = %+v, want the same successful result via dedup", result2)
	}
}

// TestUnadmittedToolHandlerFallsBackWithoutWiring pins the degrade path: no
// dispatcher or no resolver wired (a test harness, or a session with no
// agent state) must never panic and must fall back to the staged-only
// denial exactly as before the synchronous path existed.
func TestUnadmittedToolHandlerFallsBackWithoutWiring(t *testing.T) {
	s := prefixResetSession(t)

	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "grep", body: "should never run"})
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())
	// Deliberately leave s.ToolBaseResolver nil and s.binding.Dispatcher nil.

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)
	result := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))
	if !result.Handled {
		t.Fatal("advertised tool must still be recognized")
	}
	if result.Ran {
		t.Fatal("must not report Ran with no dispatcher/resolver wired")
	}
	if !strings.Contains(result.Content, "next step") {
		t.Fatalf("expected the staged-denial fallback text, got %q", result.Content)
	}
}

// TestUnadmittedToolHandlerCapsTheSynchronousResult pins the review finding:
// a deferred tool's synchronous result must honor s.MaxToolResultChars, the
// same session-level budget every other tool call is capped by - the
// dispatcher's own output-ceiling safety floor (256 KiB) is far too loose to
// stand in for an operator's configured budget on its own.
func TestUnadmittedToolHandlerCapsTheSynchronousResult(t *testing.T) {
	s := prefixResetSession(t)
	s.MaxToolResultChars = 16

	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "grep", body: strings.Repeat("x", 1000)})
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	dispatcher := runtime.New(runtime.Policy{})
	t.Cleanup(dispatcher.Close)
	s.SetDispatcher(dispatcher)
	s.ToolBaseResolver = func() *tools.Registry { return full }

	s.mu.Lock()
	s.turnID = 1
	s.mu.Unlock()

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)
	result := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))
	if !result.Ran {
		t.Fatalf("expected the call to run, got denial: %q", result.Content)
	}
	if len(result.Content) > 200 {
		t.Fatalf("synchronous result was not capped to s.MaxToolResultChars (16): got %d bytes", len(result.Content))
	}
}
