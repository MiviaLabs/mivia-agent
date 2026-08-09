package cli

import (
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/charmbracelet/bubbles/viewport"
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
					2, ph, "gpt-test", wait, 1500*time.Millisecond,
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
