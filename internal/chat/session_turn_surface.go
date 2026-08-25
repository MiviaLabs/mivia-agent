package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
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
	opts.UnadmittedToolHandler = func(ctx context.Context, name string, args json.RawMessage) agent.UnadmittedToolResult {
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
		if content, ok := s.runDeferredToolNow(ctx, dispatcher, resolver, sessionID, turnID, name, args); ok {
			return agent.UnadmittedToolResult{Handled: true, Ran: true, Content: content}
		}
		return agent.UnadmittedToolResult{Handled: true, Content: fmt.Sprintf("tool %q is authorized but was not yet loaded. It has been queued to load automatically; publication happens at the next step boundary and can be deferred - retry the call on your next step", name)}
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
// per-call timeout re-arming, no hook-context append, no pass1/turn-shaping
// bookkeeping. It DOES apply the same session-level result cap and
// remainder-spool behavior that path applies (s.MaxToolResultChars, the
// tool's own declared Capability.MaxResultBytes if any, s.RemainderSpool) -
// an operator's configured budget must bound this path exactly like every
// other tool call, not just the dispatcher's own much larger safety-floor
// ceiling that Invoke enforces regardless of caller.
func (s *Session) runDeferredToolNow(ctx context.Context, dispatcher *runtime.Dispatcher, resolver func() *tools.Registry, sessionID string, turnID uint64, name string, args json.RawMessage) (string, bool) {
	if dispatcher == nil || resolver == nil {
		return "", false
	}
	base := resolver()
	if base == nil {
		return "", false
	}
	tool, ok := base.Get(name)
	if !ok {
		return "", false
	}
	if err := dispatcher.RegisterTool(base, tool); err != nil && !dispatcher.Has(runtime.Tool, name) {
		return "", false
	}
	result := dispatcher.Invoke(ctx, runtime.Request{
		TurnID:    fmt.Sprintf("turn:%d", turnID),
		SessionID: sessionID,
		Kind:      runtime.Tool,
		Name:      name,
		Input:     args,
	})
	if result.Err != nil {
		return "", false
	}
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
	capped, _, _ := remainder.CapWithSpoolRef(spool, sessionID, string(result.Output), maxResult)
	return capped, true
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
