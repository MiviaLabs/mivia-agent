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
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type sessionsConfirm int

const (
	confirmNone sessionsConfirm = iota
	confirmDeleteOne
	confirmPurgeAll
)

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
	d.clampScrollTo(d.visibleRows(80, 24))
}

func (d *sessionsDialog) clampScrollTo(visible int) {
	visible = max(1, visible)
	if d.cursor < d.scroll {
		d.scroll = d.cursor
	}
	if d.cursor >= d.scroll+visible {
		d.scroll = d.cursor - visible + 1
	}
	if d.scroll < 0 {
		d.scroll = 0
	}
}

func (d *sessionsDialog) cursorRows(visible int) int {
	if d.cursor < len(d.sessions)-1 && visible > 1 {
		return visible - 1
	}
	return visible
}

func sessionsDialogPrefs() dialogPrefs {
	return dialogPrefs{preferredW: 70, minW: 40, minH: 8, frameCols: 4, frameRows: 3}
}

func (d *sessionsDialog) layout(w, h int) dialogLayout {
	return makeDialogLayout(w, h, sessionsDialogPrefs(), func(innerW int) (int, int) {
		rows := d.rowLines(innerW, len(d.sessions)+1)
		return maxSessionRowWidth(rows), len(rows)
	})
}

func (d *sessionsDialog) visibleRows(w, h int) int {
	l := d.layout(w, h)
	return max(1, l.pageH)
}

func (d *sessionsDialog) selected() (chat.SessionInfo, bool) {
	if d.cursor < 0 || d.cursor >= len(d.sessions) {
		return chat.SessionInfo{}, false
	}
	return d.sessions[d.cursor], true
}

// View renders the dialog frame.
func (d *sessionsDialog) View(w, h int) string {
	view, _ := d.ViewAt(max(1, w), max(1, h))
	return view
}

func (d *sessionsDialog) ViewAt(w, h int) (string, dialogLayout) {
	l := d.layout(w, h)
	d.clampScrollTo(d.cursorRows(l.pageH))
	rows := d.rowLines(l.innerW, l.pageH)
	return renderDialogFrame(fmt.Sprintf("◇ sessions · %d", len(d.sessions)), rows, d.footer(), l), l
}

func maxSessionRowWidth(rows []string) int {
	width := 0
	for _, row := range rows {
		width = max(width, ansi.StringWidth(row))
	}
	return width
}

func (d *sessionsDialog) rowLines(inner, visible int) []string {
	visible = max(1, visible)
	if len(d.sessions) == 0 {
		return []string{tuiDimStyle.Render("no saved sessions yet")}
	}
	var rows []string
	rowLimit := visible
	if d.scroll+rowLimit < len(d.sessions) && rowLimit > 1 {
		rowLimit--
	}
	end := min(len(d.sessions), d.scroll+rowLimit)
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
	if more := len(d.sessions) - end; more > 0 && len(rows) < visible {
		rows = append(rows, tuiDimStyle.Render(fmt.Sprintf("  … %d more", more)))
	} else if more > 0 && visible == 1 && len(rows) == 1 {
		if inner <= 1 {
			// A one-cell canvas cannot show both text and a count. This
			// combined affordance preserves the cursor/more state without
			// violating the exact terminal-cell contract.
			rows[0] = "↕"
			return rows
		}
		// There is no spare row for the indicator on a one-row canvas. Keep
		// the cursor row visible and put the indicator first so fitting cannot
		// erase the fact that more sessions remain below it.
		body := strings.TrimSpace(strings.TrimPrefix(stripANSI(rows[0]), "▸"))
		prefix := "▸ … " + strconv.Itoa(more) + " more "
		rows[0] = tuiAccentStyle.Render("▸ ") + tuiDimStyle.Render("… "+strconv.Itoa(more)+" more ") +
			truncateToWidth(body, max(1, inner-lipgloss.Width(prefix)))
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
		return tuiDimStyle.Render("open · delete · purge · ") + tuiInfoStyle.Render(d.notice)
	}
	return tuiDimStyle.Render("↑↓ move · enter open · d delete · P purge all · esc close")
}

// ─── Model wiring ─────────────────────────────────────────────────────

func (m *tuiModel) openSessionsDialog() {
	// Refresh from the store when it can be read, including an empty result.
	// On a transient read error, preserve the last known list so an error is
	// not presented as "you have no sessions" and mistaken for data loss.
	list, err := m.session.ListSessions()
	if err == nil {
		m.sessions = list
	}
	m.setSessionsDialog(newSessionsDialog(m.sessions))
	if err != nil {
		m.sessionsDlg.notice = "refresh failed: " + err.Error()
	}
}

// handleSessionsDialogKey routes keys while the manager is open. Every key
// is consumed: the dialog owns the screen until dismissed.
func (m *tuiModel) handleSessionsDialogKey(key string) (bool, bool, []tea.Cmd) {
	d := m.sessionsDlg
	visible := d.cursorRows(d.visibleRows(max(1, m.width), max(1, m.height)))
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
		m.setSessionsDialog(nil)
	case "up", "k":
		d.move(-1)
		d.clampScrollTo(visible)
	case "down", "j":
		d.move(1)
		d.clampScrollTo(visible)
	case "home", "g":
		d.move(-len(d.sessions))
		d.clampScrollTo(visible)
	case "end", "G":
		d.move(len(d.sessions))
		d.clampScrollTo(visible)
	case "enter":
		if s, ok := d.selected(); ok {
			m.setSessionsDialog(nil)
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
