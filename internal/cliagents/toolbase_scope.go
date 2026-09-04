package cliagents

// Root-scoped tool-base resolution for dispatcher-rebuild paths.
//
// Plans/toolbase-dispatcher-seam.md: direct dispatch uses sess.Tools
// (root-scoped by SessionPool adoption), but every widened-surface rebuild
// cloned AgentSessionState.ToolBase - captured once at attach from the
// LAUNCH checkout - causing agent/model/MCP switches to silently re-scope
// an adopted entry back to the main checkout. This helper closes that gap
// by preferring the per-session root-scoped base over the shared
// launch-scoped one.
//
// CLI parity: in a single-session CLI process, sess.Tools is the SAME
// registry that state.ToolBase was cloned from at attach time, so either
// source produces identical output. A future resolver install (by the
// pool) makes the preference meaningful only then.

import (
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// entryBase resolves the pre-scope base registry for surface rebuilds,
// preferring the session's own ToolBaseResolver (root-scoped by pool
// adoption) over the shared launch-captured state.ToolBase. Falls back to
// state.ToolBase when no resolver is installed - identical to pre-change
// behavior for callers that never installed one.
func entryBase(sess *chat.Session, state *AgentSessionState) *tools.Registry {
	if sess != nil && sess.ToolBaseResolver != nil {
		if reg := sess.ToolBaseResolver(); reg != nil {
			return reg
		}
	}
	return state.ToolBase
}
