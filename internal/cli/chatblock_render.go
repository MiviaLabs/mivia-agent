package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type ChatBlockRender struct {
	Lines  []string
	Ranges map[string][2]int
}

func RenderChatBlocks(blocks []ChatBlock, model string, width int, thinkingExpandDefault ...bool) ChatBlockRender {
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
		case ChatBlockUser:
			if block.Collapsed {
				lines = []string{"  … " + string(block.Kind)}
			} else {
				lines = formatUserMessageCard(text, width, block.SentAt)
			}
		case ChatBlockAssistant:
			if block.Collapsed {
				lines = []string{"  … " + string(block.Kind)}
			} else {
				lines = RenderMessageForHistory(providerMessageForBlock(block, text), model, width)
			}
		case ChatBlockTool:
			lines = renderToolBlock(block, text, model, width)
		case ChatBlockThinking:
			ted := len(thinkingExpandDefault) > 0 && thinkingExpandDefault[0]
			lines = renderThinkingBlock(text, block.Collapsed, block.ScrollOffset, ted)
		case ChatBlockSystem:
			if text != "" {
				lines = []string{tuiDimStyle.Render("  ⚙ " + text)}
			}
		case ChatBlockDivider:
			if text != "" {
				lines = []string{tuiDimStyle.Render(text)}
			} else {
				lines = []string{tuiDimStyle.Render("  ─── · ───")}
			}
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

// renderToolBlock renders a ChatBlockTool: collapsed shows a compact one-liner
// (from block.Rendered when available, else from block.Text truncated).
// Expanded shows the full block.Text content with dim style.
func renderToolBlock(block ChatBlock, text string, model string, width int) []string {
	if block.Collapsed {
		// Use pre-rendered line (formatToolLine output) if available, else truncate raw text.
		preview := block.Rendered
		if preview == "" {
			preview = strings.ReplaceAll(SafeChatBlockText(block.Text, maxToolResultPreview), "\n", " ")
		}
		line := fmt.Sprintf("  %s %s %s",
			toolIconForName(block.ToolName),
			toolNameStyle.Render(block.ToolName),
			tuiDimStyle.Render(preview),
		)
		return []string{line}
	}

	// Expanded: show full tool content with dim style.
	if strings.TrimSpace(text) == "" {
		return []string{fmt.Sprintf("  %s %s (no output)", toolIconForName(block.ToolName), toolNameStyle.Render(block.ToolName))}
	}
	header := fmt.Sprintf("  %s %s",
		toolIconForName(block.ToolName),
		toolNameStyle.Render(block.ToolName),
	)
	lines := []string{header}
	// Apply redaction + line cap to expanded tool content for privacy.
	redacted := redactPreview(text)
	contentLines := strings.Split(redacted, "\n")
	const maxExpandedLines = 50
	if len(contentLines) > maxExpandedLines {
		extra := len(contentLines) - maxExpandedLines
		contentLines = contentLines[:maxExpandedLines]
		contentLines = append(contentLines, tuiDimStyle.Render(fmt.Sprintf("    … (%d more lines truncated)", extra)))
	}
	for _, line := range contentLines {
		lines = append(lines, tuiDimStyle.Render("    "+line))
	}
	return lines
}

// maxThinkingLines is the max visible lines for a windowed thinking block.
const maxThinkingLines = 6

func renderThinkingBlock(text string, collapsed bool, scrollOffset int, thinkingExpandDefault bool) []string {
	// Per-block Collapsed controls visibility. thinkingExpandDefault only
	// seeds new blocks; it must not erase already-committed thinking content
	// (that made thinking flash live then disappear as "▸ thinking").
	_ = thinkingExpandDefault
	effectivelyCollapsed := collapsed
	if effectivelyCollapsed || strings.TrimSpace(text) == "" {
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
