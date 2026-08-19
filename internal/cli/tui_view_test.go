package cli

import (
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

func makeTestSession() *chat.Session {
	return newTestSessionForModel("test-model")
}

func seedTools(n int) []toolRow {
	now := time.Now()
	rows := make([]toolRow, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, toolRow{
			Name:   "tool_" + strings.Repeat("x", i%3+1),
			Detail: `{"path":"file.go"}`,
			Start:  now.Add(-time.Duration(i) * time.Second),
			Done:   i%2 == 0,
			End:    now,
		})
	}
	return rows
}

func viewLineCount(view string) int {
	if view == "" {
		return 0
	}
	return strings.Count(view, "\n") + 1
}

func maxViewHeight(height int) int {
	// View() floors terminal height at 8 before budgeting chrome.
	if height < 8 {
		return 8
	}
	return height
}

func newReadyChatModel(height, width int) *tuiModel {
	m := newTUIModel(makeTestSession(), nil, true)
	m.mode = modeChat
	m.ready = true
	m.width = width
	m.height = height
	m.viewport = viewport.New(width, max(2, height/2))
	m.viewport.SetContent("hello from viewport\nsecond line")
	m.textarea.SetWidth(max(20, width-4))
	m.textarea.SetHeight(3)
	m.turnStart = time.Now()
	return m
}

func TestView_LineCountNeverExceedsHeight(t *testing.T) {
	heights := []int{12, 16, 24, 40}
	widths := []int{40, 80}
	toolCounts := []int{0, 3, 20}

	for _, h := range heights {
		for _, w := range widths {
			for _, wait := range []bool{false, true} {
				for _, nTools := range toolCounts {
					if !wait && nTools > 0 {
						// Tool strip only renders while waiting; still exercise
						// line budget with rows present but idle.
					}
					m := newReadyChatModel(h, w)
					m.waiting = wait
					m.toolRows = seedTools(nTools)
					if nTools > 0 {
						m.toolPanel.Focused = true
						m.toolPanel.Selected = 0
						m.toolPanel.ordered = orderToolIndices(m.toolRows)
					}
					// Seed enough transcript that viewport wants many lines.
					var lines []string
					for i := 0; i < 50; i++ {
						lines = append(lines, "transcript line "+strings.Repeat("z", 20))
					}
					m.messages = lines
					m.renderVP()

					view := m.View()
					got := viewLineCount(view)
					maxH := maxViewHeight(h)
					if got > maxH {
						t.Fatalf("height=%d width=%d waiting=%v tools=%d: line count %d > max %d\nplain:\n%s",
							h, w, wait, nTools, got, maxH, stripANSI(view))
					}

					plain := stripANSI(view)
					// Status chrome should show the brand diamond + wordmark
					// when height is reasonable.
					if h >= 12 {
						hasDiamond := strings.ContainsAny(plain, "◇◆")
						if !hasDiamond || !strings.Contains(plain, "mivia") {
							t.Fatalf("height=%d width=%d waiting=%v tools=%d: missing brand diamond/wordmark\n%s",
								h, w, wait, nTools, plain)
						}
					}
				}
			}
		}
	}
}

func TestView_StatusOutsideViewport(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.waiting = false
	m.messages = []string{"user says hello", "assistant replies"}
	m.viewport.SetContent("VPONLY_MARKER unique viewport body")
	m.renderVP()
	// renderVP rebuilds from messages; set content after for isolation.
	m.viewport.SetContent("VPONLY_MARKER unique viewport body")

	view := m.View()
	plain := stripANSI(view)

	diamondAt := func(s string) int { return strings.IndexAny(s, "◇◆") }
	if diamondAt(plain) < 0 || !strings.Contains(plain, "mivia") {
		t.Fatalf("status bar brand (diamond + mivia) missing:\n%s", plain)
	}
	if !strings.Contains(plain, "VPONLY_MARKER") {
		t.Fatalf("viewport content missing from full View:\n%s", plain)
	}

	// Status is sticky chrome above the body: brand must appear before
	// the viewport marker in the joined frame.
	mi := diamondAt(plain)
	vp := strings.Index(plain, "VPONLY_MARKER")
	if mi < 0 || vp < 0 || mi > vp {
		t.Fatalf("expected brand status before viewport body (mi=%d vp=%d):\n%s", mi, vp, plain)
	}

	// Viewport.View alone must not include the status brand string as chrome
	// (viewport is message body only). Brand may appear inside messages; use marker isolation.
	bodyOnly := stripANSI(m.viewport.View())
	if diamondAt(bodyOnly) >= 0 && !strings.Contains(bodyOnly, "VPONLY_MARKER") {
		t.Fatalf("unexpected: status brand in viewport without marker: %q", bodyOnly)
	}
	// Full view has both regions; body-only is the messages region.
	if bodyOnly == plain {
		t.Fatal("View() must not equal viewport-only content (status chrome missing)")
	}
}

