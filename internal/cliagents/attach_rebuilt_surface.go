package cliagents

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// AttachRebuiltSurface rebuilds and publishes a session's agent surface
// against its CURRENT tool base. It exists for hosts that swap sess.Tools to
// a freshly built registry after launch - the TUI session pool's worktree
// adoption (SessionPool.adoptWorktreeToolsLocked). Such a registry is built
// by BuildToolsForRoot/composition.BuildRegistry alone and therefore carries
// none of the dispatcher-owned session tools (dispatch_tasks, the messaging
// and ledger tools, load_tools): those are registered by the session
// dispatcher construction at launch onto the launch registry only. Without a
// surface publication the worktree session runs with no orchestration
// surface at all, and the only later path that would rebuild one (a model
// switch) is unreachable from a tool surface that lost load_tools.
//
// The rebuild reuses the exact admission-widening mechanism: the core tier of
// the frozen tier plan (recomputed from the base when the fork carries none),
// the same skill registry discipline, and TryPublishAgentSurface for the
// publication, so the session's prompt, memory block and fence handling are
// byte-identical to any other surface publication.
//
// It reports whether a surface was published. A nil session or state, a
// session without a completer, or a process that never wired the dispatcher
// seam is a silent no-op: hosts that predate agent state or dispatcher
// wiring keep their previous behavior.
func AttachRebuiltSurface(sess *chat.Session, res *config.Resolved, state *AgentSessionState) (bool, error) {
	if sess == nil || state == nil {
		return false, nil
	}
	if sess.CurrentBinding().Completer == nil {
		return false, nil
	}
	// Without the process-level seams there is no dispatcher to build and no
	// spool to install: fail soft exactly like the admission widener's other
	// consumers instead of publishing a half-wired surface.
	if NewSessionDispatcherVar == nil || RemainderSpoolFromRegistryVar == nil {
		return false, nil
	}
	// A fork created before the launch surface captured its tier plan (tests,
	// embeddings) would build an empty core tier and brick the session to the
	// catalog alone. Recompute from the live base - the same computation the
	// launch attach ran - instead of publishing a degenerate plan.
	state.mu.Lock()
	if len(state.TierPlan.Tiers.Core) == 0 {
		if base := entryBase(sess, state); base != nil {
			state.TierPlan = PlanToolTiers(base, state.Selected, res)
		}
	}
	if state.SkillRegFull == nil {
		root := state.WorkspaceRoot
		if root == "" {
			root = "."
		}
		skillReg, warnings, err := LoadSessionSkills(root, state.AllowProjectSkills)
		if err != nil {
			state.mu.Unlock()
			return false, fmt.Errorf("load skills: %w", err)
		}
		WarnSkillLoad(warnings)
		state.SkillRegFull = FilterSkillRegistryForGate(skillReg, state.AllowProjectSkills)
	}
	state.mu.Unlock()
	prompt, maxSteps := sess.AgentSettings()
	published, err := NewSurfaceWidener(sess, res, state)(nil, chat.AgentSurfacePublication{
		Prompt:   prompt,
		MaxSteps: maxSteps,
	})
	if err != nil {
		return false, fmt.Errorf("attach rebuilt surface: %w", err)
	}
	return published, nil
}
