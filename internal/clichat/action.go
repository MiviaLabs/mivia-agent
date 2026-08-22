// Typed action model: every transcript action is a tool (⚙), an agent (◆),
// or a skill (§). The classification drives glyphs, work-group counts, and -
// later - the per-agent turn ledger. Glyphs are deliberately single-width
// text, never emoji: emoji are double-width and font-dependent, which
// misaligns columns in real terminals.
package clichat

import (
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
)

type actionKind int

const (
	actionTool actionKind = iota
	// ActionAgent marks a delegation/orchestration tool call. Shared with
	// internal/clichat's transcript renderer.
	ActionAgent
	actionSkill
)

// agentControlTools are the delegation/orchestration surfaces: calling one
// launches or controls another agent, so the transcript marks it ◆.
var agentControlTools = map[string]bool{
	cliorchestrate.HandlerDelegate:   true,
	cliorchestrate.HandlerOneshot:    true,
	cliorchestrate.HandlerMultiStep:  true,
	cliorchestrate.ToolDispatchTasks: true,
	cliorchestrate.ToolSpawnAgent:    true,
	cliorchestrate.ToolJoinRun:       true,
	cliorchestrate.ToolInspectAgents: true,
	cliorchestrate.ToolCancelRun:     true,
}

// ActionKindForTool classifies a tool name. Workspace skills are dispatched
// under their own names; classifying them as actionSkill needs the skills
// registry plumbed into the render layer - until then they read as tools.
func ActionKindForTool(name string) actionKind {
	if agentControlTools[name] {
		return ActionAgent
	}
	return actionTool
}

// actionIcon returns the single-width glyph for a kind.
func actionIcon(kind actionKind) string {
	switch kind {
	case ActionAgent:
		return GlyphDiamond
	case actionSkill:
		return "§"
	default:
		return "⚙"
	}
}

// ActionIconForTool is the glyph for a tool name. Shared with
// internal/legacytui's tool status renderer.
func ActionIconForTool(name string) string {
	return actionIcon(ActionKindForTool(name))
}