func TestToolStatus_Removed(t *testing.T) {
	// R1.1: toolStatus line with "◐ N running · M done · K total" must NOT appear.
	heights := []int{12, 24}
	widths := []int{40, 80}
	for _, h := range heights {
		for _, w := range widths {
			m := newReadyChatModel(h, w)
			m.waiting = true
			m.toolRows = seedTools(5)
			m.toolPanel.Focused = true
			m.toolPanel.Selected = 0
			m.toolPanel.ordered = orderToolIndices(m.toolRows)
			var lines []string
			for i := 0; i < 50; i++ {
				lines = append(lines, "transcript line "+strings.Repeat("z", 20))
			}
			m.messages = lines
			m.renderVP()

			view := m.View()
			plain := stripANSI(view)
			if strings.Contains(plain, "running") || strings.Contains(plain, "done") || strings.Contains(plain, "total") {
				t.Fatalf("height=%d width=%d: toolStatus line still present in output:\n%s", h, w, plain)
			}
		}
	}
}

func TestScrollIndicator(t *testing.T) {
	// R1.2: scroll indicator " ↓ " should appear only when scrolled up.
	m := newReadyChatModel(24, 80)
	m.waiting = false
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, "scroll test line "+strings.Repeat("z", 20))
	}
	m.messages = lines
	m.renderVP()
	// At bottom initially - no scroll indicator.
	m.viewport.GotoBottom()
	view := m.View()
	plain := stripANSI(view)
	if strings.Contains(plain, "↓") {
		t.Fatalf("scroll indicator present at bottom; should be absent:\n%s", plain)
	}

	// Scroll up a bit - indicator should appear.
	m.viewport.ViewUp()
	m.viewport.ViewUp()
	m.viewport.ViewUp()
	view2 := m.View()
	plain2 := stripANSI(view2)
	if !strings.Contains(plain2, "↓") {
		t.Fatalf("scroll indicator missing after scrolling up:\n%s", plain2)
	}
}

func TestRenderScrollIndicator(t *testing.T) {
	// Unit test for renderScrollIndicator directly.
	got := renderScrollIndicator(false, 80)
	if got != "" {
		t.Fatalf("expected empty when at bottom, got %q", got)
	}
	got = renderScrollIndicator(true, 40)
	if got == "" {
		t.Fatalf("expected non-empty when scrolled up, got empty")
	}
	if !strings.Contains(stripANSI(got), "↓") {
		t.Fatalf("expected ↓ in indicator, got %q", stripANSI(got))
	}
}

func TestCtrlMDoesNotToggleMouse(t *testing.T) {
	// A distinct "ctrl+m" can never arrive from a terminal: 0x0D is carriage
	// return and bubbletea aliases KeyCtrlM to KeyEnter, so any branch on the
	// string "ctrl+m" is dead code that only tests could reach - while /help
	// advertised it as the mouse toggle. The binding is removed; this pins the
	// removal so the lie cannot return.
	t.Setenv("MIVIA_MOUSE", "0")
	m := newReadyChatModel(24, 80)
	if m.mouseEnabled {
		t.Fatal("mouseEnabled should be false with MIVIA_MOUSE=0")
	}
	m.mode = modeChat
	_, _, _ = m.handleChatKey("ctrl+m", false)
	if m.mouseEnabled {
		t.Fatal("ctrl+m must be inert: the real key 0x0D is enter, so a working ctrl+m binding is impossible to press")
	}
}

