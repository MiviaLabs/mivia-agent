package clichat

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
	return RenderChatBlocksView(blocks, model, width, RailView{}, thinkingExpandDefault...)
}

// RenderChatBlocksView adds live frame/liveness for rail animation.
func RenderChatBlocksView(blocks []ChatBlock, model string, width int, view RailView, thinkingExpandDefault ...bool) ChatBlockRender {
	if width < MinCardWidth {
		width = MinCardWidth
	}
	ted := len(thinkingExpandDefault) > 0 && thinkingExpandDefault[0]
	members := buildGroupMembers(blocks)
	out := ChatBlockRender{Ranges: make(map[string][2]int)}
	for i, block := range blocks {
		mem := GroupMember{}
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
	return renderOneChatBlockMem(block, model, width, thinkingExpandDefault, GroupMember{}, RailView{})
}

func renderOneChatBlockMem(block ChatBlock, model string, width int, thinkingExpandDefault bool, mem GroupMember, view RailView) []string {
	opts := ChromeRenderOpts()
	text := SafeChatBlockText(block.Text, 0)
	rail := ResolveBlockRail(block, mem, opts, view)

	if early, ok := renderPreformattedBlock(block, rail); ok {
		return early
	}
	lines := renderBlockBody(block, text, model, width, thinkingExpandDefault)
	return ApplyBlockChromeWith(lines, block, text, opts, mem, view)
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
		return ApplyLeftRail(lines, rail), true
	case ChatBlockTool:
		if block.Collapsed {
			return ApplyLeftRail(lines, rail), true
		}
		return nil, false
	case ChatBlockSystem:
		if IsWorkStatusBlock(block) {
			return nil, false
		}
		if block.Collapsed {
			return nil, false
		}
		return lines, true
	default:
		return ApplyLeftRail(lines, rail), true
	}
}

// collapsePreview is the one-line collapsed summary for any collapsible kind.
// Uses ▸ affordance (industry collapsible-card pattern; thinking uses the same).
func collapsePreview(label, text string, maxRunes int) []string {
	preview := text
	if len([]rune(preview)) > maxRunes {
		preview = string([]rune(preview)[:maxRunes]) + "…"
	}
	return []string{"  " + GlyphTriR + " " + label + "  " + preview}
}

