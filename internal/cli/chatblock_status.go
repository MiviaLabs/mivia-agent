package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// HydrateChatBlocksForView hydrates provider messages into chat blocks and
// reconstructs turn-local empty-speech status chrome for display.
// Never write the result into Session.Messages.
func HydrateChatBlocksForView(messages []provider.Message) []ChatBlock {
	return ReconstructEmptySpeechStatus(HydrateChatBlocks(messages))
}

// IsWorkStatusBlock reports live/reconstructed empty-speech status lines.
func IsWorkStatusBlock(b ChatBlock) bool {
	return b.Kind == ChatBlockSystem && strings.HasPrefix(strings.TrimSpace(b.Text), "→")
}

// ReconstructEmptySpeechStatus is view-only: insert dim "→ …" status before tool
// waves that lack real interim assistant speech. Mirrors live Phase A chrome.
func ReconstructEmptySpeechStatus(blocks []ChatBlock) []ChatBlock {
	if len(blocks) == 0 {
		return blocks
	}
	out := make([]ChatBlock, 0, len(blocks)+4)
	i := 0
	for i < len(blocks) {
		// Ghost interim that never would have been committed live, when tools follow.
		if blocks[i].Kind == ChatBlockAssistant && !ShouldCommitInterim(blocks[i].Text) {
			if toolWaveFollows(blocks, i+1) {
				i++
				continue
			}
		}

		// Emit thinking as-is (may lead into tools).
		if blocks[i].Kind == ChatBlockThinking {
			out = append(out, blocks[i])
			i++
			// After thinking, maybe tools (with optional status already present).
			if i < len(blocks) && (blocks[i].Kind == ChatBlockTool || IsWorkStatusBlock(blocks[i])) {
				i = appendToolWaveWithStatus(out, blocks, i, &out)
			}
			continue
		}

		if IsWorkStatusBlock(blocks[i]) {
			// Existing status: emit it then the following tool wave without second status.
			out = append(out, blocks[i])
			i++
			for i < len(blocks) && blocks[i].Kind == ChatBlockTool {
				out = append(out, blocks[i])
				i++
			}
			continue
		}

		if blocks[i].Kind == ChatBlockTool {
			i = appendToolWaveWithStatus(out, blocks, i, &out)
			continue
		}

		out = append(out, blocks[i])
		i++
	}
	return out
}

// appendToolWaveWithStatus appends optional reconstructed status + tools starting at i.
// Returns the next index after the wave. out is updated via pointer.
func appendToolWaveWithStatus(_ []ChatBlock, blocks []ChatBlock, i int, out *[]ChatBlock) int {
	// Skip existing status at wave head.
	hasStatus := false
	if i < len(blocks) && IsWorkStatusBlock(blocks[i]) {
		hasStatus = true
		*out = append(*out, blocks[i])
		i++
	}
	var names, details []string
	firstID := ""
	start := i
	for i < len(blocks) && blocks[i].Kind == ChatBlockTool {
		if firstID == "" {
			firstID = blocks[i].ID
		}
		names = append(names, blocks[i].ToolName)
		details = append(details, blocks[i].Text)
		i++
	}
	if len(names) == 0 {
		return i
	}
	if !hasStatus && !hasRealInterimBefore(*out) {
		if st := statusBlockForTools(names, details, firstID); st.Text != "" {
			*out = append(*out, st)
		}
	}
	for k := start; k < i; k++ {
		*out = append(*out, blocks[k])
	}
	return i
}

func toolWaveFollows(blocks []ChatBlock, from int) bool {
	for i := from; i < len(blocks); i++ {
		switch blocks[i].Kind {
		case ChatBlockTool:
			return true
		case ChatBlockThinking:
			continue
		case ChatBlockSystem:
			if IsWorkStatusBlock(blocks[i]) {
				continue
			}
			return false
		default:
			return false
		}
	}
	return false
}

func hasRealInterimBefore(out []ChatBlock) bool {
	for i := len(out) - 1; i >= 0; i-- {
		switch out[i].Kind {
		case ChatBlockAssistant:
			return ShouldCommitInterim(out[i].Text)
		case ChatBlockUser, ChatBlockDivider:
			return false
		case ChatBlockThinking, ChatBlockSystem, ChatBlockTool:
			continue
		default:
			return false
		}
	}
	return false
}

func statusBlockForTools(names, details []string, firstID string) ChatBlock {
	if len(names) == 0 {
		return ChatBlock{}
	}
	var body string
	if len(names) == 1 {
		body = toolStatusLine(names[0], details[0])
	} else {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Running %d tools…", len(names)))
		for i, name := range names {
			detail := ""
			if i < len(details) {
				detail = details[i]
			}
			line := toolStatusLine(name, detail)
			if line == "" {
				line = name
			}
			b.WriteByte('\n')
			b.WriteString("· " + line)
		}
		body = b.String()
	}
	if body == "" {
		return ChatBlock{}
	}
	// Multi-line body: start collapsed so Enter/Space expands per-tool list.
	collapsed := strings.Contains(body, "\n")
	summary := body
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		summary = body[:i]
	}
	text := "→ " + body
	id := "status-reconstruct"
	if firstID != "" {
		id = "status-" + firstID
	}
	return ChatBlock{
		ID:        id,
		Kind:      ChatBlockSystem,
		Text:      text,
		Rendered:  TUIDimStyle.Render("  → " + summary),
		Collapsed: collapsed,
	}
}