func TestRunTUI_MouseOptionFollowsAvailability(t *testing.T) {
	// WithMouseCellMotion is applied in runTUI only when model.mouseEnabled.
	t.Setenv("MIVIA_MOUSE", "0")
	m := newTUIModel(makeTestSession(), nil, true)
	if m.mouseEnabled {
		t.Fatal("expected mouse off with MIVIA_MOUSE=0")
	}
	t.Setenv("MIVIA_MOUSE", "1")
	m2 := newTUIModel(makeTestSession(), nil, true)
	if !m2.mouseEnabled {
		t.Fatal("expected mouse on with MIVIA_MOUSE=1")
	}
}

func TestRenderStatusBar_OneLine(t *testing.T) {
	// Broader coverage than brand_test: widths, phases, thinking, queue.
	// One physical line, always led by the simple state diamond (◇/◆).
	phases := []brandPhase{phaseIdle, phaseThinking, phaseStreaming, phaseTools, phaseMulti, phaseQueued, phaseError, phaseCancel}
	widths := []int{20, 40, 80, 120}
	for _, ph := range phases {
		for _, w := range widths {
			for _, wait := range []bool{false, true} {
				if wait && ph == phaseIdle {
					continue
				}
				if !wait && (ph == phaseThinking || ph == phaseStreaming || ph == phaseTools || ph == phaseMulti) {
					continue
				}
				out := renderStatusBar(
					2, ph, wait, 1500*time.Millisecond,
					1, 2, 3, 1, 7, w, "", "", "",
				)
				plain := stripANSI(out)
				if strings.Count(plain, "\n") > 0 {
					t.Fatalf("status multi-line phase=%v wait=%v w=%d: %q", ph, wait, w, plain)
				}
				for _, r := range plain {
					if unicode.IsControl(r) && r != '\t' {
						t.Fatalf("control rune U+%04X in status: %q", r, plain)
					}
				}
				if !strings.HasPrefix(plain, "◇") && !strings.HasPrefix(plain, "◆") {
					t.Fatalf("status missing leading diamond phase=%v wait=%v w=%d: %q", ph, wait, w, plain)
				}
			}
		}
	}
}

func TestLayoutAndViewAgreeOnViewportHeight(t *testing.T) {
	// layout() (Update path) and renderChatView (View path) must size the
	// viewport identically. When layout() forgot the composer's two padding
	// rows, the frame overflowed on send and the composer border clipped.
	for _, wait := range []bool{false, true} {
		m := newReadyChatModel(30, 80)
		m.waiting = wait
		if wait {
			m.turnStart = time.Now()
		}
		m.messages = []string{"one", "two", "three"}
		m.layout()
		fromLayout := m.viewport.Height
		m.View()
		fromView := m.viewport.Height
		if fromLayout != fromView {
			t.Fatalf("waiting=%v: layout()=%d View()=%d - viewport sized differently in the two paths", wait, fromLayout, fromView)
		}
	}
}

func TestViewportHeightIgnoresLivePanel(t *testing.T) {
	// The live panel is a paint-only overlay: it must not reserve layout
	// space. layout() (Update path) and chatViewLayout (View path) must size
	// the viewport identically whether the panel is visible or not.
	for _, h := range []int{20, 30, 40, 44} {
		m := newReadyChatModel(h, 80)
		m.messages = []string{"one", "two", "three"}
		m.renderVP()
		m.layout()
		idleLayout := m.viewport.Height
		m.View()
		idleView := m.viewport.Height

		m.waiting = true
		m.turnStart = time.Now()
		m.toolRows = []toolRow{{Name: "run_command", Detail: `{"cmd":"go test"}`, Status: "running", Start: time.Now()}}
		m.streamBuf.WriteString("streaming answer")
		if m.livePanelHeight() == 0 {
			t.Fatalf("height=%d: panel must be visible while waiting", h)
		}
		m.layout()
		waitLayout := m.viewport.Height
		m.View()
		waitView := m.viewport.Height

		if idleLayout != waitLayout {
			t.Fatalf("height=%d: layout() viewport %d idle vs %d waiting - the panel reserved a band", h, idleLayout, waitLayout)
		}
		if idleView != waitView {
			t.Fatalf("height=%d: View() viewport %d idle vs %d waiting - the panel reserved a band", h, idleView, waitView)
		}
	}
}

