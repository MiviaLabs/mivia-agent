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
	base := m.renderBaseChatView()
	// The live "now" panel is a paint-only overlay over the top of the
	// transcript: it holds no layout band, so the viewport keeps its full
	// height while the agent works and the transcript never reflows. It is
	// applied before dialogs and the suggest popup, which paint above it.
	if live := m.renderLivePanel(max(1, m.chatPaneWidth()), time.Now()); live != "" {
		base = overlayAt(base, live, m.livePanelOverlayRect(), max(1, m.width), max(8, m.height))
	}
	if m.modelDlg != nil {
		m.modelDlg.busy = m.waiting
		panel, layout := m.modelDlg.ViewAt(max(1, m.width), max(1, m.height))
		return overlayAt(base, panel, layout.rect, max(1, m.width), max(1, m.height))
	}
	if m.agentDlg != nil {
		m.agentDlg.busy = m.waiting
		panel, layout := m.agentDlg.ViewAt(max(1, m.width), max(1, m.height))
		return overlayAt(base, panel, layout.rect, max(1, m.width), max(1, m.height))
	}
	if m.effortDlg != nil {
		m.effortDlg.busy = m.waiting
		panel, layout := m.effortDlg.ViewAt(max(1, m.width), max(1, m.height))
		return overlayAt(base, panel, layout.rect, max(1, m.width), max(1, m.height))
	}
	if m.worktreeDlg != nil {
		panel, layout := m.worktreeDlg.ViewAt(max(1, m.width), max(1, m.height))
		return overlayAt(base, panel, layout.rect, max(1, m.width), max(1, m.height))
	}
	if m.overlay != nil {
		panel, layout := m.overlay.ViewAt(max(1, m.width), max(1, m.height))
		return overlayAt(base, panel, layout.rect, max(1, m.width), max(1, m.height))
	}
	if m.suggest.open {
		pane := newChatPaneLayout(m.width, m.sessionsSidebar != nil)
		panel, size := renderSuggestPanel(m.suggest, max(1, pane.chatWidth), max(0, m.suggestComposerTop()-1))
		if panel != "" {
			return overlayAt(base, panel, suggestOverlayRect(m, panel, size), max(1, m.width), max(8, m.height))
		}
	}
	return base
}

// composerModelLabel is the provider-qualified model shown at the
// bottom-right of the composer border. Falls back to the shortened model
// name when the provider is unknown.
func (m *tuiModel) composerModelLabel() string {
	if m.session == nil {
		return m.modelName
	}
	sel := m.session.CurrentSelection()
	model := shortenModel(sel.Model)
	if strings.TrimSpace(sel.ProviderName) == "" {
		return model
	}
	return sel.ProviderName + "/" + model
}

// statusDetail is the status-bar stepDetail chrome. During an active turn it
// appends the live context usage so context growth is visible without opening
// /status; idle returns stepDetail as-is.
func (m *tuiModel) statusDetail() string {
	if !m.waiting {
		return m.stepDetail
	}
	return appendCtxSuffix(m.stepDetail, m.liveCtxPercent())
}

// liveCtxPercent returns the session's context usage percentage, throttled to
// at most one ContextUsage() call per 500 ms. Avoids per-frame cost of message
// cloning + tool-schema marshaling while still showing live values during a turn.
func (m *tuiModel) liveCtxPercent() int {
	if !m.waiting {
		return 0
	}
	now := time.Now()
	if now.Sub(m.cachedCtxPercentAt) < 500*time.Millisecond {
		return m.cachedCtxPercent
	}
	m.cachedCtxPercent = m.session.ContextUsage().Percent
	m.cachedCtxPercentAt = now
	return m.cachedCtxPercent
}

func appendCtxSuffix(detail string, percent int) string {
	suffix := fmt.Sprintf("ctx %d%%", percent)
	if detail == "" {
		return suffix
	}
	return detail + " · " + suffix
}

// sidebarLiveStatus derives the sessions-sidebar dot state from the live
// turn state. Open tools outrank streaming; streaming outranks thinking.
// Waiting with no data yet reads as thinking, the closest working state.
func (m *tuiModel) sidebarLiveStatus() sidebarLiveStatus {
	if !m.waiting {
		return liveStatusIdle
	}
	open, _, _ := countTools(m.toolRows)
	if open > 0 {
		return liveStatusTools
	}
	if m.streamBuf.Len() > 0 {
		return liveStatusStreaming
	}
	return liveStatusThinking
}

func (m *tuiModel) renderBaseChatView() string {
	pane := newChatPaneLayout(m.width, m.sessionsSidebar != nil)
	if !pane.sidebarVisible {
		return m.renderChatPane()
	}
	// The chat renderer uses m.width as its layout input. Scope the temporary
	// width change to this synchronous render, then restore the terminal width.
	width := m.width
	m.width = pane.chatWidth
	chat := m.renderChatPane()
	m.width = width
	sidebar := m.sessionsSidebar.viewWithActive(m.sessions, pane.sidebarWidth, max(1, m.height), m.focus == focusSidebar, m.activeSession, m.sidebarLiveStatus())
	padding := paneSpacer(pane.dividerPadding, max(1, m.height))
	divider := sidebarDivider(pane.dividerWidth, max(1, m.height))
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, padding, divider, padding, chat)
}

