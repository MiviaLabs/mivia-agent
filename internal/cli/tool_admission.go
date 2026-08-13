package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// applyDeferredToolPrompt appends the binding's frozen deferred-tool index to
// the session prompt. It runs once per binding: every later admission
// republishes this exact prompt, which is what keeps the cached system-prompt
// prefix intact while the tool array grows.
func applyDeferredToolPrompt(sess *chat.Session, res *config.Resolved, plan toolTierPlan, state *agentSessionState) {
	if sess == nil || !plan.Deferred() {
		return
	}
	prompt, maxSteps := sess.AgentSettings()
	prompt = promptWithDeferredIndex(prompt, plan)
	sess.SetAgentSettings(prompt, maxSteps, coreMemoryBlockForState(state))
	if res != nil {
		res.SystemPrompt = prompt
	}
}

// newSurfaceWidener returns the host-owned publisher for staged tool
// admissions (plan tools/05 D7). internal/chat cannot build a session
// dispatcher, so the session records intent and calls back here at the turn
// boundary.
//
// The candidate surface is built first and published only if every
// precondition still holds, checked atomically with the swap inside
// TryPublishAgentSurface. A refused publication closes the candidate
// dispatcher it never installed and leaves the stage pending.
func newSurfaceWidener(sess *chat.Session, res *config.Resolved, state *agentSessionState) chat.SurfaceWidener {
	return func(admitted []string, req chat.AgentSurfacePublication) (bool, error) {
		state.mu.Lock()
		defer state.mu.Unlock()
		candidate, err := buildWidenedWith(sess, res, state, admitted)
		if err != nil {
			return false, err
		}
		req.Registry = candidate.registry
		req.Dispatcher = candidate.dispatcher
		req.SkillRegistry = candidate.skillReg
		req.MemoryBlock = coreMemoryBlockForState(state)
		if !sess.TryPublishAgentSurface(req) {
			candidate.dispatcher.Close()
			return false, nil
		}
		// The widened surface is live, so its derived state is now the session's.
		// The tier plan is unchanged by construction; the skill scope is not, it
		// was rebuilt against a registry that now carries the admitted tools.
		candidate.commitTo(state)
		sess.SetRemainderSpool(RemainderSpoolFromRegistry(candidate.registry))
		recordSchemaMassLocked(sess, state, state.TierPlan, admitted, agentNameOf(state.Selected), "tool_admission")
		return true, nil
	}
}
