package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
)

// wireStepBoundaryAdmission installs the mid-turn admission publication hook
// and the staged-tool denial message on the turn's options. A tool staged by
// load_tools becomes callable from the NEXT STEP: the loop's Surface hook
// publishes it at the next step boundary, mid-turn, before this turn's commit
// (w2a/w2d). When that boundary defers (R2-1/R2-2), a call to the staged tool
// must report pending publication and the reason instead of the unknown-tool
// denial; the check is dynamic so a same-turn stage is visible too, and it
// announces the deferral cause mid-turn. Scoped turns (TurnOptions/skills) run
// their own surface and must not publish into the session surface mid-turn, so
// the publication is gated on turn == nil.
func (s *Session) wireStepBoundaryAdmission(opts *agent.Options, turn *TurnOptions) {
	opts.StagedToolMessage = func(name string) (string, bool) {
		names, reason, ok := s.PendingAdmissionStatus()
		if !ok || !slices.Contains(names, name) {
			return "", false
		}
		if reason != "" {
			return fmt.Sprintf("tool %q is staged for loading but publication is deferred because %s; it will be retried at the next step boundary", name, reason), true
		}
		return fmt.Sprintf("tool %q is staged for loading but is not published to the live tool surface yet. Publication happens at the step boundary and can be deferred; retry the call on your next step", name), true
	}
	// UnadmittedToolHandler covers the model-behavior risk that advertising
	// the whole admissible union (plan tools-advertising/01) introduces: a
	// deferred tool now LOOKS callable (its schema is on the wire) even
	// before load_tools ever ran for it. Recognize any advertised name,
	// auto-stage it through the same load_tools machinery so it becomes
	// NATIVELY admitted at the next step boundary (later calls in the turn
	// need no special handling), AND serve THIS call synchronously against
	// the full authorized tool set (s.ToolBaseResolver) when possible - the
	// model must never see an error for a call it already made correctly.
	// Root turns only (turn == nil): a scoped skill turn does not own the
	// session's admission state, matching the Surface hook's own turn == nil
	// gate; it still gets a denial, never a synchronous execution.
	// The prompt emitter is captured HERE because opts carries the session's
	// event sinks (OnEvent/EventBus), which is what the TUI's approval prompt
	// is drawn from. The deferred path had none, so an interactive policy
	// blocked on a gate whose prompt was never rendered.
	emitPending := agent.ToolPendingEmitter(*opts)
	opts.UnadmittedToolHandler = func(ctx context.Context, name string, args json.RawMessage) agent.UnadmittedToolResult {
		return s.serveUnadmittedTool(ctx, turn, name, args, emitPending)
	}
	opts.Surface = func() agent.Surface {
		if turn == nil {
			s.PublishPendingAdmissionAtStepBoundary()
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		reg, disp := resolveTurnExecutionSurface(s.Tools, s.binding.Dispatcher, turn)
		// ToolSpecs advertises the session's pinned binding-lifetime snapshot
		// (plan tools-advertising/01), NOT reg.OpenAITools(): admission
		// widening (PublishPendingAdmissionAtStepBoundary above) and skill
		// scoping change execution authority (reg, disp) only. Advertising the
		// live registry here was the mid-turn cache-invalidation bug this plan
		// fixes - a load_tools admission or a scoped skill turn must never
		// change the wire tools[] array.
		return agent.Surface{Registry: reg, Dispatcher: disp, ToolSpecs: s.advertisedToolSpecs, RemainderSpool: s.RemainderSpool}
	}
}

// serveUnadmittedTool answers ONE call to a tool that is advertised but not
// yet admitted. It is the body of opts.UnadmittedToolHandler, lifted out so
// the wiring above stays readable.
func (s *Session) serveUnadmittedTool(ctx context.Context, turn *TurnOptions, name string, args json.RawMessage, emitPending func(toolCallID, name, detail, input string)) agent.UnadmittedToolResult {
	if !s.isAdvertisedToolName(name) {
		return agent.UnadmittedToolResult{}
	}
	if turn != nil {
		return agent.UnadmittedToolResult{Handled: true, Content: fmt.Sprintf("tool %q is authorized but not currently loaded for this scoped run; ask the root agent to load it first", name)}
	}
	if err := s.ChargeAdmissionAttempt(); err != nil {
		return agent.UnadmittedToolResult{Handled: true, Content: err.Error()}
	}
	// TurnIDFromContext reads a dispatcher-stamped caller frame
	// (runtime.ContextWithCaller) that only exists inside
	// Dispatcher.Invoke - this call site is the "tool not found" branch
	// in executeToolTask, which never reaches the dispatcher, so it is
	// always (0, false) here. turnID 0 means "no owning turn" to
	// StageToolAdmission: dropPendingAdmissionForTurn would never be able
	// to drop this stage if the turn that requested it later errors or is
	// superseded, pinning it forever. Read the session's own live turn id
	// instead - correct here because this handler only ever runs
	// synchronously inside that turn's own loop.Run call.
	s.mu.RLock()
	turnID := s.turnID
	dispatcher := s.binding.Dispatcher
	resolver := s.ToolBaseResolver
	sessionID := s.SessionID
	s.mu.RUnlock()
	if _, err := s.StageToolAdmission([]string{name}, turnID); err != nil {
		return agent.UnadmittedToolResult{Handled: true, Content: err.Error()}
	}
	content, hookContext, hookRuns, failed, ok := s.runDeferredToolNow(ctx, dispatcher, resolver, sessionID, turnID, name, args, emitPending)
	if ok {
		// Ran means it reached the dispatcher; Failed carries whether it
		// succeeded. The message below instructs a RETRY, so anything
		// that ran, was blocked, or was refused must never reach it.
		return agent.UnadmittedToolResult{Handled: true, Ran: true, Failed: failed, Content: content, HookContext: hookContext, HookRuns: hookRuns}
	}
	return agent.UnadmittedToolResult{
		Handled: true, HookContext: hookContext, HookRuns: hookRuns,
		Content: fmt.Sprintf("tool %q is authorized but was not yet loaded. It has been queued to load automatically; publication happens at the next step boundary and can be deferred - retry the call on your next step", name),
	}
}

// isAdvertisedToolName reports whether name appears in the pinned advertised
// snapshot. It is the authority UnadmittedToolHandler uses to distinguish a
// real (deferred-but-advertised) tool from a hallucinated name: the snapshot
// is built from the frozen tier plan's admissible union (buildSurfaceFromBase
// / advertisedToolSpecs), so a name outside it was never authorized for this
// binding and must fall through to the generic denial instead of being staged.
func (s *Session) isAdvertisedToolName(name string) bool {
	s.mu.RLock()
	specs := s.advertisedToolSpecs
	s.mu.RUnlock()
	for _, spec := range specs {
		fn, _ := spec["function"].(map[string]any)
		if fn == nil {
			continue
		}
		if n, _ := fn["name"].(string); n == name {
			return true
		}
	}
	return false
}

// runDeferredToolNow serves ONE deferred-but-authorized tool call synchronously
// against the full authorized tool set, so the model gets the real result
// instead of a denial telling it to retry next turn. ok=false on any of
// several benign reasons (no dispatcher, no resolver wired, the resolver
// returns nil, the name is absent even from the full set) - the caller
// falls back to the staged-only denial message exactly as before this
// existed; it is not an error path.
//
// The tool is registered into dispatcher against base (the FULL registry,
// not s.Tools): the installed handler executes via base.Execute, so the
// call succeeds even though s.Tools itself is not widened until the
// step-boundary publish runs - no live-surface mutation is needed just to
// serve this one call. RegisterTool's "duplicate handler" error is treated
// as success (dispatcher.Has confirms it), not failure: a sibling call for
// the same deferred tool in the same step may have already won the race.
//
// This is deliberately NOT full parity with an already-admitted call's
// dispatcherShim.Run path (internal/agent/sdk_dispatcher_shim.go): no
// per-call timeout re-arming, no pass1/turn-shaping bookkeeping. Hook-run
// VISIBILITY (the operator's transcript row) and hook CONTEXT (the
// advisory text a PostToolUse hook hands the MODEL) are both threaded
// below, on the Ran path. A PreToolUse block (ok=false) still returns
// hookRuns and hookContext, but the caller does not append hookContext to
// the model-facing denial text this wave - only the Ran path's caller
// (agentloop_tool_error.go) appends it. This path's dedup bucket also
// differs from the shim's (ParentID/Step left zero). See
// docs/development/lifecycle-hooks.md's "Limitation" notes for what
// remains open and why.
func (s *Session) runDeferredToolNow(ctx context.Context, dispatcher *runtime.Dispatcher, resolver func() *tools.Registry, sessionID string, turnID uint64, name string, args json.RawMessage, emitPending func(toolCallID, name, detail, input string)) (content string, hookContext string, hookRuns []runtime.HookRun, failed bool, ok bool) {
	tool, found := resolveDeferredTool(dispatcher, resolver, name)
	if !found {
		return "", "", nil, false, false
	}
	// Approval. This path invokes the runtime dispatcher DIRECTLY, underneath
	// the SDK registry where the approval wrapper lives, so it carried no
	// approval at all: a threat model drove a write tool through it and
	// watched the file appear under a "deny" policy with a live approver
	// attached. It asks the shared decision rather than restating the policy,
	// because a second copy of that logic is how this class of hole keeps
	// reappearing.
	//
	// failed=true on the refusal. It is served (ok=true) because the model
	// gets the reason rather than the "not yet loaded" fallback - but a
	// refusal is not a completed call, and recording it as one is how a
	// denial reached every viewer as a success on the SDK path before
	// recordDenied existed. This path had reintroduced it.
	if decision := s.decideDeferredApproval(ctx, tool, name, args, emitPending); !decision.Approved {
		return "tool call denied by user: " + decision.Reason, "", nil, true, true
	}
	result := dispatcher.Invoke(ctx, runtime.Request{
		TurnID:    fmt.Sprintf("turn:%d", turnID),
		SessionID: sessionID,
		Kind:      runtime.Tool,
		Name:      name,
		Input:     args,
	})
	// HookContext is set unconditionally, including for a dedup-served
	// duplicate: DC-9 (internal/runtime/dispatcher.go) answers a duplicate
	// with the OWNER's post-hook Result, and the owner's HookContext is
	// exactly what dispatcherShim.Run appends for its own duplicates too.
	hookContext = result.HookContext
	// A dedup-served duplicate is answered with the OWNER's HookRuns (DC-9
	// fidelity), which did not run for THIS call - reporting them would
	// show a hook firing that never fired here. Mirrors dispatcherShim.Run's
	// !r.IsDuplicate() guard. HookRuns (the operator's transcript row) and
	// HookContext (the model's advisory text) have different duplicate
	// contracts on purpose: a duplicate call's hook did not run, but the
	// owner's post-hook advisory text is still valid content to hand the
	// model again, same as the owner's tool Output is.
	if !result.IsDuplicate() {
		hookRuns = result.HookRuns
	}
	// A failure is SERVED, not degraded to the "not yet loaded" fallback.
	//
	// Every result.Err used to return ok=false, and the caller answers that
	// with "authorized but was not yet loaded [...] retry the call on your
	// next step". For a call that RAN and errored - an output over the
	// ceiling, a handler that failed after a partial side effect - that told
	// the model the call never happened and named the retry; the tool is
	// admitted by then, so the retry executed for real and every side effect
	// that had already landed happened twice.
	//
	// A PreToolUse block goes the same way. The operator's own hook refused
	// the call, and reporting that as a loading problem hides their decision
	// and invites the identical block again. runtime already distinguishes
	// the two for the audit sink ("a block and a broken tool must not be
	// indistinguishable"); what the MODEL needs from both is the truth that
	// the call will not be served, and why.
	//
	// The body is result.Output either way, which deliverTerminal always
	// fills with {"status":..., "error":...} - the same bytes
	// dispatcherShim.Run hands the model for an admitted failure, down to
	// the blank-output fallback below.
	failed = result.Err != nil
	body := string(result.Output)
	if failed && strings.TrimSpace(body) == "" {
		body = fmt.Sprintf("error: %v", result.Err)
	}
	return s.capDeferredBody(sessionID, tool, args, body), hookContext, hookRuns, failed, true
}

// resolveDeferredTool finds the tool in the FULL authorized set and installs
// its handler on the dispatcher. found=false on the benign reasons the caller
// degrades on: no dispatcher, no resolver wired, the resolver returns nil, the
// name is absent even from the full set.
//
// RegisterTool's "duplicate handler" error is treated as success (Has confirms
// it), not failure: a sibling call for the same deferred tool in the same step
// may have already won the race.
func resolveDeferredTool(dispatcher *runtime.Dispatcher, resolver func() *tools.Registry, name string) (tools.Tool, bool) {
	if dispatcher == nil || resolver == nil {
		return nil, false
	}
	base := resolver()
	if base == nil {
		return nil, false
	}
	tool, found := base.Get(name)
	if !found {
		return nil, false
	}
	if err := dispatcher.RegisterTool(base, tool); err != nil && !dispatcher.Has(runtime.Tool, name) {
		return nil, false
	}
	return tool, true
}

// capDeferredBody bounds a deferred call's model-facing body by the smaller of
// the session ceiling and the tool's own declared cap.
//
// It runs for a FAILURE as well as a success. A failure body carries a
// caller-authored reason - a hook's text, a tool's error - with no bound of
// its own, and only the success path was ever capped.
func (s *Session) capDeferredBody(sessionID string, tool tools.Tool, args json.RawMessage, body string) string {
	s.mu.RLock()
	maxChars, spool := s.MaxToolResultChars, s.RemainderSpool
	s.mu.RUnlock()
	capabilityMaxBytes := 0
	if capable, ok := tool.(tools.CapableTool); ok {
		capabilityMaxBytes = capable.Capability(args).MaxResultBytes
	}
	maxResult := maxChars
	if capabilityMaxBytes > 0 && (maxResult <= 0 || capabilityMaxBytes < maxResult) {
		maxResult = capabilityMaxBytes
	}
	capped, _, _ := remainder.CapWithSpoolRef(spool, sessionID, body, maxResult)
	return capped
}

// commitTurnToken returns the token a step-boundary publication re-captured
// under the post-publication fence, so the staging turn's own commit is not
// fenced out by that publication (chat-turnstart-admission-fences-own-turn
// analog). It returns the live token ONLY when one was captured AND it belongs
// to this committing turn; the committing-turn ownership check keeps a
// superseded turn from borrowing a newer turn's token. Otherwise the caller's
// fallback token (captured before any step-boundary publication) is returned
// unchanged. The read takes the same RLock captureTurnToken uses.
func (s *Session) commitTurnToken(committingTurn uint64, fallback OperationToken) OperationToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.liveTurnToken.IdempotencyKey != "" && s.liveTurnToken.TurnID == committingTurn {
		return s.liveTurnToken
	}
	return fallback
}