func (m *tuiModel) renderChatPane() string {
	open, done, total := countTools(m.toolRows)
	phase := deriveBrandPhase(m.waiting, open, m.streamBuf.Len(), len(m.pendingQueue), false, time.Since(m.turnStart))

	header := renderStatusBar(
		m.logoFrame, phase, m.waiting, time.Since(m.turnStart),
		open, done, total, len(m.pendingQueue), m.session.MessagesCount(), m.width,
		m.statusDetail(), m.gitBranch, m.gitWorktreeName,
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

	// The live "now" panel is a paint-only overlay drawn over the top of the
	// transcript in renderChatView. It holds no layout band here, so the
	// composer and hint sit directly below the full-height viewport and the
	// hit map needs no live-panel zone: clicks over the overlay resolve to
	// the transcript beneath it.
	composerY0 := lipgloss.Height(header) + lipgloss.Height(body)
	// Composer card with vertical breathing room (no horizontal padding - aligns with viewport user cards).
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
		// Prefer keeping status (top) - drop from middle by trimming body.
		out = strings.Join(outLines[:termH], "\n")
	}
	return out
}

type chatViewLayout struct {
	termH, viewportHeight int
	input, hint           string
}

// composerPadRows is the composer card's vertical padding (1 top + 1 bottom,
// added via Padding(1,0) in renderChatView). Every height computation - the
// Update-path layout() and the View-path chatViewLayout - must subtract it,
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
		probe := renderComposer(m.textarea.View(), composerW, m.composerModelLabel())
		if lipgloss.Height(header)+lipgloss.Height(probe)+1+minVp+padRows <= termH {
			break
		}
		inputH--
	}
	m.textarea.SetHeight(inputH)
	m.textarea.SetWidth(composerInnerWidth(composerW))
	input := renderComposer(m.textarea.View(), composerW, m.composerModelLabel())
	// Hint line on a diet: the keys that matter in THIS state, plus live
	// counts. Seven competing segments read as a junk drawer; /help is the
	// full reference and is one keystroke away.
	hintParts := []string{" enter send · shift+drag or F2 to select · /help "}
	if m.suggest.open {
		hintParts[0] = " ↑↓ select · tab insert · enter run eligible command · esc dismiss "
	}
	if m.waiting {
		if m.stalledWarning {
			hintParts[0] = "· ctrl+g agents · ctrl+c cancel "
		} else {
			hintParts[0] = " type to queue · ctrl+g agents · ctrl+c cancel "
		}
	}
	if !m.mouseEnabled {
		// Mouse capture is released: the terminal owns selection again. Say so
		// loudly - a mode you cannot see is a mode you cannot leave.
		hintParts = []string{tuiAccentStyle.Render(" select mode ") +
			tuiDimStyle.Render(" drag to select, then copy as usual · F2 back ")}
	}
	// Copy/paste acknowledgements are shown here while idle. The status bar
	// shows stepDetail only during a turn, which is exactly when
	// nobody is copying: every copy made at rest was silent, and silence
	// after a copy is indistinguishable from a broken key.
	// The arm prompt is sourced from the arm itself, not from a notice TTL:
	// a prompt that outlives the arm promises an exit the next press will
	// not deliver.
	if m.quitArmed() {
		hintParts = append(hintParts, "· "+quitArmNotice+" ")
	} else if n := m.freshNotice(); n != "" {
		hintParts = append(hintParts, "· "+n+" ")
	}
	if len(m.pendingQueue) > 0 {
		hintParts = append(hintParts, fmt.Sprintf("· %d queued ", len(m.pendingQueue)))
	}
	if m.runDash != nil && !m.runDash.isOpen() {
		if s := m.runDash.summary(); s != "" {
			hintParts = append(hintParts, fmt.Sprintf("· %s ", s))
		}
	}
	hint := tuiDimStyle.Render(strings.Join(hintParts, ""))
	if m.stalledWarning {
		hint = tuiErrorStyle.Render(" ⚠ stalled ") + hint
	}
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
	// Compact visual indicator - unobtrusive arrow shown when scrolled up.
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
	left := brandNameStyled()
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

	base := m.renderWelcomeBody(w, h, status, heroBlock, heroLines)
	if !m.suggest.open {
		return base
	}
	input := renderComposer(m.textarea.View(), w, m.composerModelLabel())
	composerTop := max(1, lipgloss.Height(base)-1-lipgloss.Height(input))
	panel, size := renderSuggestPanel(m.suggest, w, max(0, composerTop-1))
	if panel == "" {
		return base
	}
	y := max(1, composerTop-size.h)
	return overlayAt(base, panel, rect{x: max(0, min(2, w-size.w)), y: y, w: size.w, h: size.h}, w, h)
}

const heroSlogan = "autonomous agents · your workspace · your rules"

// renderHeroBraille builds the Lockup+ hero: a 16×8-cell idle diamond on the
// left, identity flush beside it - wordmark, slogan, then model + workspace.
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
	input := renderComposer(m.textarea.View(), w, m.composerModelLabel())
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
		input = renderComposer(m.textarea.View(), w, m.composerModelLabel())
		inputLines = lipgloss.Height(input)
	}

	// Build warning banner for previous auto-save failure.
	warnBlock := ""
	warningText := ""
	if m.welcomeNotice != "" {
		warningText = "⚠ " + m.welcomeNotice
	} else if m.prevAutoSaveWarn != "" {
		warningText = fmt.Sprintf("⚠ Last session NOT saved: %s", m.prevAutoSaveWarn)
	}
	if warningText != "" {
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
// Does not auto-load the last session - welcome screen lets the user choose.
