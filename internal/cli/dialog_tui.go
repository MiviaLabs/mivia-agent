// TUI dialogs: command output that is *reference material*, not conversation.
//
// /help, /status and /tools used to append walls of text into the transcript,
// where they stayed forever and pushed real messages out of view. They are
// now closable dialogs over the chat frame, sharing one surface, one key
// model (esc/q close, j/k/pgup/pgdn scroll) and one look with the block and
// fleet detail overlays - all of them are blockOverlay values.
package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/charmbracelet/lipgloss"
)

// newDialog builds a scrollable dialog from pre-rendered lines.
func newDialog(title string, lines []string) *blockOverlay {
	return &blockOverlay{title: title, lines: append([]string(nil), lines...), prefs: dialogPrefsForTitle(title)}
}

func dialogPrefsForTitle(title string) dialogPrefs {
	switch title {
	case "? help":
		return dialogPrefs{preferredW: 76, preferredHPct: 70, minW: 40, minH: 8, frameCols: 4, frameRows: 2, pager: true}
	case "◇ status", "◇ status · captured at open":
		return dialogPrefs{preferredW: 60, minW: 32, minH: 8, frameCols: 4, frameRows: 2}
	case "⚙ tools":
		return dialogPrefs{preferredW: 50, preferredHPct: 60, minW: 28, minH: 8, frameCols: 4, frameRows: 2, pager: true}
	default:
		return detailDialogPrefs()
	}
}

// newHelpDialog renders the TUI's categorized help content. It must read
// tuiHelpContent, never the classic REPL's replHelpContent: the two surfaces
// bind different keys, and sharing a source is how /help once advertised
// "Ctrl+U kill line" and "Ctrl+D exit" in a UI that swallows both.
func newHelpDialog(_ ...int) *blockOverlay {
	return newHelpDialogFor(nil)
}

func newHelpDialogFor(registry *skills.Registry, _ ...int) *blockOverlay {
	var lines []string
	for i, section := range tuiHelpContentFor(registry) {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, lipgloss.NewStyle().Bold(true).Render(section.title))
		for _, item := range section.items {
			pad := 26 - lipgloss.Width(item.key)
			if pad < 1 {
				pad = 1
			}
			lines = append(lines, "  "+item.key+strings.Repeat(" ", pad)+tuiDimStyle.Render(item.desc))
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
		row("model", safeDialogText(m.modelName)),
		row("agent", safeDialogText(currentAgentDisplayName(m.agentState))),
		row("source", safeDialogText(currentAgentDisplaySource(m.agentState))),
		row("generation", fmt.Sprintf("%d", m.session.CurrentModelGeneration())),
		row("workspace", safeDialogText(m.workspaceDir)),
		row("messages", fmt.Sprintf("%d", m.session.MessagesCount())),
		row("turns", fmt.Sprintf("%d", m.session.UserTurns())),
		row("blocks", fmt.Sprintf("%d", len(m.blocks))),
	}
	usage := m.session.ContextUsage()
	lines = append(lines, row("context", fmt.Sprintf("%d%% · %s/%s window · %s output", usage.Percent, chat.FormatTokenK(usage.UsedTokens), chat.FormatTokenK(usage.ContextWindowTokens), chat.FormatTokenK(usage.OutputReserveTokens))))
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
				name := safeDialogText(r.Name)
				lines = append(lines, "    "+agentBadgeStyle.Render("◆ "+name)+
					tuiDimStyle.Render(fmt.Sprintf("  %d done, %d open", r.ToolsDone, r.ToolsOpen)))
			}
		}
	}
	if len(m.pendingQueue) > 0 {
		lines = append(lines, "", row("queued", fmt.Sprintf("%d messages", len(m.pendingQueue))))
	}
	d := newDialog("◇ status · captured at open", lines)
	d.kind = "status"
	return d
}

// newToolsDialog lists the tools available to the model this session.
func (m *tuiModel) newToolsDialog(names []string) *blockOverlay {
	lines := []string{tuiDimStyle.Render(fmt.Sprintf("%d tools available", len(names)))}
	// The advertised schema mass is the operator-facing justification for the
	// deferred tier, so the default surface reports it alongside the list.
	if mass := m.agentState.schemaMassSnapshot(); mass.Advertised > 0 {
		lines = append(lines, tuiDimStyle.Render(safeDialogText(mass.String())))
	}
	lines = append(lines, "")
	for _, n := range names {
		name := safeDialogText(n)
		lines = append(lines, "  "+toolIconForName(name)+" "+name)
	}
	return newDialog("⚙ tools", lines)
}
