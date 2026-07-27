package cli

import "strings"

func (m *tuiModel) renderThinkingBlock(id, text string) string {
	rendered := RenderChatBlocks([]ChatBlock{{ID: id, Kind: ChatBlockThinking, Text: text, Collapsed: !m.showThinking}}, m.modelName, max(40, m.width-2))
	return strings.Join(rendered.Lines, "\n")
}

func appendThinkingContent(content, thinking string) string {
	if content != "" {
		content += "\n"
	}
	return content + thinking
}
