// Typed action model: every transcript action is a tool (⚙), an agent (◆),
// or a skill (§). The classification drives glyphs, work-group counts, and —
// later — the per-agent turn ledger. Glyphs are deliberately single-width
// text, never emoji: emoji are double-width and font-dependent, which
// misaligns columns in real terminals.
package cli

type actionKind int

const (
	actionTool actionKind = iota
	actionAgent
	actionSkill
)

// agentControlTools are the delegation/orchestration surfaces: calling one
// launches or controls another agent, so the transcript marks it ◆.
var agentControlTools = map[string]bool{
	handlerDelegate:   true,
	handlerOneshot:    true,
	handlerMultiStep:  true,
	toolDispatchTasks: true,
	toolSpawnAgent:    true,
	toolJoinRun:       true,
	toolInspectAgents: true,
	toolCancelRun:     true,
}

// actionKindForTool classifies a tool name. Workspace skills are dispatched
// under their own names; classifying them as actionSkill needs the skills
// registry plumbed into the render layer — until then they read as tools.
func actionKindForTool(name string) actionKind {
	if agentControlTools[name] {
		return actionAgent
	}
	return actionTool
}

// actionIcon returns the single-width glyph for a kind.
func actionIcon(kind actionKind) string {
	switch kind {
	case actionAgent:
		return "◆"
	case actionSkill:
		return "§"
	default:
		return "⚙"
	}
}

// actionIconForTool is the glyph for a tool name.
func actionIconForTool(name string) string {
	return actionIcon(actionKindForTool(name))
}
