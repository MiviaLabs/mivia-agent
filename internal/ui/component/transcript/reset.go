package transcript

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// handleAssistantReset discards the answer of an attempt that is being
// re-driven from the beginning.
//
// Two producers reach here: a schema retry that rejected the reply, and a
// re-entry after the prompt was trimmed. Both send the whole answer again.
// Without the discard the reader sees the rejected attempt with its
// replacement appended after it, and no way to tell which one the agent
// actually acted on.
//
// What it removes is exactly the assistant PROSE of the abandoned attempt:
// the in-flight span, plus the trailing committed prose blocks. It stops at
// the first block that is not prose. Tool calls, reasoning, and hook rows
// record work that really happened and is not re-driven, so removing them
// would delete the record of the very thing the retry reacted to.
func (m Model) handleAssistantReset(b uievent.AssistantResetBody) (Model, tea.Cmd) {
	had := m.pending != "" && m.pendingKind == uievent.KindTextDelta
	m.clearPending()

	blocks := m.blocks
	cut := len(blocks)
	for cut > 0 && blocks[cut-1].Kind == uievent.KindTextEnd {
		cut--
	}
	if cut < len(blocks) {
		had = true
		m.blocks = slices.Clone(blocks[:cut])
		m.focus = -1
		m.invalidateSelection()
		m.clampOffset()
	}
	if !had {
		// Nothing was on screen to discard, so a line saying something was
		// discarded would be the transcript inventing an event.
		return m, nil
	}
	return m.pushBlock(noticeBlockValue(uievent.NoticeBody{Text: resetNotice(b.Reason)}))
}

// resetNotice is the one line the reader gets in place of the removed text.
// Reason is a content-free classification, so it is safe to print verbatim.
func resetNotice(reason string) string {
	if reason == "" {
		return "discarded the previous reply; the agent is answering again"
	}
	return "discarded the previous reply (" + reason + "); the agent is answering again"
}