// SetRemainderSpool publishes the spool under the session lock so a turn
// starting concurrently cannot observe a torn pointer.
//
// (The step-boundary admission publication entry point, PublishPendingAdmissionAtStepBoundary,
// lives in admission_status.go and shares publishPendingAdmissionFull with the turn-boundary
// paths; its token re-capture is documented there.)
func (s *Session) SetRemainderSpool(spool *remainder.Spool) {
	s.mu.Lock()
	s.RemainderSpool = spool
	s.mu.Unlock()
}

// decideDeferredApproval asks the one approval decision on behalf of the
// deferred-tool path, and raises a prompt the operator can answer.
//
// It reads the session's approval state under the same lock the rest of the
// session uses. It used to pass no EmitPending, on the stated grounds that
// "this path has no in-flight SDK call id to match a prompt back to" and that
// the result was a deny "rather than hang". BOTH claims were false. The SDK
// stamps the call id into the ctx it hands this handler
// (toolcallctx.WithToolCall), and uiadapter's gate already keys its waiter off
// exactly that id - while the missing prompt meant an interactive policy
// called the gate, blocked on a channel nobody could resolve, and drew
// nothing until the operator cancelled the turn.
//
// When the ctx genuinely carries no call id - a direct caller, a legacy
// backend - the call is REFUSED with a reason rather than left waiting on a
// prompt that cannot be raised. A refusal an operator can read beats a hang
// they cannot.
func (s *Session) decideDeferredApproval(ctx context.Context, tool tools.Tool, name string, args json.RawMessage, emitPending func(toolCallID, name, detail, input string)) sdkadapter.ApprovalDecision {
	s.mu.Lock()
	deps := sdkadapter.ApprovalDeps{
		Policy:   s.ApprovalPolicy,
		Standing: s.ApprovalStanding,
		Gate:     s.ApprovalGate,
	}
	s.mu.Unlock()

	toolCallID := ""
	if tc, ok := toolcallctx.ToolCallFromContext(ctx); ok {
		toolCallID = tc.ID
	}
	if toolCallID == "" || emitPending == nil {
		// Nothing can draw this prompt, or nothing can resolve it. Replace the
		// gate rather than dropping it: auto, read-class and standing
		// decisions are all settled BEFORE the gate is consulted, so they keep
		// working, and only the case that would have hung is refused.
		deps.Gate = func(context.Context, string, json.RawMessage) sdkadapter.ApprovalResult {
			return sdkadapter.ApprovalResult{Err: "an approval prompt cannot be raised for a deferred tool call on this path; load the tool first, or set an approval policy that does not prompt"}
		}
	} else {
		deps.EmitPending = emitPending
	}
	if deps.Policy == "" {
		deps.Policy = config.ApprovalPolicyWriteOnly
	}

	// The unclassified default lives in tools.CapabilityOf, which is also what
	// the SDK approval wrapper uses. It was written out here as well, so the
	// same rule existed twice and could drift.
	capability := tools.CapabilityOf(tool, args)
	return sdkadapter.DecideApproval(ctx, deps, sdkadapter.ApprovalRequest{
		ToolCallID:  toolCallID,
		Name:        name,
		Class:       capability.Class,
		ResourceKey: capability.ResourceKey,
		Args:        args,
	})
}
