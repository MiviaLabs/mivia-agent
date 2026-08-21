package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Program-level scroll acceptance: tea.Program event loop + live pollCmd,
// not only direct m.Update. Closes the residual "Program scroll timing" gap.

func TestScrollProg_WheelUpUnfollow_PollDoesNotYank(t *testing.T) {
	sp := startScrollProgram(t, nil)

	var saved int
	var follow bool
	sp.probe(func(m *TUIModel) {
		follow = m.followOutput
	})
	if !follow {
		t.Fatal("precondition: following")
	}

	for i := 0; i < 4; i++ {
		sp.send(tea.MouseMsg{X: 1, Y: 3, Type: tea.MouseWheelUp})
	}
	if !sp.waitUntil(2*time.Second, func(m *TUIModel) bool {
		return !m.followOutput && !m.viewport.AtBottom()
	}) {
		t.Fatal("wheel up must unfollow and leave the bottom under Program")
	}
	sp.probe(func(m *TUIModel) { saved = m.viewport.YOffset })

	// Stream via bridge; live pollCmd drains on notify - wait for streamBuf.
	const marker = "stream chunk under program"
	sp.probe(func(m *TUIModel) {
		_, _ = m.bridge.Write([]byte(strings.Repeat(marker+"\n", 12)))
	})
	if !sp.waitUntil(2*time.Second, func(m *TUIModel) bool {
		return strings.Contains(m.streamBuf.String(), marker)
	}) {
		t.Fatal("stream never drained into model under Program")
	}
	sp.probe(func(m *TUIModel) {
		if m.followOutput {
			t.Error("poll/stream must not re-follow while scrolled up")
		}
		if m.viewport.YOffset != saved {
			t.Errorf("YOffset yanked %d → %d under Program poll", saved, m.viewport.YOffset)
		}
	})
}

func TestScrollProg_WheelDownToBottomRefollows(t *testing.T) {
	sp := startScrollProgram(t, nil)
	sp.send(tea.MouseMsg{X: 1, Y: 3, Type: tea.MouseWheelUp})
	sp.send(tea.MouseMsg{X: 1, Y: 3, Type: tea.MouseWheelUp})
	if !sp.waitUntil(time.Second, func(m *TUIModel) bool { return !m.followOutput }) {
		t.Fatal("precondition unfollow")
	}
	for i := 0; i < 40; i++ {
		sp.send(tea.MouseMsg{X: 1, Y: 3, Type: tea.MouseWheelDown})
		if sp.waitUntil(200*time.Millisecond, func(m *TUIModel) bool {
			return m.viewport.AtBottom() && m.followOutput
		}) {
			return
		}
	}
	t.Fatal("wheel down to bottom must re-enable follow under Program")
}

func TestScrollProg_PgUpUnfollow_UnderPoll(t *testing.T) {
	sp := startScrollProgram(t, func(m *TUIModel) {
		m.setFocus(cli.FocusComposer)
	})
	sp.send(tea.KeyMsg{Type: tea.KeyPgUp})
	if !sp.waitUntil(time.Second, func(m *TUIModel) bool {
		return m.focus == cli.FocusScrollback && !m.followOutput
	}) {
		t.Fatal("pgup under Program must focus scrollback and unfollow")
	}
	var saved int
	const marker = "pgup-stream-x"
	sp.probe(func(m *TUIModel) {
		saved = m.viewport.YOffset
		_, _ = m.bridge.Write([]byte(strings.Repeat(marker+"\n", 10)))
	})
	if !sp.waitUntil(2*time.Second, func(m *TUIModel) bool {
		return strings.Contains(m.streamBuf.String(), marker)
	}) {
		t.Fatal("stream not drained")
	}
	sp.probe(func(m *TUIModel) {
		if m.followOutput {
			t.Error("must stay unfollowed after poll")
		}
		if m.viewport.AtBottom() && m.viewport.YOffset != saved {
			t.Errorf("yanked to bottom; YOffset=%d saved=%d", m.viewport.YOffset, saved)
		}
	})
}

func TestScrollProg_EndKeyJumpToLatest(t *testing.T) {
	sp := startScrollProgram(t, func(m *TUIModel) {
		m.noteUserScrolledUp()
		m.viewport.YOffset = 4
		m.setFocus(cli.FocusScrollback)
	})
	sp.send(tea.KeyMsg{Type: tea.KeyEnd})
	if !sp.waitUntil(time.Second, func(m *TUIModel) bool {
		return m.followOutput && m.viewport.AtBottom()
	}) {
		t.Fatal("end under Program must jump to latest")
	}
}

