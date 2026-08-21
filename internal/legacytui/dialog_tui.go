// TUI dialogs: command output that is *reference material*, not conversation.
//
// /help, /status and /tools used to append walls of text into the transcript,
// where they stayed forever and pushed real messages out of view. They are
// now closable dialogs over the chat frame, sharing one surface, one key
// model (esc/q close, j/k/pgup/pgdn scroll) and one look with the block and
// fleet detail overlays - all of them are blockOverlay values.
package legacytui

import (
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
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

func dialogPrefsForTitle(title string) cli.DialogPrefs {
	switch title {
	case "? help":
		return cli.DialogPrefs{PreferredW: 76, PreferredHPct: 70, MinW: 40, MinH: 8, FrameCols: 4, FrameRows: 2, Pager: true}
	case "◇ status", "◇ status · captured at open":
		return cli.DialogPrefs{PreferredW: 60, MinW: 32, MinH: 8, FrameCols: 4, FrameRows: 2}
	case "⚙ tools":
		return cli.DialogPrefs{PreferredW: 50, PreferredHPct: 60, MinW: 28, MinH: 8, FrameCols: 4, FrameRows: 2, Pager: true}
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
	for i, section := range cli.TUIHelpContentFor(registry) {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, lipgloss.NewStyle().Bold(true).Render(section.Title))
		for _, item := range section.Items {
			pad := 26 - lipgloss.Width(item.Key)
			if pad < 1 {
				pad = 1
			}
			lines = append(lines, "  "+item.Key+strings.Repeat(" ", pad)+TUIDimStyle.Render(item.Desc))
		}
	}
	return newDialog("? help", lines)
}

// currentAgentDisplayName is the status dialog's "agent" row. Relocated from
// identity.go: this file is its sole caller. The locked read itself lives on
// AgentSessionState.DisplayName (internal/cli): this package cannot lock the
// unexported mu field directly.
func currentAgentDisplayName(state *cli.AgentSessionState) string {
	return state.DisplayName()
}

// currentAgentDisplaySource is the status dialog's "source" row. Relocated
// from identity.go: this file is its sole caller. See currentAgentDisplayName.
func currentAgentDisplaySource(state *cli.AgentSessionState) string {
	return state.DisplaySource()
}

// newStatusDialog reports the live session state the operator asks about.
func (m *TUIModel) newStatusDialog() *blockOverlay {
	row := func(k, v string) string {
		pad := 14 - lipgloss.Width(k)
		if pad < 1 {
			pad = 1
		}
		return "  " + TUIDimStyle.Render(k) + strings.Repeat(" ", pad) + v
	}
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render("Session"),
		row("model", safeDialogText(m.modelName)),
		row("effort", safeDialogText(FormatEffortStatus(m.session.ReasoningSetting(), len(m.session.ReasoningChoices()) > 0))),
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
			row("elapsed", FormatDuration(time.Since(m.turnStart))),
			row("tools open", fmt.Sprintf("%d", len(m.toolRows))),
		)
		if rows := m.subagents.Rows(); len(rows) > 0 {
			lines = append(lines, row("agents", fmt.Sprintf("%d", len(rows))))
			for _, r := range rows {
				name := safeDialogText(r.Name)
				lines = append(lines, "    "+AgentBadgeStyle.Render("◆ "+name)+
					TUIDimStyle.Render(fmt.Sprintf("  %d done, %d open", r.ToolsDone, r.ToolsOpen)))
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
func (m *TUIModel) newToolsDialog(names []string) *blockOverlay {
	lines := []string{TUIDimStyle.Render(fmt.Sprintf("%d tools available", len(names)))}
	// The advertised schema mass is the operator-facing justification for the
	// deferred tier, so the default surface reports it alongside the list.
	if mass := m.agentState.SchemaMassSnapshot(); mass.Advertised > 0 {
		lines = append(lines, TUIDimStyle.Render(safeDialogText(mass.String())))
	}
	lines = append(lines, "")
	for _, n := range names {
		name := safeDialogText(n)
		lines = append(lines, "  "+ToolIconForName(name)+" "+name)
	}
	return newDialog("⚙ tools", lines)
}
