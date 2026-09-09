package chat

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
	// The handler hands the tool back; the loop runs it through the shared
	// shim. "Served" now means admitted for execution, not executed here.
	if result.Execute == nil {
		t.Fatalf("expected the call to be admitted for execution, got denial: %q", result.Content)
	}
	if result.Execute.Name() != "grep" {
		t.Fatalf("admitted %q, want grep", result.Execute.Name())
	}
	if strings.Contains(result.Content, "next step") {
		t.Fatalf("an admitted call must carry no retry framing, got %q", result.Content)
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
	if result2.Execute == nil {
		t.Fatalf("second identical call = %+v, want the tool admitted again", result2)
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

// TestStagedPendingCallIsHotServedWithoutRecharging pins the hot-serve change:
// a root-turn call to a name that is ALREADY staged (by load_tools, or by an
// earlier deferred call whose publication deferred) must fall through the
// staged notice to the synchronous serve - and must NOT charge a second
// admission attempt, because the call that staged the name already spent it.
// The stage itself stays pending: native publication at the boundary remains
// what makes the tool admitted for later steps and turns.
func TestStagedPendingCallIsHotServedWithoutRecharging(t *testing.T) {
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
	if _, err := s.StageToolAdmission([]string{"grep"}, 7); err != nil {
		t.Fatalf("stage: %v", err)
	}

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)

	// The staged notice no longer answers a root turn calling a staged name.
	if msg, ok := opts.StagedToolMessage("grep"); ok {
		t.Fatalf("a root turn calling a staged tool must hot-serve, got notice %q", msg)
	}

	attemptsBefore := s.admissionAttempts
	result := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))
	if result.Execute == nil {
		t.Fatalf("staged call must be admitted for execution, got denial: %q", result.Content)
	}
	if s.admissionAttempts != attemptsBefore {
		t.Fatalf("hot-serving an already-staged name charged another attempt (%d -> %d); "+
			"the staging call already spent it", attemptsBefore, s.admissionAttempts)
	}

	stage, has := s.PendingAdmission()
	if !has || !slices.Contains(stage.Names, "grep") {
		t.Fatalf("the stage must stay pending for native publication, got %+v (has=%v)", stage, has)
	}

	// While the surface is switching, the staged notice is the honest answer.
	s.mu.Lock()
	s.switching = true
	s.mu.Unlock()
	if msg, ok := opts.StagedToolMessage("grep"); !ok || !strings.Contains(msg, "staged for loading") {
		t.Fatalf("a switching surface must keep the staged notice, got ok=%v %q", ok, msg)
	}
	s.mu.Lock()
	s.switching = false
	s.mu.Unlock()

	// A scoped turn keeps the notice too: for it, "callable at the next
	// boundary" is true, while serveUnadmittedTool could only refuse.
	turn := &TurnOptions{Tools: tools.NewRegistry()}
	var scopedOpts agent.Options
	s.wireStepBoundaryAdmission(&scopedOpts, turn)
	if msg, ok := scopedOpts.StagedToolMessage("grep"); !ok || !strings.Contains(msg, "staged for loading") {
		t.Fatalf("a scoped turn must keep the staged notice, got ok=%v %q", ok, msg)
	}
}

// TestHotServingAStagedWriteToolStillAsksApproval pins that the hot-serve
// shortcut is not a way past a prompt: the per-call approval decision runs
// before the skip-the-charge branch, so a staged WRITE tool under a deny
// policy is refused and rendered as a failure, exactly like a fresh deferred
// call would be.
func TestHotServingAStagedWriteToolStillAsksApproval(t *testing.T) {
	s := prefixResetSession(t)

	write := &countingWriteTool{name: "grep"}
	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "read_file"})
	full.Register(write)
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	dispatcher := runtime.New(runtime.Policy{})
	t.Cleanup(dispatcher.Close)
	s.SetDispatcher(dispatcher)
	s.ToolBaseResolver = func() *tools.Registry { return full }

	s.mu.Lock()
	s.turnID = 7
	s.ApprovalPolicy = config.ApprovalPolicyDeny
	s.mu.Unlock()
	if _, err := s.StageToolAdmission([]string{"grep"}, 7); err != nil {
		t.Fatalf("stage: %v", err)
	}

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)
	result := opts.UnadmittedToolHandler(context.Background(), "grep", json.RawMessage(`{}`))

	if write.runs.Load() != 0 {
		t.Fatal("the policy denied the call but the staged hot-serve ran the tool anyway")
	}
	if !result.Failed {
		t.Error("a denied hot-serve is not marked Failed, so every viewer shows the " +
			"refusal as a completed, successful tool call")
	}
}
