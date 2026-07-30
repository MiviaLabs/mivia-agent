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
		return brandNameStyled() + tuiDimStyle.Render(" starting…")
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

	layout := m.chatViewLayout(header, phase)
	termH, input, hint := layout.termH, layout.input, layout.hint
	vpH := layout.viewportHeight
	m.viewport.Width = max(1, m.width)
	m.viewport.Height = vpH
	if !m.ready {
		m.ready = true
	}

	body := m.viewport.View()
	scrolledUp := !m.followOutput || !m.viewport.AtBottom()

	// Scroll indicator: " ↓ latest " while waiting away from bottom; else " ↓ ".
	if scrolledUp && m.width > 12 {
		hint += renderScrollIndicator(true, m.width, m.waiting)
	}

	composerY0 := lipgloss.Height(header) + lipgloss.Height(body)
	// Composer card with vertical breathing room (no horizontal padding — aligns with viewport user cards).
	paddedInput := lipgloss.NewStyle().Padding(1, 0).Render(input)
	composerY1 := composerY0 + lipgloss.Height(paddedInput) + lipgloss.Height(hint) - 1
	m.hitMap.rebuild(m.width, termH, lipgloss.Height(header), lipgloss.Height(body), 1, 0, composerY0, composerY1, m.chatBlockRanges, m.viewport.YOffset)

	parts := []string{header, body, paddedInput, hint}
	// Append run dashboard panel if open and has runs.
	if m.runDash != nil && m.runDash.isOpen() {
		dash := m.runDash.renderPanel(m.width)
		if dash != "" {
			parts = append(parts, dash)
		}
	}
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

// composerPadRows is the composer card's vertical padding (1 top + 1 bottom,
// added via Padding(1,0) in renderChatView). Every height computation — the
// Update-path layout() and the View-path chatViewLayout — must subtract it,
// or the two paths size the viewport differently and the frame clips the
// composer border on send.
const composerPadRows = 2

func (m *tuiModel) chatViewLayout(header string, phase brandPhase) chatViewLayout {
	const minVp = 2
	const padRows = composerPadRows
	termH := max(8, m.height)
	composerW := max(18, m.width-2) // leave 1 col left + right for padding
	inputH := min(composerMaxHeight(termH), max(1, m.textarea.LineCount()))
	for inputH > 1 {
		m.textarea.SetHeight(inputH)
		m.textarea.SetWidth(composerInnerWidth(composerW))
		probe := renderComposer(m.textarea.View(), composerW, m.waiting, len(m.pendingQueue), m.focus == focusComposer, phase, m.stepDetail, m.stalledWarning)
		if lipgloss.Height(header)+lipgloss.Height(probe)+1+minVp+padRows <= termH {
			break
		}
		inputH--
	}
	m.textarea.SetHeight(inputH)
	m.textarea.SetWidth(composerInnerWidth(composerW))
	input := renderComposer(m.textarea.View(), composerW, m.waiting, len(m.pendingQueue), m.focus == focusComposer, phase, m.stepDetail, m.stalledWarning)
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
	// Run dashboard indicator.
	if m.runDash != nil && !m.runDash.isOpen() {
		if s := m.runDash.summary(); s != "" {
			hintParts = append(hintParts, fmt.Sprintf("· %s ", s))
		}
	}
	if m.runDash != nil {
		hintParts = append(hintParts, "· ctrl+r runs ")
	}
	hint := tuiDimStyle.Render(strings.Join(hintParts, ""))
	remain := max(minVp, termH-lipgloss.Height(header)-lipgloss.Height(input)-lipgloss.Height(hint)-padRows)
	return chatViewLayout{termH: termH, viewportHeight: remain, input: input, hint: hint}
}

