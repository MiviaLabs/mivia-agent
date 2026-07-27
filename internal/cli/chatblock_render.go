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
	ted := len(thinkingExpandDefault) > 0 && thinkingExpandDefault[0]
	out := ChatBlockRender{Ranges: make(map[string][2]int)}
	for _, block := range blocks {
		start := len(out.Lines)
		lines := renderOneChatBlock(block, model, width, ted)
		out.Lines = append(out.Lines, lines...)
		if block.ID != "" {
			out.Ranges[block.ID] = [2]int{start, len(out.Lines)}
		}
	}
	return out
}

// renderOneChatBlock paints a single block and applies static left-rail chrome.
func renderOneChatBlock(block ChatBlock, model string, width int, thinkingExpandDefault bool) []string {
	opts := chromeRenderOpts()
	text := SafeChatBlockText(block.Text, 0)

	// Preformatted production lines (e.g. tools with formatToolLine in Rendered)
	// still need rails — do not skip chrome for Rendered tools.
	if block.Rendered != "" {
		line := SafeChatBlockText(block.Rendered, 0)
		lines := []string{line}
		switch block.Kind {
		case ChatBlockDivider:
			return applyLeftRailHeader(lines, railForDividerText(block.Text, opts))
		case ChatBlockTool:
			return applyLeftRailHeader(lines, railForBlock(ChatBlockTool, blockToolFailed(block), opts))
		case ChatBlockSystem:
			// Preformatted status (→) keeps its own marker.
			return lines
		default:
			return applyLeftRailHeader(lines, railForBlock(block.Kind, false, opts))
		}
	}

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
		// Unstyled prefix so injectRailOnLine can replace leading spaces width-neutrally.
		lines = renderThinkingBlock(text, block.Collapsed, block.ScrollOffset, thinkingExpandDefault)
	case ChatBlockSystem:
		if text != "" {
			if strings.HasPrefix(strings.TrimSpace(text), "→") {
				lines = []string{tuiDimStyle.Render("  " + text)}
			} else {
				lines = []string{tuiDimStyle.Render("  ⚙ " + text)}
			}
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

	// System → work status already has the product marker.
	if block.Kind == ChatBlockSystem && strings.HasPrefix(strings.TrimSpace(text), "→") {
		return lines
	}
	rail := railForBlock(block.Kind, blockToolFailed(block), opts)
	if block.Kind == ChatBlockDivider {
		rail = railForDividerText(text, opts)
	}
	return applyLeftRailHeader(lines, rail)
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
