// TUI view layout: sticky status, message-like composer, tools, welcome.
package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func (m *tuiModel) View() string {
	if !m.ready {
		return tuiAccentStyle.Render("  mivia") + tuiDimStyle.Render(" starting…")
	}
	if m.mode == modeWelcome {
		return m.viewWelcome()
	}

	open, done, total := countTools(m.toolRows)
	phase := deriveBrandPhase(m.waiting, open, m.streamBuf.Len(), len(m.pendingQueue), false)

	// --- Fixed chrome (never inside viewport) — measure first, then size body ---
	header := renderStatusBar(
		m.logoFrame, phase, m.modelName, m.waiting, time.Since(m.turnStart),
		open, done, total, len(m.pendingQueue), len(m.session.Messages), m.width, m.showThinking,
	)

	// Height budget: never paint more lines than m.height (alt-screen drops TOP = status).
	const minVp = 2
	termH := m.height
	if termH < 8 {
		termH = 8
	}
	// Textarea lines (card adds top/bottom border → +2 visual lines).
	inputH := min(composerMaxHeight(termH), max(3, m.textarea.LineCount()+1))
	for inputH > 2 {
		m.textarea.SetHeight(inputH)
		m.textarea.SetWidth(composerInnerWidth(m.width))
		probe := renderComposer(m.textarea.View(), m.width, m.waiting, len(m.pendingQueue), true)
		if lipgloss.Height(header)+lipgloss.Height(probe)+1+minVp <= termH {
			break
		}
		inputH--
	}
	m.textarea.SetHeight(inputH)
	m.textarea.SetWidth(composerInnerWidth(m.width))
	input := renderComposer(m.textarea.View(), m.width, m.waiting, len(m.pendingQueue), true)

	var hintParts []string
	if m.waiting {
		hintParts = append(hintParts, " type to queue · enter queue · ctrl+c cancel ")
	} else {
		hintParts = append(hintParts, " enter send · alt+enter newline · ctrl+c quit ")
	}
	if len(m.toolRows) > 0 {
		hintParts = append(hintParts, "· tab/space tools ")
	}
	if m.msgOffset > 0 {
		hintParts = append(hintParts, "· ↑ history ")
	}
	if len(m.pendingQueue) > 0 {
		hintParts = append(hintParts, fmt.Sprintf("· %d queued ", len(m.pendingQueue)))
	}
	hint := tuiDimStyle.Render(strings.Join(hintParts, ""))

	fixedH := lipgloss.Height(header) + lipgloss.Height(input) + lipgloss.Height(hint)
	remain := termH - fixedH
	if remain < minVp {
		remain = minVp
	}
	toolMaxLines := 0
	if m.waiting && len(m.toolRows) > 0 {
		toolMaxLines = min(m.calcToolPanelLines(), max(2, remain/3))
		if remain-toolMaxLines < minVp {
			toolMaxLines = max(0, remain-minVp)
		}
	}
	vpH := max(minVp, remain-toolMaxLines)
	m.viewport.Width = max(1, m.width)
	m.viewport.Height = vpH
	if !m.ready {
		m.ready = true
	}

	body := m.viewport.View()
	if !m.viewport.AtBottom() && m.width > 12 {
		hint = tuiDimStyle.Render(" ↓ more below · ") + hint
	}

	toolStrip := ""
	if m.waiting && len(m.toolRows) > 0 && toolMaxLines > 0 {
		yBase := lipgloss.Height(header) + lipgloss.Height(body)
		maxVis := toolMaxVisibleRows
		if toolMaxLines < maxVis+2 {
			maxVis = max(1, toolMaxLines-2)
		}
		var n int
		toolStrip, n, m.toolPanel = renderToolPanelWindow(
			m.toolRows, m.width, time.Now(), m.toolPanel, m.logoFrame, phase,
			maxVis, yBase,
		)
		if n > toolMaxLines {
			pl := strings.Split(toolStrip, "\n")
			if len(pl) > toolMaxLines {
				pl = pl[:toolMaxLines]
				pl[toolMaxLines-1] = tuiDimStyle.Render("  …")
				toolStrip = strings.Join(pl, "\n")
			}
		}
	}

	parts := []string{header, body}
	if toolStrip != "" {
		parts = append(parts, toolStrip)
	}
	parts = append(parts, input, hint)
	out := lipgloss.JoinVertical(lipgloss.Left, parts...)
	outLines := strings.Split(out, "\n")
	if len(outLines) > termH {
		// Prefer keeping status (top) — drop from middle by trimming body/tools.
		// Drop from bottom of body region: keep header + last (termH-1) lines.
		out = strings.Join(outLines[:termH], "\n")
	}
	return out
}