func TestLivePanelOverlayDoesNotReflowTranscript(t *testing.T) {
	// The overlay paints over the transcript top without shifting it: the
	// viewport content is byte-identical idle vs waiting, and every transcript
	// row below the overlay region is byte-identical to the idle frame.
	m := newReadyChatModel(40, 80)
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, "transcript line "+strings.Repeat("z", 16))
	}
	m.messages = lines
	m.View()     // size the viewport through the View path while idle
	m.renderVP() // settle content and scroll at the sized height before capture
	idleHeight := m.viewport.Height
	idleVP := m.viewport.View()
	idleLines := strings.Split(stripANSI(m.View()), "\n")

	m.waiting = true
	m.turnStart = time.Now()
	m.toolRows = []toolRow{{Name: "run_command", Detail: `{"cmd":"go test"}`, Status: "running", Start: time.Now()}}
	m.streamBuf.WriteString("streaming answer")
	m.renderVP()
	m.View() // size the viewport through the View path while waiting
	waitHeight := m.viewport.Height
	waitVP := m.viewport.View()
	waitLines := strings.Split(stripANSI(m.View()), "\n")

	if idleHeight != waitHeight {
		t.Fatalf("viewport height %d idle vs %d waiting - the overlay must not shrink the viewport", idleHeight, waitHeight)
	}
	if idleVP != waitVP {
		t.Fatal("viewport content changed when the overlay appeared - the overlay must not reflow the transcript")
	}
	H := m.livePanelHeight()
	if H == 0 {
		t.Fatal("precondition: the overlay must be visible while waiting")
	}
	if !strings.Contains(waitLines[1], " now · ") {
		t.Fatalf("overlay header must sit on the row below the status header:\n%s", strings.Join(waitLines[:4], "\n"))
	}
	// Transcript rows below the overlay are byte-identical to the idle frame:
	// painting must not shift or reflow anything beneath it.
	for y := H + 1; y <= idleHeight; y++ {
		if waitLines[y] != idleLines[y] {
			t.Fatalf("transcript row %d reflowed under the overlay:\nidle:   %q\nwaiting: %q", y, idleLines[y], waitLines[y])
		}
	}
}

// sidebarPrefix is the first sidebarWidth columns of a joined view line.
// The sidebar renders before the divider lane, so the row identity and the
// status dot always live in this prefix and nowhere else on the line.
func sidebarPrefix(line string) string {
	if len(line) > 28 {
		return line[:28]
	}
	return line
}

// sidebarRowLine returns the first view line whose sidebar prefix contains
// the session name.
func sidebarRowLine(t *testing.T, view, name string) string {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(sidebarPrefix(line), name) {
			return line
		}
	}
	t.Fatalf("sidebar row %q not found:\n%s", name, view)
	return ""
}

