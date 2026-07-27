package cli

import "strings"

// renderThinkingBlockView takes raw thinking text, collapse state, and a scrollOffset
// and returns a rendered string (for use in the live streaming viewport).
func renderThinkingBlockView(id, text string, collapsed bool, scrollOffset int, model string, width int) string {
	rendered := RenderChatBlocks(
		[]ChatBlock{{ID: id, Kind: ChatBlockThinking, Text: text, Collapsed: collapsed, ScrollOffset: scrollOffset}},
		model,
		max(40, width-2),
	)
	return strings.Join(rendered.Lines, "\n")
}

// clipThinkingFromLines strips thinking blocks from rendered lines (for clipboard export).
func clipThinkingFromLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.Contains(l, "thinking") {
			continue
		}
		out = append(out, l)
	}
	return out
}
