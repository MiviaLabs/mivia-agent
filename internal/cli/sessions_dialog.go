// Session manager dialog (/sessions).
//
// Switching, deleting and purging used to be three slash commands that took
// a name you had to already know (/list, /load <name>, /delete <name>).
// This is the same set of actions over a list you can see, with the
// destructive ones behind an explicit confirmation that names what it is
// about to destroy — deleting a session is irreversible, so no single
// keystroke may do it.
package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type sessionsConfirm int

const (
	confirmNone sessionsConfirm = iota
	confirmDeleteOne
	confirmPurgeAll
)

// sessionsDialogRows is the visible window over the session list.
const sessionsDialogRows = 12

type sessionsDialog struct {
	sessions []chat.SessionInfo
	cursor   int
	scroll   int
	confirm  sessionsConfirm
	notice   string
}

func newSessionsDialog(sessions []chat.SessionInfo) *sessionsDialog {
	return &sessionsDialog{sessions: append([]chat.SessionInfo(nil), sessions...)}
}

// removeAt drops a row and keeps the cursor inside the remaining list.
func (d *sessionsDialog) removeAt(i int) {
	if i < 0 || i >= len(d.sessions) {
		return
	}
	d.sessions = append(d.sessions[:i], d.sessions[i+1:]...)
	if d.cursor >= len(d.sessions) {
		d.cursor = max(0, len(d.sessions)-1)
	}
	if len(d.sessions) == 0 {
		d.cursor = 0
	}
	d.clampScroll()
}

func (d *sessionsDialog) move(delta int) {
	if len(d.sessions) == 0 {
		return
	}
	d.cursor = min(len(d.sessions)-1, max(0, d.cursor+delta))
	d.clampScroll()
}

func (d *sessionsDialog) clampScroll() {
	if d.cursor < d.scroll {
		d.scroll = d.cursor
	}
	if d.cursor >= d.scroll+sessionsDialogRows {
		d.scroll = d.cursor - sessionsDialogRows + 1
	}
	if d.scroll < 0 {
		d.scroll = 0
	}
}

func (d *sessionsDialog) selected() (chat.SessionInfo, bool) {
	if d.cursor < 0 || d.cursor >= len(d.sessions) {
		return chat.SessionInfo{}, false
	}
	return d.sessions[d.cursor], true
}

// View renders the dialog frame.
func (d *sessionsDialog) View(w, h int) string {
	if w < 40 {
		w = 40
	}
	inner := w - 4
	border := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	var b strings.Builder

	title := fmt.Sprintf(" ◇ sessions · %d ", len(d.sessions))
	b.WriteString(border.Render("┌─"+title) +
		border.Render(strings.Repeat("─", max(0, w-3-lipgloss.Width(title)))+"┐") + "\n")

	rows := d.rowLines(inner)
	for _, r := range rows {
		b.WriteString(border.Render("│ ") + r)
		if fill := inner - lipgloss.Width(r); fill > 0 {
			b.WriteString(strings.Repeat(" ", fill))
		}
		b.WriteString(border.Render(" │") + "\n")
	}

	footer := d.footer()
	b.WriteString(border.Render("│ ") + footer)
	if fill := inner - lipgloss.Width(footer); fill > 0 {
		b.WriteString(strings.Repeat(" ", fill))
	}
	b.WriteString(border.Render(" │") + "\n")
	b.WriteString(border.Render("└" + strings.Repeat("─", max(0, w-2)) + "┘"))
	return b.String()
}

func (d *sessionsDialog) rowLines(inner int) []string {
	if len(d.sessions) == 0 {
		return []string{tuiDimStyle.Render("no saved sessions yet")}
	}
	var rows []string
	end := min(len(d.sessions), d.scroll+sessionsDialogRows)
	for i := d.scroll; i < end; i++ {
		s := d.sessions[i]
		marker := "  "
		name := s.Name
		if i == d.cursor {
			marker = tuiAccentStyle.Render("▸ ")
			name = lipgloss.NewStyle().Bold(true).Render(name)
		}
		meta := tuiDimStyle.Render(fmt.Sprintf("%s · %d msgs", formatSessionAge(s.UpdatedAt), s.MessageCount))
		line := marker + name
		gap := inner - lipgloss.Width(line) - lipgloss.Width(meta)
		if gap < 1 {
			line = truncateToWidth(line, max(8, inner-lipgloss.Width(meta)-1))
			gap = max(1, inner-lipgloss.Width(line)-lipgloss.Width(meta))
		}
		rows = append(rows, line+strings.Repeat(" ", gap)+meta)
	}
	if more := len(d.sessions) - end; more > 0 {
		rows = append(rows, tuiDimStyle.Render(fmt.Sprintf("  … %d more", more)))
	}
	return rows
}