// renderScrollIndicator returns a compact scroll indicator for the hint line.
// Returns empty string when at bottom (no indicator needed).
// waiting enables the Phase D "↓ latest" affordance during a live turn.
func renderScrollIndicator(scrolledUp bool, width int, waiting ...bool) string {
	if !scrolledUp {
		return ""
	}
	live := len(waiting) > 0 && waiting[0]
	if live {
		return tuiDimStyle.Render(" ↓ latest ")
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
	left := brandNameStyled() + " " + tuiDimStyle.Render(m.modelName)
	right := tuiDimStyle.Render(" welcome ")
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	spacerN := w - lw - rw
	if spacerN < 1 {
		spacerN = 1
	}
	status := left + tuiHeaderStyle.Render(strings.Repeat("─", spacerN)) + right

	// Build hero block: Lockup+ (diamond left, identity right) on terminals
	// wide enough for the side-by-side; text-only hero otherwise.
	var heroBlock string
	var heroLines int
	if w >= 70 && h >= 24 && (m.prevAutoSaveWarn == "" || h >= 26) {
		heroBlock, heroLines = renderHeroBraille(m.logoFrame, w, m.modelName, m.workspaceDir)
	} else {
		heroBlock, heroLines = renderHeroText(w)
	}

	return m.renderWelcomeBody(w, h, status, heroBlock, heroLines)
}

const heroSlogan = "autonomous agents · your workspace · your rules"

// renderHeroBraille builds the Lockup+ hero: a 16×8-cell idle diamond on the
// left, identity flush beside it — wordmark, slogan, then model + workspace.
// Left-aligned like a tool, not centered like a poster. No greeting, no
// version string.
func renderHeroBraille(frame, w int, modelName, workspace string) (block string, lines int) {
	const margin = "  "
	const gap = "   "
	rows := renderStateLogoRows(phaseWelcome, frame, 32, 32)

	budget := w - len(margin) - 16 - len(gap)
	facts := modelName
	if workspace != "" {
		if facts != "" {
			facts += " · "
		}
		facts += workspace
	}
	facts = truncateToWidth(facts, budget)
	slogan := truncateToWidth(heroSlogan, budget)

	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(margin)
		b.WriteString(r)
		switch i {
		case 2:
			b.WriteString(gap + brandNameStyled())
		case 3:
			b.WriteString(gap + tuiDimStyle.Render(slogan))
		case 5:
			if facts != "" {
				b.WriteString(gap + tuiDimStyle.Render(facts))
			}
		}
	}
	return b.String(), len(rows)
}

// renderHeroText builds a compact text-only hero for small terminals.
func renderHeroText(w int) (block string, lines int) {
	const margin = "  "
	word := margin + brandNameStyled()
	slogan := margin + tuiDimStyle.Render(truncateToWidth(heroSlogan, w-len(margin)))
	return word + "\n" + slogan, 2
}

func (m *tuiModel) renderWelcomeBody(w, h int, status, heroBlock string, heroLines int) string {
	// Composer card (border chrome outside textarea height).
	inputH := min(composerMaxHeight(h), max(1, m.textarea.LineCount()))
	m.textarea.SetWidth(composerInnerWidth(w))
	m.textarea.SetHeight(inputH)
	input := renderComposer(m.textarea.View(), w, false, 0, true, phaseWelcome, "", false)
	inputLines := lipgloss.Height(input)
	// Single instruction line, primary action first. The old centered tag
	// under the hero repeated this and cost the picker a row.
	hint := tuiDimStyle.Render(" type to start · ↑↓ sessions · enter open · ctrl+c quit ")

	// Keep enough room for the status, body chrome, and composer before the
	// picker consumes session rows.
	const welcomeChromeLines = 6
	for inputH > 1 && heroLines+inputLines+welcomeChromeLines > h {
		inputH--
		m.textarea.SetHeight(inputH)
		input = renderComposer(m.textarea.View(), w, false, 0, true, phaseWelcome, "", false)
		inputLines = lipgloss.Height(input)
	}

	// Build warning banner for previous auto-save failure.
	warnBlock := ""
	if m.prevAutoSaveWarn != "" {
		warningText := fmt.Sprintf("⚠ Last session NOT saved: %s", m.prevAutoSaveWarn)
		// Truncate if wider than terminal.
		if len(warningText) > w {
			warningText = warningText[:w]
		}
		warnBlock = tuiErrorStyle.Render(warningText)
	}

	pickerBudget := h - heroLines - inputLines - welcomeChromeLines
	if warnBlock != "" {
		pickerBudget -= 2 // blank line plus warning
	}
	// A truncated picker adds a "more" line in addition to its four fixed
	// chrome lines, so reserve five before allocating session rows.
	maxRows := min(12, max(1, pickerBudget-5))

	// Absolute Y of picker: after status, blank, hero, blank, warn
	yBase := 1 + 1 + heroLines + 1
	if warnBlock != "" {
		yBase += 2 // blank + warn line
	}
	picker, hits, sc := renderSessionPicker(m.sessions, m.sessionSel, m.sessionScroll, w, maxRows, yBase)
	m.sessionHits = hits
	m.sessionScroll = sc

	bodyParts := []string{
		"",
		heroBlock,
	}
	if warnBlock != "" {
		bodyParts = append(bodyParts, "", warnBlock)
	}
	bodyParts = append(bodyParts, "", picker)
	body := strings.Join(bodyParts, "\n")

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
