package chat

import (
	"context"
	"fmt"
	"slices"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
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
	// before load_tools ever ran for it. Rather than a bare "not available"
	// denial, recognize any advertised name and auto-stage it through the
	// same load_tools machinery, so the model recovers in exactly one extra
	// step instead of having to learn to call load_tools first. Root turns
	// only (turn == nil): a scoped skill turn does not own the session's
	// admission state, matching the Surface hook's own turn == nil gate.
	opts.UnadmittedToolHandler = func(ctx context.Context, name string) (string, bool) {
		if !s.isAdvertisedToolName(name) {
			return "", false
		}
		if turn != nil {
			return fmt.Sprintf("tool %q is authorized but not currently loaded for this scoped run; ask the root agent to load it first", name), true
		}
		if err := s.ChargeAdmissionAttempt(); err != nil {
			return err.Error(), true
		}
		turnID, _ := TurnIDFromContext(ctx)
		if _, err := s.StageToolAdmission([]string{name}, turnID); err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("tool %q is authorized but was not yet loaded. It has been queued to load automatically; publication happens at the next step boundary and can be deferred - retry the call on your next step", name), true
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
