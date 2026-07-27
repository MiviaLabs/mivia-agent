package cli

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type ChatBlockRender struct {
	Lines  []string
	Ranges map[string][2]int
}

func RenderChatBlocks(blocks []ChatBlock, model string, width int) ChatBlockRender {
	if width < 20 {
		width = 20
	}
	out := ChatBlockRender{Ranges: make(map[string][2]int)}
	for _, block := range blocks {
		start := len(out.Lines)
		if block.Rendered != "" {
			out.Lines = append(out.Lines, SafeChatBlockText(block.Rendered, 0))
			out.Ranges[block.ID] = [2]int{start, len(out.Lines)}
			continue
		}
		text := SafeChatBlockText(block.Text, 0)
		var lines []string
		switch block.Kind {
		case ChatBlockUser, ChatBlockAssistant:
			if block.Collapsed {
				lines = []string{"  … " + string(block.Kind)}
			} else {
				lines = RenderMessageForHistory(providerMessageForBlock(block, text), model, width)
			}
		case ChatBlockTool:
			lines = renderToolBlock(block, text, model, width)
		case ChatBlockThinking:
			lines = renderThinkingBlock(text, block.Collapsed, block.ScrollOffset)
		case ChatBlockSystem:
			if text != "" {
				lines = []string{tuiDimStyle.Render("  ⚙ " + text)}
			}
		case ChatBlockDivider:
			lines = []string{tuiDimStyle.Render("  ─── · ───")}
		default:
			if text != "" {
				lines = strings.Split(RenderMarkdown(text, width), "\n")
			}
		}
		out.Lines = append(out.Lines, lines...)
		out.Ranges[block.ID] = [2]int{start, len(out.Lines)}
	}
	return out
}

// renderToolBlock renders a ChatBlockTool: collapsed shows a compact one-liner,
// expanded with large content shows full output with dim style.
func renderToolBlock(block ChatBlock, text string, model string, width int) []string {
	if block.Collapsed {
		// Compact one-liner with truncated preview.
		preview := strings.ReplaceAll(SafeChatBlockText(block.Text, maxToolResultPreview), "\n", " ")
		line := fmt.Sprintf("  %s %s %s",
			toolIconForName(block.ToolName),
			toolNameStyle.Render(block.ToolName),
			tuiDimStyle.Render(preview),
		)
		return []string{line}
	}

	// Not collapsed — check if content exceeds preview limit.
	if utf8.RuneCountInString(text) > maxToolResultPreview {
		// Expanded rendering: tool name header + full content in dim style.
		header := fmt.Sprintf("  %s %s",
			toolIconForName(block.ToolName),
			toolNameStyle.Render(block.ToolName),
		)
		lines := []string{header}
		for _, line := range strings.Split(text, "\n") {
			lines = append(lines, tuiDimStyle.Render("    "+line))
		}
		return lines
	}

	// Small content: use existing compact rendering.
	toolText := block.ToolName + " " + SafeChatBlockText(block.Text, maxToolResultPreview)
	return RenderMessageForHistory(providerMessageForBlock(block, toolText), model, width)
}

// maxThinkingLines is the max visible lines for a windowed thinking block.
const maxThinkingLines = 6

func renderThinkingBlock(text string, collapsed bool, scrollOffset int) []string {
	if collapsed || strings.TrimSpace(text) == "" {
		return []string{tuiThinkingStyle.Render("  ▸ thinking")}
	}

	allLines := strings.Split(SafeChatBlockText(text, 0), "\n")
	n := len(allLines)

	// Determine the window bounds.
	start := 0
	if n > maxThinkingLines {
		maxOffset := n - maxThinkingLines
		if scrollOffset < 0 {
			scrollOffset = 0
		}
		if scrollOffset > maxOffset {
			scrollOffset = maxOffset
		}
		// scrollOffset=0 → show most recent lines (bottom).
		// scrollOffset increases → scroll upward through older lines.
		start = maxOffset - scrollOffset
	}

	end := start + maxThinkingLines
	if end > n {
		end = n
	}
	window := allLines[start:end]

	var out []string
	out = append(out, tuiThinkingStyle.Render("  ▾ thinking"))

	// Show "↑ ..." if there are lines above the window.
	if start > 0 {
		out = append(out, tuiThinkingStyle.Render("    ↑ ..."))
	}

	for _, line := range window {
		if line != "" {
			out = append(out, tuiThinkingStyle.Render("    "+line))
		}
	}

	// Show "↓ ..." if there are lines below the window.
	if end < n {
		out = append(out, tuiThinkingStyle.Render("    ↓ ..."))
	}

	return out
}

func providerMessageForBlock(block ChatBlock, text string) provider.Message {
	role := provider.RoleAssistant
	if block.Kind == ChatBlockUser {
		role = provider.RoleUser
	}
	if block.Kind == ChatBlockTool {
		role = provider.RoleTool
	}
	return provider.Message{Role: role, Content: text, Name: block.ToolName, ToolCallID: block.ToolCallID}
}
