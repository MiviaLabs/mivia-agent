package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
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
		// A ROOT turn calling a name that is already staged AND whose surface
		// is stable gets the call hot-served by the UnadmittedToolHandler
		// below, not this notice: the synchronous serve executes on the
		// current dispatcher and widens nothing, so a pending stage is no
		// reason to make the model wait for a publication it does not need.
		// Scoped turns keep the notice - "callable at the next boundary" is
		// true for them, while serveUnadmittedTool could only refuse - and so
		// do the surfaces whose replacement is in flight (switching, or a
		// guard refusing), where the wait is real.
		if turn == nil && s.hotServeEligible(name) {
			return "", false
		}
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
	denylist := s.ToolDenylist
	s.mu.RUnlock()
	return s.admitDeferredCall(ctx, turnID, dispatcher, resolver, denylist, name, args, emitPending)
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

// spendAdmissionFor charges the attempt and stages the name for publication.
//
// The two are one step because their ORDER is a requirement, not a style:
// StageToolAdmission refunds the attempt the host charged when the request
// turns out to be a no-op, so the charge must already have happened.
//
// The caller runs this only after the call is resolved and approved. It used
// to run first, so a refused call spent one of MaxAdmissionAttempts and a name
// nothing could resolve burned the publication ceiling.
func (s *Session) spendAdmissionFor(name string, turnID uint64) error {
	if err := s.ChargeAdmissionAttempt(); err != nil {
		return err
	}
	_, err := s.StageToolAdmission([]string{name}, turnID)
	return err
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