func (m *tuiModel) viewWelcome() string {
	w := m.width
	if w < 20 {
		w = 20
	}
	h := m.height
	if h < 10 {
		h = 10
	}

	// Status
	left := tuiAccentStyle.Render(" mivia ") + tuiDimStyle.Render(m.modelName)
	right := tuiDimStyle.Render(" welcome ")
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	spacerN := w - lw - rw
	if spacerN < 1 {
		spacerN = 1
	}
	status := left + tuiHeaderStyle.Render(strings.Repeat("─", spacerN)) + right

	// Logo (compact on short terminals) — welcome phase color (white identity).
	var logo string
	if h < 22 {
		logo = compactLogoFrameColor(m.logoFrame, w, brandColorWelcome)
	} else {
		logo = renderLogoFrameColor(m.logoFrame, w, brandColorWelcome)
	}
	word := renderWordmark(w)
	tag := tuiDimStyle.Render("type a message to start · select a session to resume")
	tag = lipgloss.PlaceHorizontal(w, lipgloss.Center, tag)

	// Composer card (border chrome outside textarea height).
	inputH := min(composerMaxHeight(h), max(3, m.textarea.LineCount()+1))
	m.textarea.SetWidth(composerInnerWidth(w))
	m.textarea.SetHeight(inputH)
	input := renderComposer(m.textarea.View(), w, false, 0, true)
	inputLines := lipgloss.Height(input)
	hint := tuiDimStyle.Render(" ↑↓ sessions · enter open · type+enter new · ctrl+c quit ")

	// Vertical budget for session list — never exceed terminal height.
	logoLines := strings.Count(logo, "\n") + 1
	// Prefer compact logo if full mark would blow the budget.
	fixedNoPicker := 1 + logoLines + 1 + 1 + 2 + inputLines + 1 + 2 // status+logo+word+tag+blanks+input+hint
	if fixedNoPicker+3 > h && h < 22 {
		// already compact path; shrink composer if needed
		for inputH > 2 && fixedNoPicker+1 > h {
			inputH--
			m.textarea.SetHeight(inputH)
			input = renderComposer(m.textarea.View(), w, false, 0, true)
			inputLines = lipgloss.Height(input)
			fixedNoPicker = 1 + logoLines + 1 + 1 + 2 + inputLines + 1 + 2
		}
	}
	maxRows := h - fixedNoPicker
	if maxRows < 0 {
		maxRows = 0
	}
	if maxRows > 12 {
		maxRows = 12
	}

	// Absolute Y of picker: after status, blank, logo, blank, word, blank, tag, blank
	yBase := 1 + 1 + logoLines + 1 + 1 + 1 + 1 + 1
	picker, hits, sc := renderSessionPicker(m.sessions, m.sessionSel, m.sessionScroll, w, maxRows, yBase)
	m.sessionHits = hits
	m.sessionScroll = sc

	body := strings.Join([]string{
		"",
		logo,
		"",
		word,
		"",
		tag,
		"",
		picker,
	}, "\n")

	out := lipgloss.JoinVertical(lipgloss.Left, status, body, "", input, hint)
	// Hard clamp: alt-screen drops top lines if we overflow.
	outLines := strings.Split(out, "\n")
	if len(outLines) > h {
		out = strings.Join(outLines[:h], "\n")
	}
	return out
}

// runTUI starts the Bubble Tea TUI program.
// Does not auto-load the last session — welcome screen lets the user choose.
