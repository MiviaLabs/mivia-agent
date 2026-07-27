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
		return renderNavBrandWordmark(0, phaseIdle) + tuiDimStyle.Render(" starting…")
	}
	if m.mode == modeWelcome {
		return m.viewWelcome()
	}
	return m.renderChatView()
}

func (m *tuiModel) renderChatView() string {
	open, done, total := countTools(m.toolRows)
	phase := deriveBrandPhase(m.waiting, open, m.streamBuf.Len(), len(m.pendingQueue), false, time.Since(m.turnStart))

	header := renderStatusBar(
		m.logoFrame, phase, m.modelName, m.waiting, time.Since(m.turnStart),
		open, done, total, len(m.pendingQueue), m.session.MessagesCount(), m.width,
		m.stepDetail,
	)

	layout := m.chatViewLayout(header)
	termH, input, hint := layout.termH, layout.input, layout.hint
	vpH := layout.viewportHeight
	m.viewport.Width = max(1, m.width)
	m.viewport.Height = vpH
	if !m.ready {
		m.ready = true
	}

	body := m.viewport.View()
	scrolledUp := !m.viewport.AtBottom()

	// Scroll indicator: appended to hint as unobtrusive " ↓ " when scrolled up.
	if scrolledUp && m.width > 12 {
		hint += renderScrollIndicator(true, m.width)
	}

	composerY0 := lipgloss.Height(header) + lipgloss.Height(body)
	// Composer card with vertical breathing room (no horizontal padding — aligns with viewport user cards).
	paddedInput := lipgloss.NewStyle().Padding(1, 0).Render(input)
	composerY1 := composerY0 + lipgloss.Height(paddedInput) + lipgloss.Height(hint) - 1
	m.hitMap.rebuild(m.width, termH, lipgloss.Height(header), lipgloss.Height(body), 1, 0, composerY0, composerY1, m.chatBlockRanges, m.viewport.YOffset)

	parts := []string{header, body, paddedInput, hint}
	out := lipgloss.JoinVertical(lipgloss.Left, parts...)
	outLines := strings.Split(out, "\n")
	if len(outLines) > termH {
		// Prefer keeping status (top) — drop from middle by trimming body.
		out = strings.Join(outLines[:termH], "\n")
	}
	return out
}

type chatViewLayout struct {
	termH, viewportHeight int
	input, hint           string
}

func (m *tuiModel) chatViewLayout(header string) chatViewLayout {
	const minVp = 2
	const padRows = 2 // 1 top + 1 bottom padding around composer box
	termH := max(8, m.height)
	composerW := max(18, m.width-2) // leave 1 col left + right for padding
	inputH := min(composerMaxHeight(termH), max(3, m.textarea.LineCount()+1))
	for inputH > 2 {
		m.textarea.SetHeight(inputH)
		m.textarea.SetWidth(composerInnerWidth(composerW))
		probe := renderComposer(m.textarea.View(), composerW, m.waiting, len(m.pendingQueue), m.focus == focusComposer, m.stepDetail, m.stalledWarning)
		if lipgloss.Height(header)+lipgloss.Height(probe)+1+minVp+padRows <= termH {
			break
		}
		inputH--
	}
	m.textarea.SetHeight(inputH)
	m.textarea.SetWidth(composerInnerWidth(composerW))
	input := renderComposer(m.textarea.View(), composerW, m.waiting, len(m.pendingQueue), m.focus == focusComposer, m.stepDetail, m.stalledWarning)
	hintParts := []string{" enter send · alt+enter newline · ctrl+c quit "}
	if m.waiting {
		hintParts[0] = " type to queue · enter queue · ctrl+c cancel "
	}
	if m.msgOffset > 0 {
		hintParts = append(hintParts, "· ↑ history ")
	}
	if len(m.pendingQueue) > 0 {
		hintParts = append(hintParts, fmt.Sprintf("· %d queued ", len(m.pendingQueue)))
	}
	hint := tuiDimStyle.Render(strings.Join(hintParts, ""))
	remain := max(minVp, termH-lipgloss.Height(header)-lipgloss.Height(input)-lipgloss.Height(hint)-padRows)
	return chatViewLayout{termH: termH, viewportHeight: remain, input: input, hint: hint}
}

