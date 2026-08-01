package cli

import (
	"fmt"
	"strings"
)

// workGroupAutoCollapseMin is the tool count at which groups default collapsed.
const workGroupAutoCollapseMin = 4

// workGroupWindowRows is the fixed height of an expanded group's scrollable
// window. A turn can contain hundreds of actions; expanding one used to dump
// every member into the transcript, burying the conversation. The group is
// now a bounded viewport with ↑/↓ affordances, scrolled with j/k while it is
// selected (and readable in full via the detail overlay).
const workGroupWindowRows = 12

// maxWorkGroupRows is the legacy alias kept for the non-windowed renderer.
const maxWorkGroupRows = workGroupWindowRows

// workGroup is a half-open index range [Start,End) of thinking/status/tool blocks.
type workGroup struct {
	Start, End int
	ToolCount  int
	AgentCount int    // agent-control actions (◆) among the tools
	FailCount  int    // failed actions - always surfaced on the header
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
		// Single tool (or thinking-only) stays flat - group chrome starts at 2 tools.
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

// RenderChatBlocksWithWorkGroupsView renders with per-group scroll at zero
// offset (compatibility entry point).
func RenderChatBlocksWithWorkGroupsView(blocks []ChatBlock, model string, width int, thinkingExpandDefault bool, collapsed map[string]bool, view railView) ChatBlockRender {
	return RenderChatBlocksWithWorkGroupsWindow(blocks, model, width, thinkingExpandDefault, collapsed, nil, view)
}

// RenderChatBlocksWithWorkGroupsWindow renders expanded groups as bounded
// scrollable windows. scroll maps a group key to its first visible member.
func RenderChatBlocksWithWorkGroupsWindow(blocks []ChatBlock, model string, width int, thinkingExpandDefault bool, collapsed map[string]bool, scroll map[string]int, view railView) ChatBlockRender {
	if width < minCardWidth {
		width = minCardWidth
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
				// Bounded scrollable window over the group's members.
				total := g.End - g.Start
				off := clampWorkGroupScroll(scroll[g.Key], total)
				if off > 0 {
					out.Lines = append(out.Lines, tuiDimStyle.Render(
						fmt.Sprintf("    ↑ %d earlier", off)))
				}
				end := min(g.Start+off+workGroupWindowRows, g.End)
				for j := g.Start + off; j < end; j++ {
					mem := groupMember{}
					if j < len(members) {
						mem = members[j]
					}
					appendRenderedBlockMem(&out, blocks[j], model, width, thinkingExpandDefault, mem, view)
				}
				if remaining := g.End - end; remaining > 0 {
					out.Lines = append(out.Lines, tuiDimStyle.Render(
						fmt.Sprintf("    ↓ %d more · j/k scroll · o open all", remaining)))
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
	marker := glyphTriD
	if collapsed {
		marker = glyphTriR
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

// clampWorkGroupScroll bounds a group's window offset to its member count.
func clampWorkGroupScroll(off, total int) int {
	maxOff := total - workGroupWindowRows
	if maxOff < 0 {
		maxOff = 0
	}
	if off > maxOff {
		off = maxOff
	}
	if off < 0 {
		off = 0
	}
	return off
}

// scrollSelectedWorkGroup moves the selected group's window by one row.
// Reports whether a group was actually scrolled (so the key can fall
// through to other handlers when the selection is not a group).
func (m *tuiModel) scrollSelectedWorkGroup(down bool) bool {
	if m.selectedBlockID == "" || !strings.HasPrefix(m.selectedBlockID, "work:") {
		return false
	}
	var group *workGroup
	for _, g := range findWorkGroups(m.blocks) {
		if g.Key == m.selectedBlockID {
			gg := g
			group = &gg
			break
		}
	}
	if group == nil || workGroupCollapsedDefault(*group, m.workGroupCollapsed) {
		return false
	}
	if m.workGroupScroll == nil {
		m.workGroupScroll = map[string]int{}
	}
	delta := -1
	if down {
		delta = 1
	}
	next := clampWorkGroupScroll(m.workGroupScroll[m.selectedBlockID]+delta, group.End-group.Start)
	m.workGroupScroll[m.selectedBlockID] = next
	m.renderVP()
	return true
}
