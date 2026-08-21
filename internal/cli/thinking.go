package cli

import "strings"

// renderThinkingBlockView takes raw thinking text, collapse state, and a scrollOffset
// and returns a rendered string (for use in the live streaming viewport).
//
// liveFrame + live=true drive cyan pulse on the live overlay only; history
// blocks use Live=false via renderBlocksForView.
func renderThinkingBlockView(id, text string, collapsed bool, scrollOffset int, model string, width int, thinkingExpandDefault bool, liveFrame int, live bool) string {
	rendered := RenderChatBlocksView(
		[]ChatBlock{{ID: id, Kind: ChatBlockThinking, Text: text, Collapsed: collapsed, ScrollOffset: scrollOffset}},
		model,
		Max(40, width-2),
		RailView{Frame: liveFrame, Live: live},
		thinkingExpandDefault,
	)
	return strings.Join(rendered.Lines, "\n")
}
