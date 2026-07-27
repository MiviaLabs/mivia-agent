package cli

import (
	"fmt"
)

// workGroupAutoCollapseMin is the tool count at which groups default collapsed.
const workGroupAutoCollapseMin = 4

// workGroup is a half-open index range [Start,End) of thinking/status/tool blocks.
type workGroup struct {
	Start, End int
	ToolCount  int
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
		tools := 0
		key := ""
		for i < len(blocks) && isWorkMember(blocks[i]) {
			if blocks[i].Kind == ChatBlockTool {
				tools++
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
		groups = append(groups, workGroup{Start: start, End: i, ToolCount: tools, Key: "work:" + key})
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
// collapsed map keys are workGroup.Key; absent keys use auto-collapse policy.
// blocks SoT is not mutated.
func RenderChatBlocksWithWorkGroups(blocks []ChatBlock, model string, width int, thinkingExpandDefault bool, collapsed map[string]bool) ChatBlockRender {
	if width < 20 {
		width = 20
	}
	groups := findWorkGroups(blocks)
	groupByStart := make(map[int]workGroup, len(groups))
	for _, g := range groups {
		groupByStart[g.Start] = g
	}

	out := ChatBlockRender{Ranges: make(map[string][2]int)}
	i := 0
	for i < len(blocks) {
		if g, ok := groupByStart[i]; ok {
			isCollapsed := workGroupCollapsedDefault(g, collapsed)
			ensureBlockGap(&out)
			startLine := len(out.Lines)
			header := formatWorkGroupHeader(g, isCollapsed)
			out.Lines = append(out.Lines, header)
			out.Ranges[g.Key] = [2]int{startLine, len(out.Lines)}
			if !isCollapsed {
				for j := g.Start; j < g.End; j++ {
					appendRenderedBlock(&out, blocks[j], model, width, thinkingExpandDefault)
				}
			}
			i = g.End
			continue
		}
		appendRenderedBlock(&out, blocks[i], model, width, thinkingExpandDefault)
		i++
	}
	return out
}

func formatWorkGroupHeader(g workGroup, collapsed bool) string {
	marker := "▾"
	if collapsed {
		marker = "▸"
	}
	text := fmt.Sprintf("  %s Work · %d tools", marker, g.ToolCount)
	return tuiDimStyle.Render(text)
}

func appendRenderedBlock(out *ChatBlockRender, block ChatBlock, model string, width int, thinkingExpandDefault bool) {
	// Shared path with RenderChatBlocks so rails stay consistent.
	lines := renderOneChatBlock(block, model, width, thinkingExpandDefault)
	if len(lines) == 0 {
		return
	}
	// One blank line between bubble groups for readability.
	ensureBlockGap(out)
	start := len(out.Lines)
	out.Lines = append(out.Lines, lines...)
	if block.ID != "" {
		out.Ranges[block.ID] = [2]int{start, len(out.Lines)}
	}
}
