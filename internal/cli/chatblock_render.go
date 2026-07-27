package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type ChatBlockRender struct {
	Lines  []string
	Ranges map[string][2]int
}

func RenderChatBlocks(blocks []ChatBlock, model string, width int, thinkingExpandDefault ...bool) ChatBlockRender {
	return RenderChatBlocksView(blocks, model, width, railView{}, thinkingExpandDefault...)
}

// RenderChatBlocksView adds live frame/liveness for rail animation.
func RenderChatBlocksView(blocks []ChatBlock, model string, width int, view railView, thinkingExpandDefault ...bool) ChatBlockRender {
	if width < 20 {
		width = 20
	}
	ted := len(thinkingExpandDefault) > 0 && thinkingExpandDefault[0]
	members := buildGroupMembers(blocks)
	out := ChatBlockRender{Ranges: make(map[string][2]int)}
	for i, block := range blocks {
		mem := groupMember{}
		if i < len(members) {
			mem = members[i]
		}
		appendRenderedBlockMem(&out, block, model, width, ted, mem, view)
	}
	return out
}

// ensureBlockGap inserts one blank line between successive bubble groups so
// messages are not stacked flush against each other.
func ensureBlockGap(out *ChatBlockRender) {
	if len(out.Lines) == 0 {
		return
	}
	if out.Lines[len(out.Lines)-1] != "" {
		out.Lines = append(out.Lines, "")
	}
}

// renderOneChatBlock paints a single block and applies hierarchical rail chrome.
func renderOneChatBlock(block ChatBlock, model string, width int, thinkingExpandDefault bool) []string {
	return renderOneChatBlockMem(block, model, width, thinkingExpandDefault, groupMember{}, railView{})
}

func renderOneChatBlockMem(block ChatBlock, model string, width int, thinkingExpandDefault bool, mem groupMember, view railView) []string {
	opts := chromeRenderOpts()
	text := SafeChatBlockText(block.Text, 0)
	rail := resolveBlockRail(block, mem, opts, view)

	if early, ok := renderPreformattedBlock(block, rail); ok {
		return early
	}
	lines := renderBlockBody(block, text, model, width, thinkingExpandDefault)
	return applyBlockChromeWith(lines, block, text, opts, mem, view)
}

// renderPreformattedBlock handles block.Rendered early exits.
func renderPreformattedBlock(block ChatBlock, rail LeftRail) ([]string, bool) {
	if block.Rendered == "" {
		return nil, false
	}
	line := SafeChatBlockText(block.Rendered, 0)
	lines := []string{line}
	switch block.Kind {
	case ChatBlockDivider:
		return applyLeftRail(lines, rail), true
	case ChatBlockTool:
		if block.Collapsed {
			return applyLeftRail(lines, rail), true
		}
		return nil, false
	case ChatBlockSystem:
		if isWorkStatusBlock(block) {
			return nil, false
		}
		if block.Collapsed {
			return nil, false
		}
		return lines, true
	default:
		return applyLeftRail(lines, rail), true
	}
}

// collapsePreview is the one-line collapsed summary for any collapsible kind.
// Uses ▸ affordance (industry collapsible-card pattern; thinking uses the same).
func collapsePreview(label, text string, maxRunes int) []string {
	preview := text
	if len([]rune(preview)) > maxRunes {
		preview = string([]rune(preview)[:maxRunes]) + "…"
	}
	return []string{"  ▸ " + label + "  " + preview}
}

func renderBlockBody(block ChatBlock, text, model string, width int, thinkingExpandDefault bool) []string {
	switch block.Kind {
	case ChatBlockUser:
		if block.Collapsed {
			return collapsePreview("user", text, 40)
		}
		return formatUserMessageCard(text, width, block.SentAt)
	case ChatBlockAssistant:
		if block.Collapsed {
			return collapsePreview("assistant", text, 40)
		}
		// MessageBubble path: vertical + horizontal pad like user cards.
		// Falls back to history renderer only when empty (should not happen).
		if lines := AssistantBubble.Render(text, width, time.Time{}); len(lines) > 0 {
			return lines
		}
		return RenderMessageForHistory(providerMessageForBlock(block, text), model, width)
	case ChatBlockTool:
		return renderToolBlock(block, text, model, width)
	case ChatBlockThinking:
		return renderThinkingBlock(text, block.Collapsed, block.ScrollOffset, thinkingExpandDefault)
	case ChatBlockSystem:
		if isWorkStatusBlock(block) {
			return renderWorkStatusBlock(text, block.Collapsed)
		}
		if text == "" {
			return nil
		}
		if block.Collapsed {
			if i := strings.IndexByte(text, '\n'); i >= 0 {
				text = text[:i]
			}
			return collapsePreview("system", text, 48)
		}
		if strings.HasPrefix(strings.TrimSpace(text), "→") {
			return []string{tuiDimStyle.Render("  " + text)}
		}
		return []string{tuiDimStyle.Render("  ⚙ " + text)}
	case ChatBlockDivider:
		if text != "" {
			return []string{tuiDimStyle.Render(text)}
		}
		return []string{tuiDimStyle.Render("  ─── · ───")}
	default:
		if text != "" {
			return strings.Split(RenderMarkdown(text, width), "\n")
		}
		return nil
	}
}

func renderWorkStatusBlock(text string, collapsed bool) []string {
	parts := strings.Split(text, "\n")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return nil
	}
	marker := "▸"
	if !collapsed {
		marker = "▾"
	}
	out := []string{"  " + marker + " " + strings.TrimSpace(parts[0])}
	if !collapsed {
		for _, line := range parts[1:] {
			if strings.TrimSpace(line) != "" {
				out = append(out, tuiDimStyle.Render("    "+line))
			}
		}
	}
	return out
}

// renderToolBlock renders a ChatBlockTool: collapsed shows a compact one-liner
// (from block.Rendered when available, else from block.Text truncated).
// Expanded shows the full block.Text content with dim style.
func renderToolBlock(block ChatBlock, text string, model string, width int) []string {
	_ = model
	if block.Collapsed {
		// Use pre-rendered line (formatToolLine output) if available, else truncate raw text.
		preview := block.Rendered
		if preview == "" {
			preview = strings.ReplaceAll(SafeChatBlockText(block.Text, maxToolResultPreview), "\n", " ")
		}
		// ▸ collapse affordance matches other block kinds.
		line := fmt.Sprintf("  ▸ %s %s %s",
			toolIconForName(block.ToolName),
			toolNameStyle.Render(block.ToolName),
			tuiDimStyle.Render(preview),
		)
		return []string{line}
	}

	// Expanded: show full tool content with dim style + ▾ expand affordance.
	if strings.TrimSpace(text) == "" {
		return []string{fmt.Sprintf("  ▾ %s %s (no output)", toolIconForName(block.ToolName), toolNameStyle.Render(block.ToolName))}
	}
	header := fmt.Sprintf("  ▾ %s %s",
		toolIconForName(block.ToolName),
		toolNameStyle.Render(block.ToolName),
	)
	lines := []string{header}
	// Apply redaction + line cap to expanded tool content for privacy.
	redacted := redactPreview(text)
	if isEditTool(block.ToolName) || resultLooksLikeDiff(redacted) {
		for _, line := range renderDiffBody(redacted, width, 50) {
			lines = append(lines, line)
		}
		return lines
	}
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
