package cli

import (
	"fmt"
)

// workGroupAutoCollapseMin is the tool count at which groups default collapsed.
const workGroupAutoCollapseMin = 4

// maxWorkGroupRows caps how many members an expanded group renders inline.
// Beyond it, an explicit "… N more" line replaces the tail — a huge turn
// must never dump hundreds of rows into the transcript.
const maxWorkGroupRows = 30

// workGroup is a half-open index range [Start,End) of thinking/status/tool blocks.
type workGroup struct {
	Start, End int
	ToolCount  int
	AgentCount int    // agent-control actions (◆) among the tools
	FailCount  int    // failed actions — always surfaced on the header
	Key        string // stable id for collapse state
}

// findWorkGroups returns contiguous work runs broken by user/assistant/divider/non-status system.
func findWorkGroups(blocks []ChatBlock) []workGroup {
	var groups []workGroup
	i := 0
	for i < len(blocks) {
		if !isWorkMember(blocks[i]) {
			i++
			continue
		}
		start := i
		tools, agents, failed := 0, 0, 0
		key := ""
		for i < len(blocks) && isWorkMember(blocks[i]) {
			if blocks[i].Kind == ChatBlockTool {
				tools++
				if actionKindForTool(blocks[i].ToolName) == actionAgent {
					agents++
				}
				if blocks[i].Failed {
					failed++
				}
				if key == "" {
					key = blocks[i].ID
					if key == "" {
						key = fmt.Sprintf("tool-%d", i)
					}
				}
			}
			if key == "" && blocks[i].ID != "" {
				key = blocks[i].ID
			}
			i++
		}
		if tools == 0 && i == start {
			continue
		}
		// Single tool (or thinking-only) stays flat — group chrome starts at 2 tools.
		if tools < 2 {
			continue
		}
		if key == "" {
			key = fmt.Sprintf("wg-%d-%d", start, i)
		}
		groups = append(groups, workGroup{
			Start: start, End: i, ToolCount: tools,
			AgentCount: agents, FailCount: failed,
			Key: "work:" + key,
		})
	}
	return groups
}

func isWorkMember(b ChatBlock) bool {
	switch b.Kind {
	case ChatBlockThinking, ChatBlockTool:
		return true
	case ChatBlockSystem:
		return isWorkStatusBlock(b)
	default:
		return false
	}
}

// workGroupCollapsedDefault returns whether a group should start collapsed.
func workGroupCollapsedDefault(g workGroup, overrides map[string]bool) bool {
	if overrides != nil {
		if v, ok := overrides[g.Key]; ok {
			return v
		}
	}
	return g.ToolCount >= workGroupAutoCollapseMin
}

// RenderChatBlocksWithWorkGroups renders blocks with optional collapsible work groups.
func RenderChatBlocksWithWorkGroups(blocks []ChatBlock, model string, width int, thinkingExpandDefault bool, collapsed map[string]bool) ChatBlockRender {
	return RenderChatBlocksWithWorkGroupsView(blocks, model, width, thinkingExpandDefault, collapsed, railView{})
}