func TestSessionsSidebarLiveStatusReflectsTurnState(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.sessionsSidebar = newSessionsSidebar()
	m.sessions = []chat.SessionInfo{{Name: "alpha"}, {Name: "beta"}}

	cases := []struct {
		name        string
		waiting     bool
		thinking    string
		stream      string
		toolRows    []toolRow
		wantGlyph   string
		wantCurrent bool
	}{
		{name: "idle", waiting: false, wantGlyph: "●", wantCurrent: true},
		{name: "thinking", waiting: true, thinking: "reasoning text", wantGlyph: "◔", wantCurrent: true},
		{name: "streaming", waiting: true, stream: "streamed words", wantGlyph: "◐", wantCurrent: true},
		{name: "tools", waiting: true, toolRows: []toolRow{{Name: "t", Detail: `{}`, Start: time.Now()}}, wantGlyph: "◉", wantCurrent: true},
		{name: "waiting no data", waiting: true, wantGlyph: "◔", wantCurrent: true},
		{name: "precedence", waiting: true, thinking: "reasoning text", stream: "streamed words", toolRows: []toolRow{{Name: "t", Detail: `{}`, Start: time.Now()}}, wantGlyph: "◉", wantCurrent: true},
		{name: "nil active", waiting: false, wantGlyph: "", wantCurrent: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m.activeSession = &m.sessions[1]
			if tc.name == "nil active" {
				m.activeSession = nil
			}
			m.waiting = tc.waiting
			m.thinkingBuf.Reset()
			m.thinkingBuf.WriteString(tc.thinking)
			m.streamBuf.Reset()
			m.streamBuf.WriteString(tc.stream)
			m.toolRows = tc.toolRows
			if len(tc.toolRows) > 0 {
				m.toolPanel.Focused = true
				m.toolPanel.Selected = 0
				m.toolPanel.ordered = orderToolIndices(m.toolRows)
			}

			view := stripANSI(m.View())
			alphaLine := sidebarRowLine(t, view, "alpha")
			betaLine := sidebarRowLine(t, view, "beta")

			if tc.wantCurrent {
				if !strings.Contains(sidebarPrefix(betaLine), "current") {
					t.Fatalf("%s: active row missing identity marker: %q", tc.name, betaLine)
				}
				if !strings.Contains(sidebarPrefix(betaLine), tc.wantGlyph) {
					t.Fatalf("%s: active row missing dot %q: %q", tc.name, tc.wantGlyph, betaLine)
				}
			} else {
				for _, glyph := range []string{"●", "◔", "◐", "◉"} {
					if strings.Contains(sidebarPrefix(betaLine), glyph) || strings.Contains(sidebarPrefix(alphaLine), glyph) {
						t.Fatalf("%s: dot %q rendered with nil active: %q", tc.name, glyph, view)
					}
				}
				if strings.Contains(view, "current") {
					t.Fatalf("%s: identity marker with nil active: %q", tc.name, view)
				}
			}
			if strings.Contains(sidebarPrefix(alphaLine), "current") {
				t.Fatalf("%s: non-active row shows identity marker: %q", tc.name, alphaLine)
			}
			for _, glyph := range []string{"●", "◔", "◐", "◉"} {
				if strings.Contains(sidebarPrefix(alphaLine), glyph) {
					t.Fatalf("%s: non-active row shows dot %q: %q", tc.name, glyph, alphaLine)
				}
			}
		})
	}
}

