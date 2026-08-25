// Package blackboard renders the interactive run blackboard and inter-agent
// messaging center in the Terminal UI.
package blackboard

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// maxVisibleRows is the maximum number of items shown at once.
const maxVisibleRows = 4

// Tab identifies the active view in the blackboard overlay.
type Tab int

const (
	TabFindings Tab = 0
	TabMessages Tab = 1
)

// Finding represents one durable discovery or claim on the run blackboard.
type Finding struct {
	Agent string
	Claim string
	Refs  []string
	At    time.Time
}

// Message represents one direct inter-agent steer, question, ask, or answer.
type Message struct {
	From string
	To   string
	Kind string
	Body string
	At   time.Time
}

// Model manages blackboard discoveries and inter-agent communication logs.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	tab      Tab
	findings []Finding
	messages []Message

	fCursor int
	fOffset int
	mCursor int
	mOffset int

	active bool
	width  int
}

// New returns an empty blackboard model.
func New(t theme.Theme, tier theme.Tier) Model {
	return Model{Theme: t, Tier: tier}
}

// Active reports whether the overlay is currently open.
func (m Model) Active() bool {
	return m.active
}

// SetWidth updates the available width.
func (m *Model) SetWidth(w int) {
	m.width = w
}

// AddFinding records a durable discovery on the run blackboard.
func (m *Model) AddFinding(agent, claim string, refs []string) {
	if strings.TrimSpace(claim) == "" {
		return
	}
	m.findings = append(m.findings, Finding{
		Agent: agent,
		Claim: claim,
		Refs:  refs,
		At:    time.Now(),
	})
	m.adjustOffset()
}

// AddMessage records an inter-agent steer, question, ask, or answer.
func (m *Model) AddMessage(from, to, kind, body string) {
	if strings.TrimSpace(body) == "" {
		return
	}
	m.messages = append(m.messages, Message{
		From: from,
		To:   to,
		Kind: kind,
		Body: body,
		At:   time.Now(),
	})
	m.adjustOffset()
}

// FindingsCount returns the number of recorded findings.
func (m Model) FindingsCount() int {
	return len(m.findings)
}

// MessagesCount returns the number of recorded messages.
func (m Model) MessagesCount() int {
	return len(m.messages)
}

// Open activates the overlay.
func (m *Model) Open() {
	m.active = true
	m.adjustOffset()
}

// Close dismisses the overlay.
func (m *Model) Close() {
	m.active = false
}

// ToggleTab switches between the Findings and Messages tabs.
func (m *Model) ToggleTab() {
	if m.tab == TabFindings {
		m.tab = TabMessages
	} else {
		m.tab = TabFindings
	}
	m.adjustOffset()
}

// Up moves the selection cursor up in the active tab.
func (m *Model) Up() {
	if !m.active {
		return
	}
	if m.tab == TabFindings {
		if m.fCursor > 0 {
			m.fCursor--
		}
	} else {
		if m.mCursor > 0 {
			m.mCursor--
		}
	}
	m.adjustOffset()
}

// Down moves the selection cursor down in the active tab.
func (m *Model) Down() {
	if !m.active {
		return
	}
	if m.tab == TabFindings {
		if m.fCursor < len(m.findings)-1 {
			m.fCursor++
		}
	} else {
		if m.mCursor < len(m.messages)-1 {
			m.mCursor++
		}
	}
	m.adjustOffset()
}

func (m *Model) adjustOffset() {
	if m.tab == TabFindings {
		clampOffset(&m.fCursor, &m.fOffset, len(m.findings))
	} else {
		clampOffset(&m.mCursor, &m.mOffset, len(m.messages))
	}
}

func clampOffset(cursor, offset *int, total int) {
	if total == 0 {
		*cursor = 0
		*offset = 0
		return
	}
	if *cursor >= total {
		*cursor = total - 1
	}
	if *cursor < 0 {
		*cursor = 0
	}
	if *cursor < *offset {
		*offset = *cursor
	}
	if *cursor >= *offset+maxVisibleRows {
		*offset = *cursor - maxVisibleRows + 1
	}
	maxOff := total - maxVisibleRows
	if maxOff < 0 {
		maxOff = 0
	}
	if *offset > maxOff {
		*offset = maxOff
	}
	if *offset < 0 {
		*offset = 0
	}
}