// RenderChatBlocksWithWorkGroupsView adds live rail frame/liveness.
func RenderChatBlocksWithWorkGroupsView(blocks []ChatBlock, model string, width int, thinkingExpandDefault bool, collapsed map[string]bool, view railView) ChatBlockRender {
	if width < 20 {
		width = 20
	}
	groups := findWorkGroups(blocks)
	groupByStart := make(map[int]workGroup, len(groups))
	for _, g := range groups {
		groupByStart[g.Start] = g
	}
	members := buildGroupMembers(blocks)

	out := ChatBlockRender{Ranges: make(map[string][2]int)}
	i := 0
	for i < len(blocks) {
		if g, ok := groupByStart[i]; ok {
			isCollapsed := workGroupCollapsedDefault(g, collapsed)
			ensureBlockGap(&out)
			startLine := len(out.Lines)
			header := formatWorkGroupHeader(g, isCollapsed, view)
			out.Lines = append(out.Lines, header)
			out.Ranges[g.Key] = [2]int{startLine, len(out.Lines)}
			if !isCollapsed {
				// Cap the inline dump: past maxWorkGroupRows members, an
				// explicit "… N more" line replaces the tail.
				shown := 0
				for j := g.Start; j < g.End; j++ {
					if shown >= maxWorkGroupRows {
						remaining := g.End - j
						out.Lines = append(out.Lines, tuiDimStyle.Render(
							fmt.Sprintf("    … %d more actions", remaining)))
						break
					}
					mem := groupMember{}
					if j < len(members) {
						mem = members[j]
					}
					appendRenderedBlockMem(&out, blocks[j], model, width, thinkingExpandDefault, mem, view)
					shown++
				}
			}
			// One empty lane after the whole Work group (collapsed or expanded).
			// Tools inside stay tight; spacing is only after the set.
			if len(out.Lines) == 0 || out.Lines[len(out.Lines)-1] != "" {
				out.Lines = append(out.Lines, "")
			}
			i = g.End
			continue
		}
		mem := groupMember{}
		if i < len(members) {
			mem = members[i]
		}
		appendRenderedBlockMem(&out, blocks[i], model, width, thinkingExpandDefault, mem, view)
		i++
	}
	return out
}

func formatWorkGroupHeader(g workGroup, collapsed bool, view railView) string {
	marker := "▾"
	if collapsed {
		marker = "▸"
	}
	// Base segment stays stable ("Work · N tools"); typed counts extend it
	// so a collapsed group still tells the operator what ran and what broke.
	text := fmt.Sprintf("  %s Work · %d tools", marker, g.ToolCount)
	if g.AgentCount > 0 {
		text += fmt.Sprintf(" · %d ◆", g.AgentCount)
	}
	line := tuiDimStyle.Render(text)
	if g.FailCount > 0 {
		line += tuiDimStyle.Render(" · ") + tuiErrorStyle.Render(fmt.Sprintf("%d ✗", g.FailCount))
	}
	opts := chromeRenderOpts()
	state := RailStateNeutral
	if view.Live && g.ToolCount >= 2 {
		state = RailStateParallelLive
	}
	rail := railFromRole(RailRoleGroupHeader, state, opts, view)
	return applyLeftRail([]string{line}, rail)[0]
}

func appendRenderedBlock(out *ChatBlockRender, block ChatBlock, model string, width int, thinkingExpandDefault bool) {
	appendRenderedBlockMem(out, block, model, width, thinkingExpandDefault, groupMember{}, railView{})
}

func appendRenderedBlockMem(out *ChatBlockRender, block ChatBlock, model string, width int, thinkingExpandDefault bool, mem groupMember, view railView) {
	lines := renderOneChatBlockMem(block, model, width, thinkingExpandDefault, mem, view)
	if len(lines) == 0 {
		return
	}
	// No ensureBlockGap before: message bubbles own a trailing empty lane;
	// tools/groups stay tight (no bottom margin).
	start := len(out.Lines)
	out.Lines = append(out.Lines, lines...)
	if block.ID != "" {
		// Range excludes trailing empty lane so selection hits content only.
		out.Ranges[block.ID] = [2]int{start, len(out.Lines)}
	}
	if wantsBottomLane(block, mem) {
		if len(out.Lines) == 0 || out.Lines[len(out.Lines)-1] != "" {
			out.Lines = append(out.Lines, "")
		}
	}
}

// wantsBottomLane: free empty row after user/assistant speech for readability.
// Tools, thinking, work-group members, system status: no bottom margin.
func wantsBottomLane(block ChatBlock, mem groupMember) bool {
	if mem.InGroup || mem.IsHeader {
		return false
	}
	switch block.Kind {
	case ChatBlockUser, ChatBlockAssistant:
		return true
	default:
		return false
	}
}