// footer carries either the key legend or the pending confirmation, which
// always names what it is about to destroy.
func (d *sessionsDialog) footer() string {
	switch d.confirm {
	case confirmDeleteOne:
		if s, ok := d.selected(); ok {
			return tuiErrorStyle.Render(fmt.Sprintf("delete %q? ", s.Name)) +
				tuiDimStyle.Render("y confirm · n or esc cancel")
		}
	case confirmPurgeAll:
		return tuiErrorStyle.Render(fmt.Sprintf("purge ALL %d sessions? ", len(d.sessions))) +
			tuiDimStyle.Render("y confirm · n or esc cancel")
	}
	if d.notice != "" {
		return tuiInfoStyle.Render(d.notice)
	}
	return tuiDimStyle.Render("↑↓ move · enter open · d delete · P purge all · esc close")
}

// ─── Model wiring ─────────────────────────────────────────────────────

func (m *tuiModel) openSessionsDialog() {
	// Refresh from the store when it can be read, but never blank a list we
	// already have: a transient read error should not present as "you have
	// no sessions", which is indistinguishable from data loss.
	if list, err := m.session.ListSessions(); err == nil && len(list) > 0 {
		m.sessions = list
	}
	m.sessionsDlg = newSessionsDialog(m.sessions)
}

// handleSessionsDialogKey routes keys while the manager is open. Every key
// is consumed: the dialog owns the screen until dismissed.
func (m *tuiModel) handleSessionsDialogKey(key string) (bool, bool, []tea.Cmd) {
	d := m.sessionsDlg
	if d.confirm != confirmNone {
		switch key {
		case "y":
			m.applySessionsConfirm()
		case "n", "esc":
			d.confirm = confirmNone
		}
		return true, true, nil
	}
	switch key {
	case "esc", "q":
		m.sessionsDlg = nil
	case "up", "k":
		d.move(-1)
	case "down", "j":
		d.move(1)
	case "home", "g":
		d.move(-len(d.sessions))
	case "end", "G":
		d.move(len(d.sessions))
	case "enter":
		if s, ok := d.selected(); ok {
			m.sessionsDlg = nil
			if err := m.openSessionByName(s.Name); err != nil {
				m.appendInfo("open failed: " + err.Error())
				m.renderVP()
			}
		}
	case "d":
		// Destructive keys are inert with nothing to destroy.
		if _, ok := d.selected(); ok {
			d.confirm = confirmDeleteOne
		}
	case "P":
		if len(d.sessions) > 0 {
			d.confirm = confirmPurgeAll
		}
	}
	return true, true, nil
}

// applySessionsConfirm executes the armed destructive action.
func (m *tuiModel) applySessionsConfirm() {
	d := m.sessionsDlg
	switch d.confirm {
	case confirmDeleteOne:
		s, ok := d.selected()
		if !ok {
			break
		}
		if err := m.session.DeleteSession(s.Name); err != nil {
			d.notice = "delete failed: " + err.Error()
			break
		}
		d.removeAt(d.cursor)
		d.notice = fmt.Sprintf("deleted %q", s.Name)
	case confirmPurgeAll:
		failed := 0
		for _, s := range d.sessions {
			if err := m.session.DeleteSession(s.Name); err != nil {
				failed++
			}
		}
		total := len(d.sessions)
		d.sessions = nil
		d.cursor, d.scroll = 0, 0
		if failed > 0 {
			d.notice = fmt.Sprintf("purged %d of %d (%d failed)", total-failed, total, failed)
		} else {
			d.notice = fmt.Sprintf("purged %d sessions", total)
		}
	}
	d.confirm = confirmNone
	// Mirror the dialog's surviving rows into the model so the welcome
	// picker cannot keep offering sessions that were just destroyed.
	m.sessions = append([]chat.SessionInfo(nil), d.sessions...)
	if m.sessionSel >= len(m.sessions) {
		m.sessionSel = max(0, len(m.sessions)-1)
	}
}