func TestScrollProg_ConcurrentPollWhileScrolledUp(t *testing.T) {
	sp := startScrollProgram(t, func(m *TUIModel) {
		m.noteUserScrolledUp()
		m.viewport.YOffset = 3
	})
	var saved int
	sp.probe(func(m *TUIModel) {
		if m.viewport.AtBottom() {
			t.Fatal("precondition: not at bottom")
		}
		saved = m.viewport.YOffset
	})
	for i := 0; i < 8; i++ {
		marker := "s" + itoa(i)
		sp.probe(func(m *TUIModel) {
			_, _ = m.bridge.Write([]byte(marker + "\n"))
			if i%3 == 0 {
				m.bridge.PushToolWithID(true, "c"+itoa(i), "read_file", `{"path":"a.go"}`)
			}
		})
		if !sp.waitUntil(2*time.Second, func(m *TUIModel) bool {
			return strings.Contains(m.streamBuf.String(), marker)
		}) {
			t.Fatalf("tick %d stream not drained", i)
		}
		sp.probe(func(m *TUIModel) {
			if m.followOutput {
				t.Errorf("tick %d re-enabled follow", i)
			}
			if m.viewport.YOffset != saved {
				t.Errorf("tick %d YOffset %d → %d", i, saved, m.viewport.YOffset)
			}
		})
	}
}

func TestScrollProg_LatestIndicatorInViewString(t *testing.T) {
	sp := startScrollProgram(t, func(m *TUIModel) {
		m.waiting = true
		m.noteUserScrolledUp()
		m.viewport.YOffset = 2
	})
	var plain string
	sp.probe(func(m *TUIModel) {
		plain = cli.StripANSI(m.View())
	})
	if !strings.Contains(plain, "↓") || !strings.Contains(plain, "latest") {
		t.Fatalf("expected ↓ latest under Program View, got %q", plain)
	}
	sp.send(tea.KeyMsg{Type: tea.KeyEnd})
	if !sp.waitUntil(time.Second, func(m *TUIModel) bool { return m.followOutput }) {
		t.Fatal("end should follow")
	}
	sp.probe(func(m *TUIModel) {
		plain2 := cli.StripANSI(m.View())
		if strings.Contains(plain2, "latest") {
			t.Fatalf("latest should clear at bottom: %q", plain2)
		}
	})
}

// Paint / glyph timing: View frames under Program stay bounded and show
// latest content when following (not a real pixel raster, but frame SoT).
func TestScrollProg_PaintFollowShowsLatestMarker(t *testing.T) {
	sp := startScrollProgram(t, nil)
	const marker = "PAINT_MARKER_LATEST_XYZ"
	sp.probe(func(m *TUIModel) {
		if !m.followOutput {
			m.jumpToLatest()
		}
		_, _ = m.bridge.Write([]byte(strings.Repeat("tail\n", 5) + marker + "\n"))
	})
	if !sp.waitUntil(2*time.Second, func(m *TUIModel) bool {
		return strings.Contains(m.streamBuf.String(), marker)
	}) {
		t.Fatal("stream not drained")
	}
	var plain string
	var lines int
	var h int
	sp.probe(func(m *TUIModel) {
		// Force stream chrome into viewport content path.
		m.renderStreamVP()
		view := m.View()
		plain = cli.StripANSI(view)
		lines = strings.Count(view, "\n") + 1
		h = m.height
	})
	if !strings.Contains(plain, marker) {
		t.Fatalf("following paint frame must show latest marker; view=%q", plain)
	}
	if h > 0 && lines > h+4 {
		// Allow small lipgloss padding slack over terminal height.
		t.Fatalf("paint frame too tall: lines=%d height=%d", lines, h)
	}
}

func TestScrollProg_PaintUnfollowPreservesFrameBudget(t *testing.T) {
	sp := startScrollProgram(t, func(m *TUIModel) {
		m.noteUserScrolledUp()
		m.viewport.YOffset = 2
	})
	const marker = "PAINT_HIDDEN_WHILE_UP"
	sp.probe(func(m *TUIModel) {
		_, _ = m.bridge.Write([]byte(marker + "\n"))
	})
	if !sp.waitUntil(2*time.Second, func(m *TUIModel) bool {
		return strings.Contains(m.streamBuf.String(), marker)
	}) {
		t.Fatal("stream not drained")
	}
	var lines, h int
	var follow bool
	sp.probe(func(m *TUIModel) {
		follow = m.followOutput
		view := m.View()
		lines = strings.Count(view, "\n") + 1
		h = m.height
	})
	if follow {
		t.Fatal("must stay unfollowed")
	}
	if h > 0 && lines > h+4 {
		t.Fatalf("unfollowed paint frame too tall: lines=%d height=%d", lines, h)
	}
}

func TestScrollIndicator_GlyphWidthBounded(t *testing.T) {
	// Glyph/paint chrome: indicator strings must stay compact for layout.
	ind := cli.StripANSI(renderScrollIndicator(true, 80, true))
	if lipglossWidth(ind) > 16 {
		t.Fatalf("↓ latest indicator too wide: %q width=%d", ind, lipglossWidth(ind))
	}
	ind2 := cli.StripANSI(renderScrollIndicator(true, 80, false))
	if lipglossWidth(ind2) > 8 {
		t.Fatalf("↓ indicator too wide: %q width=%d", ind2, lipglossWidth(ind2))
	}
	if renderScrollIndicator(false, 80, true) != "" {
		t.Fatal("no indicator when at bottom")
	}
}

// lipglossWidth counts display cells without importing lipgloss in every test file.
func lipglossWidth(s string) int {
	// Approximate: rune count is enough for our short ASCII/arrow indicators.
	return len([]rune(s))
}
