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
	return m.renderChatView()
}

func (m *tuiModel) renderChatView() string {
	open, done, total := countTools(m.toolRows)
	phase := deriveBrandPhase(m.waiting, open, m.streamBuf.Len(), len(m.pendingQueue), false)

	header := renderStatusBar(
		m.logoFrame, phase, m.modelName, m.waiting, time.Since(m.turnStart),
		open, done, total, len(m.pendingQueue), m.session.MessagesCount(), m.width,
		m.stepDetail,
	)

	layout := m.chatViewLayout(header)
	termH, input, hint, toolMaxLines := layout.termH, layout.input, layout.hint, layout.toolMaxLines
	vpH := layout.viewportHeight
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
	toolY0, toolY1 := 1, 0
	if toolStrip != "" {
		toolY0 = lipgloss.Height(header) + lipgloss.Height(body)
		toolY1 = toolY0 + lipgloss.Height(toolStrip) - 1
	}
	composerY0 := lipgloss.Height(header) + lipgloss.Height(body) + lipgloss.Height(toolStrip)
	composerY1 := composerY0 + lipgloss.Height(input) + lipgloss.Height(hint) - 1
	m.hitMap.rebuild(m.width, termH, lipgloss.Height(header), lipgloss.Height(body), toolY0, toolY1, composerY0, composerY1, m.chatBlockRanges, m.viewport.YOffset)

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

type chatViewLayout struct {
	termH, viewportHeight, toolMaxLines int
	input, hint                         string
}

func (m *tuiModel) chatViewLayout(header string) chatViewLayout {
	const minVp = 2
	termH := max(8, m.height)
	inputH := min(composerMaxHeight(termH), max(3, m.textarea.LineCount()+1))
	for inputH > 2 {
		m.textarea.SetHeight(inputH)
		m.textarea.SetWidth(composerInnerWidth(m.width))
		probe := renderComposer(m.textarea.View(), m.width, m.waiting, len(m.pendingQueue), m.focus == focusComposer, m.stepDetail, m.stalledWarning)
		if lipgloss.Height(header)+lipgloss.Height(probe)+1+minVp <= termH {
			break
		}
		inputH--
	}
	m.textarea.SetHeight(inputH)
	m.textarea.SetWidth(composerInnerWidth(m.width))
	input := renderComposer(m.textarea.View(), m.width, m.waiting, len(m.pendingQueue), m.focus == focusComposer, m.stepDetail, m.stalledWarning)
	hintParts := []string{" enter send · alt+enter newline · ctrl+c quit "}
	if m.waiting {
		hintParts[0] = " type to queue · enter queue · ctrl+c cancel "
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
	remain := max(minVp, termH-lipgloss.Height(header)-lipgloss.Height(input)-lipgloss.Height(hint))
	toolMaxLines := 0
	if m.waiting && len(m.toolRows) > 0 {
		toolMaxLines = min(m.calcToolPanelLines(), max(2, remain/3))
		if remain-toolMaxLines < minVp {
			toolMaxLines = max(0, remain-minVp)
		}
	}
	return chatViewLayout{termH: termH, viewportHeight: max(minVp, remain-toolMaxLines), toolMaxLines: toolMaxLines, input: input, hint: hint}
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
	logoLines := strings.Count(logo, "\n") + 1
	tag := tuiDimStyle.Render("type a message to start · select a session to resume")
	tag = lipgloss.PlaceHorizontal(w, lipgloss.Center, tag)

	return m.renderWelcomeBody(w, h, status, logo, word, tag, logoLines)
}

func (m *tuiModel) renderWelcomeBody(w, h int, status, logo, word, tag string, logoLines int) string {
	// Composer card (border chrome outside textarea height).
	inputH := min(composerMaxHeight(h), max(3, m.textarea.LineCount()+1))
	m.textarea.SetWidth(composerInnerWidth(w))
	m.textarea.SetHeight(inputH)
	input := renderComposer(m.textarea.View(), w, false, 0, true, "", false)
	inputLines := lipgloss.Height(input)
	hint := tuiDimStyle.Render(" ↑↓ sessions · enter open · type+enter new · ctrl+c quit ")

	// Vertical budget for session list — never exceed terminal height.
	// fixedNoPicker = status(1) + body_pre(logoLines + 6) + blank(1) + input(inputLines) + hint(1)
	// body pre-picker: 4 blanks + logo + word + tag = logoLines + 6
	fixedNoPicker := logoLines + inputLines + 9
	// Shrink composer if total fixed height exceeds terminal.
	for inputH > 2 && fixedNoPicker > h {
		inputH--
		m.textarea.SetHeight(inputH)
		input = renderComposer(m.textarea.View(), w, false, 0, true, "", false)
		inputLines = lipgloss.Height(input)
		fixedNoPicker = logoLines + inputLines + 9
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