// Height returns the total terminal rows claimed by the overlay.
func (m Model) Height() int {
	if !m.active {
		return 0
	}
	total := len(m.findings)
	if m.tab == TabMessages {
		total = len(m.messages)
	}
	if total == 0 {
		return 3 // 1 empty row + 2 border rows
	}
	return min(total, maxVisibleRows) + 2
}

// View renders the blackboard / messaging overlay.
func (m Model) View() string {
	if !m.active {
		return ""
	}

	accent := render.Role(m.Theme, m.Tier, theme.RoleAccent)
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)

	tab1 := fmt.Sprintf("📌 Findings (%d)", len(m.findings))
	tab2 := fmt.Sprintf("✉ Messages (%d)", len(m.messages))
	if m.tab == TabFindings {
		tab1 = accent.Bold(true).Render("[" + tab1 + "]")
		tab2 = subtle.Render(tab2)
	} else {
		tab1 = subtle.Render(tab1)
		tab2 = accent.Bold(true).Render("[" + tab2 + "]")
	}

	label := fmt.Sprintf("[ %s  %s  •  Tab: switch  •  Esc: close ]", tab1, tab2)
	if !render.HintFits(m.width, label) {
		label = fmt.Sprintf("[ 📋 Blackboard (%d/%d) ]", len(m.findings), len(m.messages))
	}

	innerWidth := m.width - 4
	if innerWidth < 10 {
		innerWidth = 10
	}

	var rows []string
	if m.tab == TabFindings {
		rows = m.renderFindingsRows(innerWidth)
	} else {
		rows = m.renderMessagesRows(innerWidth)
	}

	body := strings.Join(rows, "\n")
	body = render.FillBG(m.Theme, m.Tier, theme.RoleBGSubtle, body)
	return render.BorderedWithHint(m.Theme, m.Tier, theme.RoleBorder, theme.RoleAccent, m.width, body, label)
}

func (m Model) renderFindingsRows(innerWidth int) []string {
	muted := render.Role(m.Theme, m.Tier, theme.RoleFGMuted)
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	warning := render.Role(m.Theme, m.Tier, theme.RoleWarning)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)

	if len(m.findings) == 0 {
		return []string{"  " + muted.Render("No findings posted yet on the run blackboard.")}
	}

	var rows []string
	end := min(m.fOffset+maxVisibleRows, len(m.findings))
	for i := m.fOffset; i < end; i++ {
		f := m.findings[i]
		prefix := "  "
		style := fg
		if i == m.fCursor {
			prefix = warning.Render("📌 ")
			style = fg.Bold(true)
		} else {
			prefix = subtle.Render("•  ")
		}

		author := subtle.Render("@" + f.Agent + ": ")
		preview := strings.ReplaceAll(f.Claim, "\n", " ⏎ ")
		available := innerWidth - ansi.StringWidth(prefix) - ansi.StringWidth(author)
		if available > 0 && ansi.StringWidth(preview) > available {
			preview = ansi.Truncate(preview, available, "…")
		}
		rows = append(rows, prefix+author+style.Render(preview))
	}
	return rows
}

func (m Model) renderMessagesRows(innerWidth int) []string {
	muted := render.Role(m.Theme, m.Tier, theme.RoleFGMuted)
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	accent := render.Role(m.Theme, m.Tier, theme.RoleAccent)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)

	if len(m.messages) == 0 {
		return []string{"  " + muted.Render("No inter-agent messages recorded in this run.")}
	}

	var rows []string
	end := min(m.mOffset+maxVisibleRows, len(m.messages))
	for i := m.mOffset; i < end; i++ {
		msg := m.messages[i]
		prefix := "  "
		style := fg
		if i == m.mCursor {
			prefix = accent.Render("✉ ")
			style = fg.Bold(true)
		} else {
			prefix = subtle.Render("• ")
		}

		routing := subtle.Render(fmt.Sprintf("[%s] @%s➔@%s: ", msg.Kind, msg.From, msg.To))
		preview := strings.ReplaceAll(msg.Body, "\n", " ⏎ ")
		available := innerWidth - ansi.StringWidth(prefix) - ansi.StringWidth(routing)
		if available > 0 && ansi.StringWidth(preview) > available {
			preview = ansi.Truncate(preview, available, "…")
		}
		rows = append(rows, prefix+routing+style.Render(preview))
	}
	return rows
}
