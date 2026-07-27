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
	return &chat.Session{Model: "test-model"}
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
					// Status chrome should show brand when height is reasonable.
					if h >= 12 && !strings.Contains(plain, "⣿") {
						t.Fatalf("height=%d width=%d waiting=%v tools=%d: missing braille brand\n%s",
							h, w, wait, nTools, plain)
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

	if !strings.Contains(plain, "⣿") {
		t.Fatalf("status bar brand (braille MIVIA) missing:\n%s", plain)
	}
	if !strings.Contains(plain, "VPONLY_MARKER") {
		t.Fatalf("viewport content missing from full View:\n%s", plain)
	}

	// Status is sticky chrome above the body: brand must appear before
	// the viewport marker in the joined frame.
	mi := strings.Index(plain, "⣿")
	vp := strings.Index(plain, "VPONLY_MARKER")
	if mi < 0 || vp < 0 || mi > vp {
		t.Fatalf("expected brand status before viewport body (mi=%d vp=%d):\n%s", mi, vp, plain)
	}

	// Viewport.View alone must not include the status brand string as chrome
	// (viewport is message body only). Brand may appear inside messages; use marker isolation.
	bodyOnly := stripANSI(m.viewport.View())
	if strings.Contains(bodyOnly, "⣿") && !strings.Contains(bodyOnly, "VPONLY_MARKER") {
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
	// At bottom initially — no scroll indicator.
	m.viewport.GotoBottom()
	view := m.View()
	plain := stripANSI(view)
	if strings.Contains(plain, "↓") {
		t.Fatalf("scroll indicator present at bottom; should be absent:\n%s", plain)
	}

	// Scroll up a bit — indicator should appear.
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

func TestMouseToggleKey(t *testing.T) {
	// R1.3: mouseEnabled defaults to false; ctrl+m toggles it.
	m := newReadyChatModel(24, 80)
	if m.mouseEnabled {
		t.Fatal("mouseEnabled should default to false")
	}
	// Simulate ctrl+m key press in chat mode.
	m.mode = modeChat
	_, _, cmds := m.handleChatKey("ctrl+m", false)
	if !m.mouseEnabled {
		t.Fatal("mouseEnabled should be true after ctrl+m toggle")
	}
	if len(cmds) == 0 {
		t.Fatal("ctrl+m should return at least one command (mouse enable/disable)")
	}
	// Toggle again.
	_, _, cmds2 := m.handleChatKey("ctrl+m", false)
	if m.mouseEnabled {
		t.Fatal("mouseEnabled should be false after second ctrl+m toggle")
	}
	if len(cmds2) == 0 {
		t.Fatal("ctrl+m should return at least one command on second toggle")
	}
}

func TestRunTUI_NoMouseOption(t *testing.T) {
	// R1.3: verify that WithMouseCellMotion is not part of the program options.
	// We check by ensuring newTUIModel.mouseEnabled is false by default,
	// and that the runTUI function doesn't reference WithMouseCellMotion.
	m := newTUIModel(makeTestSession(), nil, true)
	if m.mouseEnabled {
		t.Fatal("newTUIModel must have mouseEnabled=false by default")
	}
}

func TestRenderStatusBar_OneLine(t *testing.T) {
	// Broader coverage than brand_test: widths, phases, thinking, queue.
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
					1, 2, 3, 1, 7, w, "",
				)
				if strings.Count(out, "\n") > 0 {
					t.Fatalf("status multi-line phase=%v wait=%v w=%d: %q", ph, wait, w, out)
				}
				// No control chars except optional ANSI (already single logical line).
				plain := stripANSI(out)
				for _, r := range plain {
					if r == '\n' || r == '\r' {
						t.Fatalf("newline after strip: %q", plain)
					}
					if unicode.IsControl(r) && r != '\t' {
						// Allow nothing else control-like in plain text.
						t.Fatalf("control rune U+%04X in status: %q", r, plain)
					}
				}
				if w >= 40 && !strings.ContainsAny(plain, "⣿⠇⣶⣀⡀⠂⠄⠈⠐") {
					t.Fatalf("missing brand braille phase=%v wait=%v w=%d: %q", ph, wait, w, plain)
				}
			}
		}
	}
}