// renderScrollIndicator returns a compact scroll indicator for the hint line.
// Returns empty string when at bottom (no indicator needed).
func renderScrollIndicator(scrolledUp bool, width int) string {
	if !scrolledUp {
		return ""
	}
	// Compact visual indicator — unobtrusive arrow shown when scrolled up.
	return tuiDimStyle.Render(" ↓ ")
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
	left := renderNavBrandWordmark(0, phaseIdle) + " " + tuiDimStyle.Render(m.modelName)
	right := tuiDimStyle.Render(" welcome ")
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	spacerN := w - lw - rw
	if spacerN < 1 {
		spacerN = 1
	}
	status := left + tuiHeaderStyle.Render(strings.Repeat("─", spacerN)) + right

	// Build hero block: diamond + MIVIA side by side, slogan below.
	var heroBlock string
	var heroLines int
	if h >= 28 && w >= 60 {
		heroBlock, heroLines = renderHeroBraille(m.logoFrame, w)
	} else {
		heroBlock, heroLines = renderHeroText(w)
	}

	tag := tuiDimStyle.Render("type a message to start · select a session to resume")
	tag = lipgloss.PlaceHorizontal(w, lipgloss.Center, tag)

	return m.renderWelcomeBody(w, h, status, heroBlock, tag, heroLines)
}

// renderHeroBraille builds the welcome hero: diamond + MIVIA side by side, slogan below.
func renderHeroBraille(frame, w int) (block string, lines int) {
	// Get raw diamond art (uncentered).
	diamond := renderLogoFrameColor(frame, 0, brandColorWelcome)
	diamondLines := strings.Split(diamond, "\n")
	diaH := len(diamondLines)

	// Get raw MIVIA braille (2 lines).
	mivia := renderWordmarkBrailleLines(frame)
	miviaH := 2

	// Gap between diamond and MIVIA: 3 braille-cell columns.
	const gapCols = "   " // 3 cols

	// Vertically center MIVIA against diamond.
	padTop := (diaH - miviaH) / 2

	merged := make([]string, diaH)
	for i := 0; i < diaH; i++ {
		leftPart := diamondLines[i]
		if i >= padTop && i < padTop+miviaH {
			merged[i] = leftPart + gapCols + mivia[i-padTop]
		} else {
			merged[i] = leftPart
		}
	}
	hero := strings.Join(merged, "\n")

	// Center the whole hero block.
	hero = lipgloss.PlaceHorizontal(w, lipgloss.Center, hero)

	// Slogan below.
	slogan := tuiDimStyle.Render("autonomous agents · your workspace · your rules")
	slogan = lipgloss.PlaceHorizontal(w, lipgloss.Center, slogan)

	block = hero + "\n" + slogan
	lines = diaH + 1
	return
}

// renderHeroText builds a compact text-only hero for small terminals.
func renderHeroText(w int) (block string, lines int) {
	word := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFFFFF")).
		Bold(true).
		Render("MIVIA")
	word = lipgloss.PlaceHorizontal(w, lipgloss.Center, word)

	slogan := tuiDimStyle.Render("autonomous agents · your workspace · your rules")
	slogan = lipgloss.PlaceHorizontal(w, lipgloss.Center, slogan)

	block = word + "\n" + slogan
	lines = 2
	return
}

func (m *tuiModel) renderWelcomeBody(w, h int, status, heroBlock, tag string, heroLines int) string {
	// Composer card (border chrome outside textarea height).
	inputH := min(composerMaxHeight(h), max(3, m.textarea.LineCount()+1))
	m.textarea.SetWidth(composerInnerWidth(w))
	m.textarea.SetHeight(inputH)
	input := renderComposer(m.textarea.View(), w, false, 0, true, "", false)
	inputLines := lipgloss.Height(input)
	hint := tuiDimStyle.Render(" ↑↓ sessions · enter open · type+enter new · ctrl+c quit ")

	// Vertical budget for session list — never exceed terminal height.
	// fixedNoPicker = status(1) + body_pre(heroLines + 3) + blank(1) + input(inputLines) + hint(1)
	// body pre-picker: hero + blank + tag = heroLines + 3
	const extraLines = 3 // blank(1) + hero_blank(1) + tag(1)
	// The +1 below is the blank line before input.
	fixedNoPicker := heroLines + extraLines + 1 + inputLines + 1
	// Shrink composer if total fixed height exceeds terminal.
	for inputH > 2 && fixedNoPicker > h {
		inputH--
		m.textarea.SetHeight(inputH)
		input = renderComposer(m.textarea.View(), w, false, 0, true, "", false)
		inputLines = lipgloss.Height(input)
		fixedNoPicker = heroLines + extraLines + 1 + inputLines + 1
	}
	maxRows := h - fixedNoPicker
	if maxRows < 0 {
		maxRows = 0
	}
	if maxRows > 12 {
		maxRows = 12
	}

	// Absolute Y of picker: after status, blank, hero, blank, tag, blank
	yBase := 1 + 1 + heroLines + 1 + 1 + 1
	picker, hits, sc := renderSessionPicker(m.sessions, m.sessionSel, m.sessionScroll, w, maxRows, yBase)
	m.sessionHits = hits
	m.sessionScroll = sc

	body := strings.Join([]string{
		"",
		heroBlock,
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