// TestShortenModel pins the composer/hero model-name truncation contract: a
// 24-byte budget, longer names clipped to 21 bytes plus "...". ASCII inputs
// are exact. (Multibyte names can still be cut mid-rune by the byte slice;
// that DC-6 defect is outside this delivery slice's scope.)
func TestShortenModel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "ascii short", in: "gpt-4o-mini", want: "gpt-4o-mini"},
		{name: "ascii exactly 24", in: strings.Repeat("a", 24), want: strings.Repeat("a", 24)},
		{name: "ascii long", in: strings.Repeat("a", 30), want: strings.Repeat("a", 21) + "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shortenModel(tc.in); got != tc.want {
				t.Fatalf("shortenModel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestWelcomeWarningTruncationKeepsUTF8 pins the welcome-banner truncation to
// cell boundaries. The banner used to be clipped with a BYTE slice
// (warningText[:w]); when byte w landed inside a multi-byte rune the banner
// emitted invalid UTF-8, which terminals render as U+FFFD. This is defect
// class DC-6 in .agents/quality/defect-taxonomy.md ("A truncation cuts a
// multi-byte character"). Every case asserts (1) every raw view line is valid
// UTF-8, (2) the trimmed warning line fits the terminal width, and (3) the
// warning content survives. The multibyte cut cases (and the exact-boundary
// case) fail RED against the old byte slice and pass with truncateToWidth.
func TestWelcomeWarningTruncationKeepsUTF8(t *testing.T) {
	cases := []welcomeWarningCase{
		{name: "empty warning", width: 40, wantWarning: false},
		{name: "ascii short", width: 40, prevWarn: "disk full", wantWarning: true, wantContent: "Last session NOT saved: disk full"},
		// ASCII long: old behavior preserved exactly (byte cut == cell cut).
		{name: "ascii long", width: 40, prevWarn: strings.Repeat("x", 60), wantWarning: true, wantContent: "Last session NOT saved: "},
		// Display width (20) fits even though byte length (31) exceeds w: the
		// old code over-truncated to 5 runes and cut mid-rune.
		{name: "multibyte fits by display width", width: 20, welcomeNotice: strings.Repeat("界", 9), wantWarning: true, wantContent: strings.Repeat("界", 9)},
		// Byte 20 lands inside a 3-byte rune (U+754C).
		{name: "3-byte cut", width: 20, welcomeNotice: strings.Repeat("界", 30), wantWarning: true, wantContent: "⚠"},
		// Byte 25 lands inside a 2-byte rune (U+00E9).
		{name: "2-byte cut", width: 25, welcomeNotice: strings.Repeat("é", 40), wantWarning: true, wantContent: "⚠"},
		// Byte 26 lands inside a 4-byte rune (U+1F600).
		{name: "4-byte cut", width: 26, welcomeNotice: strings.Repeat("😀", 30), wantWarning: true, wantContent: "⚠"},
		// "⚠ Last session NOT saved: " (26 cols) + 7×界 (14 cols) = exactly
		// 40 display columns; the fix must keep all 7 runes (no off-by-one).
		{name: "exact boundary at width", width: 40, prevWarn: strings.Repeat("界", 7), wantWarning: true, wantContent: strings.Repeat("界", 7)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertWelcomeWarningUTF8(t, tc)
		})
	}
}

// welcomeWarningCase is a table entry for TestWelcomeWarningTruncationKeepsUTF8.
type welcomeWarningCase struct {
	name          string
	width         int
	welcomeNotice string
	prevWarn      string
	wantWarning   bool
	wantContent   string
}

func assertWelcomeWarningUTF8(t *testing.T, tc welcomeWarningCase) {
	// width < 70 forces the deterministic text hero, so the only ⚠
	// in the frame is the warning banner itself.
	m := newTUIModel(makeTestSession(), nil, true)
	m.ready = true
	m.mode = modeWelcome
	m.height = 40
	m.width = tc.width
	m.welcomeNotice = tc.welcomeNotice
	m.prevAutoSaveWarn = tc.prevWarn

	raw := m.View()
	plain := stripANSI(raw)
	rawLines := strings.Split(raw, "\n")
	plainLines := strings.Split(plain, "\n")
	if len(rawLines) != len(plainLines) {
		t.Fatalf("raw/stripped line count mismatch: %d vs %d", len(rawLines), len(plainLines))
	}

	var warnLine string
	for i := range plainLines {
		if !utf8.ValidString(rawLines[i]) {
			t.Fatalf("line %d is invalid UTF-8 (U+FFFD risk): %q", i, rawLines[i])
		}
		if strings.Contains(plainLines[i], "⚠") {
			if warnLine != "" {
				t.Fatalf("multiple ⚠ lines: %q and %q", warnLine, plainLines[i])
			}
			// lipgloss.JoinVertical pads every line to the widest line in
			// the frame (the hint line can exceed the terminal width), so
			// the padded frame line is wider than the banner content and
			// would fail the width assertion below for every subtest.
			// Measure the banner on its trimmed content instead.
			warnLine = strings.TrimRight(plainLines[i], " ")
		}
	}
	if !tc.wantWarning {
		if warnLine != "" {
			t.Fatalf("empty warning rendered a banner: %q", warnLine)
		}
		return
	}
	if warnLine == "" {
		t.Fatalf("expected a ⚠ warning line in:\n%s", plain)
	}
	if w := lipgloss.Width(warnLine); w > tc.width {
		t.Fatalf("warning line width %d exceeds terminal width %d: %q", w, tc.width, warnLine)
	}
	if tc.wantContent != "" && !strings.Contains(warnLine, tc.wantContent) {
		t.Fatalf("warning line missing %q:\n%q", tc.wantContent, warnLine)
	}
}