func renderBlockBody(block ChatBlock, text, model string, width int, thinkingExpandDefault bool) []string {
	switch block.Kind {
	case ChatBlockUser:
		// Conversation is never collapsed - see toggleSelectedBlock.
		return formatUserMessageCard(text, width, block.SentAt)
	case ChatBlockAssistant:
		// MessageBubble path: vertical + horizontal pad like user cards.
		// Falls back to history renderer only when empty (should not happen).
		if lines := AssistantBubble.Render(text, width, time.Time{}); len(lines) > 0 {
			return lines
		}
		return RenderMessageForHistory(providerMessageForBlock(block, text), model, width)
	case ChatBlockTool:
		return renderToolBlock(block, text, model, width)
	case ChatBlockThinking:
		return renderThinkingBlock(text, block.Collapsed, block.ScrollOffset, thinkingExpandDefault, width)
	case ChatBlockSystem:
		if IsWorkStatusBlock(block) {
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
			return []string{TUIDimStyle.Render("  " + text)}
		}
		return []string{TUIDimStyle.Render("  ⚙ " + text)}
	case ChatBlockDivider:
		if text != "" {
			return []string{TUIDimStyle.Render(text)}
		}
		// A bare turn rule carried no information and ate a row; the blank
		// lane between blocks already separates turns. Footers with text
		// (turn number, duration, tally, cancelled, error) still render.
		return nil
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
	marker := GlyphTriR
	if !collapsed {
		marker = glyphTriD
	}
	out := []string{"  " + marker + " " + strings.TrimSpace(parts[0])}
	if !collapsed {
		for _, line := range parts[1:] {
			if strings.TrimSpace(line) != "" {
				out = append(out, TUIDimStyle.Render("    "+line))
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
	// Nested tools keep their ◆ producing-agent badge in history.
	agentPart := ""
	if block.AgentName != "" {
		agentPart = AgentBadgeStyle.Render(GlyphDiamond+" "+block.AgentName) + " "
	}
	if block.Collapsed {
		// File edits are the agent's most consequential output: the collapsed
		// row shows a peek of the change (a few diff lines) rather than only a
		// one-line summary, so scrolling history shows what actually changed.
		if IsEditTool(block.ToolName) || ResultLooksLikeDiff(text) {
			return renderCollapsedEditBlock(block, text, agentPart, width)
		}
		// Use pre-rendered line (formatToolLine output) if available, else truncate raw text.
		preview := block.Rendered
		if preview == "" {
			preview = strings.ReplaceAll(SafeChatBlockText(block.Text, maxToolResultPreview), "\n", " ")
		}
		// Ledger-row chrome: status glyph + duration when known.
		status := ""
		if block.Failed {
			status = " " + ToolErrStyle.Render(GlyphCross)
		} else if block.Elapsed > 0 {
			status = " " + ToolOkStyle.Render(GlyphCheck)
		}
		dur := ""
		if block.Elapsed > 0 {
			dur = " " + ToolTimeStyle.Render(FormatDuration(block.Elapsed))
		}
		// ▸ collapse affordance matches other block kinds.
		line := fmt.Sprintf("  %s %s %s%s %s%s%s",
			GlyphTriR,
			ToolIconForName(block.ToolName),
			agentPart,
			ToolNameStyle.Render(block.ToolName),
			TUIDimStyle.Render(preview),
			dur,
			status,
		)
		return []string{line}
	}

	// Expanded: show full tool content with dim style + ▾ expand affordance.
	if strings.TrimSpace(text) == "" {
		return []string{fmt.Sprintf("  %s %s %s (no output)", glyphTriD, ToolIconForName(block.ToolName), ToolNameStyle.Render(block.ToolName))}
	}
	header := fmt.Sprintf("  %s %s %s",
		glyphTriD,
		ToolIconForName(block.ToolName),
		ToolNameStyle.Render(block.ToolName),
	)
	lines := []string{header}
	// Apply redaction + line cap to expanded tool content for privacy.
	redacted := RedactPreview(text)
	if IsEditTool(block.ToolName) || ResultLooksLikeDiff(redacted) {
		for _, line := range RenderDiffBody(redacted, width, 50) {
			lines = append(lines, line)
		}
		return lines
	}
	contentLines := strings.Split(redacted, "\n")
	const maxExpandedLines = 50
	if len(contentLines) > maxExpandedLines {
		extra := len(contentLines) - maxExpandedLines
		contentLines = contentLines[:maxExpandedLines]
		contentLines = append(contentLines, TUIDimStyle.Render(fmt.Sprintf("    … (%d more lines truncated)", extra)))
	}
	for _, line := range contentLines {
		lines = append(lines, TUIDimStyle.Render("    "+line))
	}
	return lines
}

// MaxThinkingLines is the max visible lines for a windowed thinking block.
const MaxThinkingLines = 6

func renderThinkingBlock(text string, collapsed bool, scrollOffset int, thinkingExpandDefault bool, width int) []string {
	// Per-block Collapsed controls visibility. thinkingExpandDefault only
	// seeds new blocks; it must not erase already-committed thinking content
	// (that made thinking flash live then disappear as "▸ thinking").
	_ = thinkingExpandDefault
	effectivelyCollapsed := collapsed
	if strings.TrimSpace(text) == "" {
		return []string{TUIThinkingStyle.Render("  " + GlyphTriR + " thinking")}
	}
	rawLines := strings.Split(SafeChatBlockText(text, 0), "\n")
	if effectivelyCollapsed {
		// Say what the fold is hiding: a bare "thinking" gave no reason to
		// open it and no sense of how much reasoning happened.
		n := 0
		for _, l := range rawLines {
			if strings.TrimSpace(l) != "" {
				n++
			}
		}
		return []string{TUIThinkingStyle.Render(fmt.Sprintf("  %s thinking · %d lines", GlyphTriR, n))}
	}
	// Wrap each raw line to the pane width before windowing, so a single long
	// reasoning line does not overflow the viewport (every other block kind
	// wraps its text; this one used to emit raw lines unwrapped).
	contentWidth := Max(20, width-4)
	allLines := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		if l == "" {
			allLines = append(allLines, "")
			continue
		}
		wrapped := WrapANSIv2(l, contentWidth)
		allLines = append(allLines, strings.Split(wrapped, "\n")...)
	}
	n := len(allLines)

	// Determine the window bounds.
	start := 0
	if n > MaxThinkingLines {
		maxOffset := n - MaxThinkingLines
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

	end := start + MaxThinkingLines
	if end > n {
		end = n
	}
	window := allLines[start:end]

	var out []string
	out = append(out, TUIThinkingStyle.Render("  "+glyphTriD+" thinking"))

	// Show "↑ ..." if there are lines above the window.
	if start > 0 {
		out = append(out, TUIThinkingStyle.Render("    ↑ ..."))
	}

	for _, line := range window {
		if line != "" {
			out = append(out, TUIThinkingStyle.Render("    "+line))
		}
	}

	// Show "↓ ..." if there are lines below the window.
	if end < n {
		out = append(out, TUIThinkingStyle.Render("    ↓ ..."))
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
