package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"strings"
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
		if block.Collapsed {
			out.Lines = append(out.Lines, "  … "+string(block.Kind))
			out.Ranges[block.ID] = [2]int{start, len(out.Lines)}
			continue
		}
		text := SafeChatBlockText(block.Text, 0)
		var lines []string
		switch block.Kind {
		case ChatBlockUser, ChatBlockAssistant, ChatBlockTool:
			if block.Kind == ChatBlockTool {
				text = block.ToolName + " " + SafeChatBlockText(block.Text, maxToolResultPreview)
			}
			lines = RenderMessageForHistory(providerMessageForBlock(block, text), model, width)
		case ChatBlockThinking:
			lines = renderThinkingBlock(text, block.Collapsed)
		case ChatBlockSystem:
			if text != "" {
				lines = []string{tuiDimStyle.Render("  ⚙ " + text)}
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
