// TUI dialogs: command output that is *reference material*, not conversation.
//
// /help, /status and /tools used to append walls of text into the transcript,
// where they stayed forever and pushed real messages out of view. They are
// now closable dialogs over the chat frame, sharing one surface, one key
// model (esc/q close, j/k/pgup/pgdn scroll) and one look with the block and
// fleet detail overlays — all of them are blockOverlay values.
package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// newDialog builds a scrollable dialog from pre-rendered lines.
func newDialog(title string, lines []string) *blockOverlay {
	return &blockOverlay{title: title, lines: lines}
}

// newHelpDialog renders the TUI's categorized help content. It must read
// tuiHelpContent, never the classic REPL's replHelpContent: the two surfaces
// bind different keys, and sharing a source is how /help once advertised
// "Ctrl+U kill line" and "Ctrl+D exit" in a UI that swallows both.
func newHelpDialog(width int) *blockOverlay {
	inner := max(30, min(96, width-6))
	var lines []string
	for i, section := range tuiHelpContent() {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, lipgloss.NewStyle().Bold(true).Render(section.title))
		for _, item := range section.items {
			pad := 26 - lipgloss.Width(item.key)
			if pad < 1 {
				pad = 1
			}
			desc := truncateToWidth(item.desc, max(8, inner-lipgloss.Width(item.key)-pad-2))
			lines = append(lines, "  "+item.key+strings.Repeat(" ", pad)+tuiDimStyle.Render(desc))
		}
	}
	return newDialog("? help", lines)
}

// newStatusDialog reports the live session state the operator asks about.
func (m *tuiModel) newStatusDialog() *blockOverlay {
	row := func(k, v string) string {
		pad := 14 - lipgloss.Width(k)
		if pad < 1 {
			pad = 1
		}
		return "  " + tuiDimStyle.Render(k) + strings.Repeat(" ", pad) + v
	}
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render("Session"),
		row("model", m.modelName),
		row("workspace", m.workspaceDir),
		row("messages", fmt.Sprintf("%d", m.session.MessagesCount())),
		row("turns", fmt.Sprintf("%d", m.session.UserTurns())),
		row("blocks", fmt.Sprintf("%d", len(m.blocks))),
	}
	if m.waiting {
		lines = append(lines,
			"",
			lipgloss.NewStyle().Bold(true).Render("Current turn"),
			row("elapsed", formatDuration(time.Since(m.turnStart))),
			row("tools open", fmt.Sprintf("%d", len(m.toolRows))),
		)
		if rows := m.subagents.Rows(); len(rows) > 0 {
			lines = append(lines, row("agents", fmt.Sprintf("%d", len(rows))))
			for _, r := range rows {
				lines = append(lines, "    "+agentBadgeStyle.Render("◆ "+r.Name)+
					tuiDimStyle.Render(fmt.Sprintf("  %d done, %d open", r.ToolsDone, r.ToolsOpen)))
			}
		}
	}
	if len(m.pendingQueue) > 0 {
		lines = append(lines, "", row("queued", fmt.Sprintf("%d messages", len(m.pendingQueue))))
	}
	return newDialog("◇ status", lines)
}

// newToolsDialog lists the tools available to the model this session.
func (m *tuiModel) newToolsDialog(names []string) *blockOverlay {
	lines := []string{tuiDimStyle.Render(fmt.Sprintf("%d tools available", len(names))), ""}
	for _, n := range names {
		lines = append(lines, "  "+toolIconForName(n)+" "+n)
	}
	return newDialog("⚙ tools", lines)
}
