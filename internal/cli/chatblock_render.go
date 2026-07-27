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
			lines = renderThinkingBlock(text, block.Collapsed)
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

func renderThinkingBlock(text string, collapsed bool) []string {
	header := tuiThinkingStyle.Render("  ▸ thinking")
	if collapsed || strings.TrimSpace(text) == "" {
		return []string{header}
	}
	lines := []string{tuiThinkingStyle.Render("  ▾ thinking")}
	for _, line := range strings.Split(SafeChatBlockText(text, 0), "\n") {
		lines = append(lines, tuiThinkingStyle.Render("    "+line))
	}
	return lines
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
