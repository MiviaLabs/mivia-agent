package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
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

	// A second identical call in the same turn returns the same result. This
	// tool is READ-class and fixed-body, so it re-runs (its capability says
	// reads always execute fresh) and the bodies match either way - the
	// assertion cannot tell dedup from a re-run and does not need to. The
	// comment here used to claim dedup; TestDeferredToolSuppressesHookRunsOn
	// Duplicate is where a genuine duplicate is proven, with a counter.
	result2 := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{"pattern":"package"}`))
	if !result2.Ran || result2.Content != result.Content {
		t.Fatalf("second identical call = %+v, want the same successful result", result2)
	}
}

// A same-turn duplicate must not re-execute the tool (proven by a call
// counter, not by content equality - the earlier assertion would pass
// identically for a genuine re-run of a fixed-body tool) and must not
// report a hook run for a hook that did not fire on this call: a
// dedup-served duplicate is answered with the OWNER's HookRuns (DC-9), so
// reporting them here would show a hook firing that never fired.
func TestDeferredToolSuppressesHookRunsOnDuplicate(t *testing.T) {
	s := prefixResetSession(t)
	// WRITE-class on purpose. Only Write/External calls dedup - "ExecutionRead
	// calls always execute fresh" - so a read-class fixture could not produce
	// the duplicate this test exists to describe. It used to use one, and
	// passed only because the deferred path dropped the tool's own dedup
	// declaration.
	tool := &countingWriteTool{name: "grep"}
	// A Write-class tool also has to clear approval, which the read-class
	// fixture never did. This test is about the duplicate contract, not the
	// approval one, so the policy is auto.
	s.ApprovalPolicy = config.ApprovalPolicyAuto
	full := tools.NewRegistry()
	full.Register(tool)
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	pre := func(context.Context, runtime.Request) runtime.HookVerdict {
		return runtime.HookVerdict{Runs: []runtime.HookRun{{Event: "PreToolUse", Program: "guard.sh"}}}
	}
	dispatcher := runtime.New(runtime.Policy{PreInvokeHook: pre})
	t.Cleanup(dispatcher.Close)
	s.SetDispatcher(dispatcher)
	s.ToolBaseResolver = func() *tools.Registry { return full }
	s.mu.Lock()
	s.turnID = 7
	s.mu.Unlock()

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)

	first := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))
	if !first.Ran || len(first.HookRuns) != 1 {
		t.Fatalf("owner call: Ran=%v HookRuns=%+v, want Ran=true and 1 hook run", first.Ran, first.HookRuns)
	}

	second := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))
	if !second.Ran || second.Content != first.Content {
		t.Fatalf("second identical call = %+v, want the same successful result via dedup", second)
	}
	if calls := tool.runs.Load(); calls != 1 {
		t.Fatalf("the tool executed %d times, want exactly 1 (the duplicate must not re-run it)", calls)
	}
	if len(second.HookRuns) != 0 {
		t.Fatalf("a dedup-served duplicate must report no hook runs, got %+v", second.HookRuns)
	}
}

// A PostToolUse hook's advisory text must reach the model, not just the
// operator's transcript - parity with dispatcherShim.Run's hookContext
// threading. runDeferredToolNow returns it unframed; framing happens at
// the internal/agent call site that has appendHookContext in scope.
func TestDeferredToolReturnsHookContextForTheModel(t *testing.T) {
	s := prefixResetSession(t)
	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "grep", body: "main.go:1:package main"})
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	post := func(context.Context, runtime.Request, runtime.Result) runtime.HookResult {
		return runtime.HookResult{Context: "gofmt rewrote 2 files"}
	}
	dispatcher := runtime.New(runtime.Policy{PostInvokeHook: post})
	t.Cleanup(dispatcher.Close)
	s.SetDispatcher(dispatcher)
	s.ToolBaseResolver = func() *tools.Registry { return full }
	s.mu.Lock()
	s.turnID = 7
	s.mu.Unlock()

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)
	result := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))

	if !result.Ran {
		t.Fatalf("expected the call to be served, got denial: %q", result.Content)
	}
	if result.HookContext != "gofmt rewrote 2 files" {
		t.Fatalf("HookContext = %q, want the PostToolUse hook's advisory text", result.HookContext)
	}
	if result.Content != "main.go:1:package main" {
		t.Fatalf("Content = %q, want the unframed tool result - framing happens at the agent call site", result.Content)
	}
}

// A dedup-served duplicate's HookRuns must be nil (the hook did not run for
// THIS call), but its HookContext is still the owner's real advisory text -
// DC-9 answers a duplicate with the owner's post-hook Result, and
// dispatcherShim.Run appends that same context for its own duplicates.
func TestDeferredToolReportsHookContextForADuplicate(t *testing.T) {
	s := prefixResetSession(t)
	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "grep", body: "ok"})
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	post := func(context.Context, runtime.Request, runtime.Result) runtime.HookResult {
		return runtime.HookResult{Context: "fmt.sh ran"}
	}
	dispatcher := runtime.New(runtime.Policy{PostInvokeHook: post})
	t.Cleanup(dispatcher.Close)
	s.SetDispatcher(dispatcher)
	s.ToolBaseResolver = func() *tools.Registry { return full }
	s.mu.Lock()
	s.turnID = 7
	s.mu.Unlock()

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)

	first := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))
	if !first.Ran || first.HookContext != "fmt.sh ran" {
		t.Fatalf("owner call: Ran=%v HookContext=%q", first.Ran, first.HookContext)
	}
	second := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))
	if !second.Ran {
		t.Fatalf("second identical call = %+v, want served via dedup", second)
	}
	if len(second.HookRuns) != 0 {
		t.Fatalf("a dedup-served duplicate must report no hook RUNS, got %+v", second.HookRuns)
	}
	if second.HookContext != "fmt.sh ran" {
		t.Fatalf("a dedup-served duplicate must still carry the owner's hook CONTEXT, got %q", second.HookContext)
	}
}

// A PreToolUse block is the case an operator most needs to see: the call
// never ran, but the denying hook did. Today's model-facing text on this
// path is unchanged (the generic "queued to load... retry" message) - see
// runDeferredToolNow's doc comment for why that is a deliberate half-fix.
func TestDeferredToolReportsAPreToolUseBlock(t *testing.T) {
	s := prefixResetSession(t)
	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "grep", body: "should never run"})
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	pre := func(context.Context, runtime.Request) runtime.HookVerdict {
		return runtime.HookVerdict{Denied: true, Reason: "policy forbids this", Runs: []runtime.HookRun{
			{Event: "PreToolUse", Program: "guard.sh", Denied: true, Output: "policy forbids this"},
		}}
	}
	dispatcher := runtime.New(runtime.Policy{PreInvokeHook: pre})
	t.Cleanup(dispatcher.Close)
	s.SetDispatcher(dispatcher)
	s.ToolBaseResolver = func() *tools.Registry { return full }
	s.mu.Lock()
	s.turnID = 7
	s.mu.Unlock()

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)

	result := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))
	// INVERTED, deliberately. This asserted `!result.Ran` and that the content
	// still said "next step" - the "deliberate half-fix" its own message named.
	// The half-fix was the bug: !Ran routes the model to "authorized but was
	// not yet loaded [...] retry the call on your next step", so a call the
	// operator's own hook refused was reported as a loading problem, with an
	// instruction to retry it. Ran now means "reached the dispatcher" and
	// Failed carries the outcome, so the block can be reported as itself.
	if !result.Ran {
		t.Fatalf("a blocked call must report Ran (it reached the dispatcher) so "+
			"the caller does not fall back to the retry message, got %+v", result)
	}
	if !result.Failed {
		t.Fatalf("a blocked call must be marked Failed, or the operator's own "+
			"hook refusal renders as a completed call, got %+v", result)
	}
	if len(result.HookRuns) != 1 || !result.HookRuns[0].Denied {
		t.Fatalf("the blocking run must still be reported: %+v", result.HookRuns)
	}
	if strings.Contains(result.Content, "next step") {
		t.Fatalf("the model is still told to retry a call its operator blocked: %q", result.Content)
	}
	if !strings.Contains(result.Content, "policy forbids this") {
		t.Fatalf("the model is not told the hook's reason: %q", result.Content)
	}
}

// A hook can fire successfully while the TOOL's own execution still fails -
// a distinct case from a PreToolUse block (which never reaches the tool at
// all). The hook run must still be reported. Proven by
// an execution-reached flag (not just an error path, which any
// pre-execute dispatcher failure would also satisfy) and by asserting
// Denied==false on the reported run (a PreToolUse block's run IS denied -
// this is the one cheap assertion that separates the two cases).
func TestDeferredToolReportsHookRunsWhenToolItselfFails(t *testing.T) {
	s := prefixResetSession(t)
	tool := &failingTool{name: "grep"}
	full := tools.NewRegistry()
	full.Register(tool)
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	pre := func(context.Context, runtime.Request) runtime.HookVerdict {
		return runtime.HookVerdict{Runs: []runtime.HookRun{{Event: "PreToolUse", Program: "guard.sh"}}}
	}
	dispatcher := runtime.New(runtime.Policy{PreInvokeHook: pre})
	t.Cleanup(dispatcher.Close)
	s.SetDispatcher(dispatcher)
	s.ToolBaseResolver = func() *tools.Registry { return full }
	s.mu.Lock()
	s.turnID = 7
	s.mu.Unlock()

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)

	result := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))
	// INVERTED, deliberately - see TestDeferredToolReportsAPreToolUseBlock.
	// "A failing tool execution must not report Ran" was the bug stated as a
	// requirement: it forced a tool that HAD executed onto the "not yet
	// loaded - retry next step" message, and the retry re-ran every side
	// effect the first call had already landed. Covered end-to-end by
	// TestADeferredToolThatRanAndFailedIsNotReportedAsUnloaded.
	if !result.Ran {
		t.Fatalf("a tool that executed must report Ran, got %+v", result)
	}
	if !result.Failed {
		t.Fatalf("a tool that executed and errored must be marked Failed, got %+v", result)
	}
	if !tool.reached.Load() {
		t.Fatal("the tool's own Execute was never reached - this test would also pass for a pre-execute dispatcher failure, which is not what it claims to cover")
	}
	if len(result.HookRuns) != 1 || result.HookRuns[0].Denied {
		t.Fatalf("the hook that DID run before the tool failed must be reported and NOT marked Denied (that would mean a PreToolUse block, a different case), got %+v", result.HookRuns)
	}
}

// failingTool always errors, to exercise the "hook ran, tool itself
// failed" path distinctly from a PreToolUse block. reached proves Execute
// was actually called, ruling out a pre-execute dispatcher failure
// satisfying the same assertions for the wrong reason.
type failingTool struct {
	name    string
	reached atomic.Bool
}

func (t *failingTool) Name() string               { return t.name }
func (t *failingTool) Description() string        { return "always fails" }
func (t *failingTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *failingTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead}
}
func (t *failingTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.reached.Store(true)
	return "", errors.New("always fails")
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
	if len(result.HookRuns) != 0 {
		t.Fatalf("nothing dispatched, so there must be no hook runs to report, got %+v", result.HookRuns)
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

// A deferred tool that RAN and failed must not be reported to the model as
// never having run.
//
// runDeferredToolNow returned ok=false for every result.Err, and the caller
// answers ok=false with "authorized but was not yet loaded [...] retry the
// call on your next step". So a tool that executed and errored - an output
// over the ceiling, a handler that failed after a partial side effect - told
// the model the call never happened AND named the retry. The tool is admitted
// by then, so the retry executes for real and every side effect that already
// landed happens twice.
//
// internal/runtime draws this distinction itself and says why: a block "is
// deliberately distinct from failed, which means the tool ran and broke.
// Collapsing them would make a working gate and a broken tool
// indistinguishable". This path collapsed them.
func TestADeferredToolThatRanAndFailedIsNotReportedAsUnloaded(t *testing.T) {
	s := prefixResetSession(t)
	tool := &failingTool{name: "grep"}
	full := tools.NewRegistry()
	full.Register(tool)
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
	result := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))

	if !tool.reached.Load() {
		t.Fatal("the tool's own Execute was never reached, so this test is not " +
			"covering the ran-and-failed case it claims to")
	}
	if !result.Ran {
		t.Error("a tool that executed reports Ran=false, so the caller tells the " +
			"model it was never loaded and to retry - the retry runs it a second time")
	}
	if !result.Failed {
		t.Error("a tool that executed and errored is not marked Failed, so the " +
			"operator's transcript renders it as a successful call")
	}
	if strings.Contains(result.Content, "not yet loaded") {
		t.Errorf("the model is told a call that RAN was never loaded: %q", result.Content)
	}
	if !strings.Contains(result.Content, "always fails") {
		t.Errorf("the model is not told why the call failed: %q", result.Content)
	}
}

// The same for a call a PreToolUse hook blocked. The operator's own hook
// refused it; telling the model it was a loading problem and to retry hides
// the operator's decision and invites the same block again.
func TestADeferredCallBlockedByAHookIsNotReportedAsUnloaded(t *testing.T) {
	s := prefixResetSession(t)
	tool := &failingTool{name: "grep"}
	full := tools.NewRegistry()
	full.Register(tool)
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	pre := func(context.Context, runtime.Request) runtime.HookVerdict {
		return runtime.HookVerdict{
			Denied: true, Reason: "guard.sh refused this call",
			Runs: []runtime.HookRun{{Event: "PreToolUse", Program: "guard.sh", Denied: true}},
		}
	}
	dispatcher := runtime.New(runtime.Policy{PreInvokeHook: pre})
	t.Cleanup(dispatcher.Close)
	s.SetDispatcher(dispatcher)
	s.ToolBaseResolver = func() *tools.Registry { return full }
	s.mu.Lock()
	s.turnID = 7
	s.mu.Unlock()

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)
	result := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))

	if tool.reached.Load() {
		t.Fatal("the hook denied the call but the tool ran anyway")
	}
	if strings.Contains(result.Content, "not yet loaded") {
		t.Errorf("a call the operator's hook blocked is reported to the model as a "+
			"loading problem, with an instruction to retry: %q", result.Content)
	}
	if !strings.Contains(result.Content, "guard.sh refused this call") {
		t.Errorf("the model is not told the hook's reason: %q", result.Content)
	}
	if !result.Failed {
		t.Error("a blocked call is not marked Failed, so the operator's transcript " +
			"renders their own hook's refusal as a successful call")
	}
	if len(result.HookRuns) != 1 || !result.HookRuns[0].Denied {
		t.Errorf("the denying hook run must still reach the operator, got %+v", result.HookRuns)
	}
}

// A DENIED deferred call must not be recorded as a successful one.
//
// The refusal returns ok=true, which the caller reads as Ran and records with
// failed=false - so under a "deny" policy the operator's transcript showed a
// green tool call whose body read "tool call denied by user". The SDK path
// records the opposite on purpose, and its comment names this exact class:
// "the reason a denial used to reach every viewer as a success". The deferred
// path reintroduced it.
func TestADeniedDeferredCallIsNotRecordedAsASuccess(t *testing.T) {
	s := prefixResetSession(t)
	tool := &failingTool{name: "grep"}
	full := tools.NewRegistry()
	full.Register(tool)
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	dispatcher := runtime.New(runtime.Policy{})
	t.Cleanup(dispatcher.Close)
	s.SetDispatcher(dispatcher)
	s.ToolBaseResolver = func() *tools.Registry { return full }
	s.mu.Lock()
	s.turnID = 7
	s.ApprovalPolicy = config.ApprovalPolicyDeny
	s.mu.Unlock()

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)
	result := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))

	if tool.reached.Load() {
		t.Fatal("the policy denied the call but the tool ran anyway")
	}
	if !result.Failed {
		t.Error("a denied call is not marked Failed, so every viewer - the TUI, " +
			"the NDJSON status mapping, the remote reader - shows the refusal as a " +
			"completed, successful tool call")
	}
}

// countingWriteTool is ExecutionWrite, so Capability.Dedups() is true: a
// same-turn duplicate delivery is answered from the record rather than
// executing the side effect twice.
type countingWriteTool struct {
	name string
	runs atomic.Int32
}

func (t *countingWriteTool) Name() string               { return t.name }
func (t *countingWriteTool) Description() string        { return "counting write tool" }
func (t *countingWriteTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *countingWriteTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionWrite}
}
func (t *countingWriteTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.runs.Add(1)
	return t.name + " ran", nil
}
