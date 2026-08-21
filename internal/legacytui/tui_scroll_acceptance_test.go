package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// tallScrollModel builds an overflowing chat model ready for Update-path scroll tests.
func tallScrollModel(t *testing.T, vpH, lines int) *TUIModel {
	t.Helper()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.width = 80
	m.height = 40
	m.viewport = newTranscriptViewport(80, vpH)
	for i := 0; i < lines; i++ {
		m.appendBlock(cli.ChatBlock{Kind: cli.ChatBlockSystem, Text: "hist " + itoa(i) + " " + strings.Repeat("z", 12)})
	}
	m.layout()
	m.viewport.Height = vpH
	m.renderVP()
	m.viewport.GotoBottom()
	_ = m.View() // rebuild hitMap for mouse
	return m
}

func transcriptMouseY(m *TUIModel) int {
	// Header is line 0; transcript body starts below. Use a mid-body Y that
	// lands in hitTranscript after View() rebuild.
	if m.height < 8 {
		return 2
	}
	return 3
}

// TestScrollAccept_MouseWheelUpUnfollowsAndStreamDoesNotYank
func TestScrollAccept_MouseWheelUpUnfollowsAndStreamDoesNotYank(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	if !m.followOutput {
		t.Fatal("precondition: follow on")
	}
	preOff := m.viewport.YOffset
	// Scroll far enough to genuinely leave the bottom. Live content no longer
	// grows the transcript (it renders in the live panel), so a single wheel
	// step can land exactly at the last offset and "at bottom" would be true.
	for i := 0; i < 4; i++ {
		_, _ = m.Update(tea.MouseMsg{X: 1, Y: transcriptMouseY(m), Type: tea.MouseWheelUp})
	}
	if m.followOutput {
		t.Fatal("wheel up must unfollow")
	}
	if m.viewport.AtBottom() {
		t.Fatal("precondition: wheel up must leave the bottom")
	}
	if m.viewport.YOffset >= preOff && preOff > 0 {
		t.Fatalf("expected YOffset to move up from bottom %d", preOff)
	}
	saved := m.viewport.YOffset
	_, _ = m.bridge.Write([]byte(strings.Repeat("stream chunk\n", 15)))
	_, _ = m.Update(tuiTickMsg{bridge: m.bridge})
	if m.followOutput {
		t.Fatal("stream tick must not re-follow while scrolled up")
	}
	if m.viewport.YOffset != saved {
		t.Fatalf("YOffset yanked %d → %d on stream tick", saved, m.viewport.YOffset)
	}
}

// TestScrollAccept_MouseWheelDownToBottomRefollows
func TestScrollAccept_MouseWheelDownToBottomRefollows(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	_, _ = m.Update(tea.MouseMsg{X: 1, Y: transcriptMouseY(m), Type: tea.MouseWheelUp})
	_, _ = m.Update(tea.MouseMsg{X: 1, Y: transcriptMouseY(m), Type: tea.MouseWheelUp})
	if m.followOutput {
		t.Fatal("precondition: unfollowed")
	}
	for i := 0; i < 40 && !m.viewport.AtBottom(); i++ {
		_, _ = m.Update(tea.MouseMsg{X: 1, Y: transcriptMouseY(m), Type: tea.MouseWheelDown})
	}
	if !m.viewport.AtBottom() {
		t.Fatal("could not reach bottom via wheel down")
	}
	if !m.followOutput {
		t.Fatal("wheel down to bottom must re-enable follow")
	}
	_, _ = m.bridge.Write([]byte("more stream\nmore\n"))
	_, _ = m.Update(tuiTickMsg{bridge: m.bridge})
	if !m.followOutput || !m.viewport.AtBottom() {
		t.Fatal("following stream must stick to bottom")
	}
}

// TestScrollAccept_PgUpViaUpdateUnfollows
func TestScrollAccept_PgUpViaUpdateUnfollows(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.setFocus(cli.FocusComposer)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.focus != cli.FocusScrollback {
		t.Fatalf("pgup should focus scrollback, got %v", m.focus)
	}
	if m.followOutput {
		t.Fatal("pgup must unfollow")
	}
	saved := m.viewport.YOffset
	_, _ = m.bridge.Write([]byte(strings.Repeat("x\n", 10)))
	_, _ = m.Update(tuiTickMsg{bridge: m.bridge})
	if m.followOutput {
		t.Fatal("must stay unfollowed")
	}
	if m.viewport.YOffset != saved && m.viewport.AtBottom() {
		t.Fatalf("content growth yanked to bottom; YOffset=%d saved=%d", m.viewport.YOffset, saved)
	}
}

// TestScrollAccept_EndKeyJumpToLatestViaUpdate
func TestScrollAccept_EndKeyJumpToLatestViaUpdate(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.noteUserScrolledUp()
	m.viewport.YOffset = 4
	m.setFocus(cli.FocusScrollback)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if !m.followOutput {
		t.Fatal("end must re-enable follow")
	}
	if !m.viewport.AtBottom() {
		t.Fatal("end must GotoBottom")
	}
}

// TestScrollAccept_ConcurrentTicksWhileScrolledUp
func TestScrollAccept_ConcurrentTicksWhileScrolledUp(t *testing.T) {
	m := tallScrollModel(t, 5, 60)
	m.noteUserScrolledUp()
	m.viewport.YOffset = 3
	if m.viewport.AtBottom() {
		t.Fatal("precondition: not at bottom")
	}
	saved := m.viewport.YOffset
	for i := 0; i < 12; i++ {
		_, _ = m.bridge.Write([]byte("s" + itoa(i) + "\n"))
		if i%3 == 0 {
			m.bridge.PushToolWithID(true, "c"+itoa(i), "read_file", `{"path":"a.go"}`)
		}
		_, _ = m.Update(tuiTickMsg{bridge: m.bridge})
		if m.followOutput {
			t.Fatalf("tick %d re-enabled follow", i)
		}
		if m.viewport.YOffset != saved {
			t.Fatalf("tick %d YOffset %d → %d", i, saved, m.viewport.YOffset)
		}
	}
}

// TestScrollAccept_FinishStreamWhileScrolledUpDoesNotYank
func TestScrollAccept_FinishStreamWhileScrolledUpDoesNotYank(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.noteUserScrolledUp()
	m.viewport.YOffset = 5
	if m.viewport.AtBottom() {
		t.Fatal("precondition: not at bottom")
	}
	saved := m.viewport.YOffset
	m.streamBuf.WriteString("final answer body\n" + strings.Repeat("line\n", 5))
	_ = m.finishStream(nil)
	if m.waiting {
		t.Fatal("must finish")
	}
	if m.followOutput {
		// finishStream → renderVP: if wasAtBottom was false, shouldFollow keeps false
		// unless AtBottom after SetContent. With overflow, should stay unfollowed.
	}
	if m.viewport.AtBottom() && !m.followOutput {
		// clamp may land at bottom only if content shrunk - not expected
	}
	if m.viewport.YOffset != saved && m.viewport.AtBottom() {
		t.Fatalf("finish yanked to bottom; YOffset=%d saved=%d", m.viewport.YOffset, saved)
	}
	// Prefer exact preserve when unfollowed.
	if !m.followOutput && m.viewport.YOffset != saved {
		// applyFollowScroll restores savedOffset - must match
		t.Fatalf("YOffset not preserved on finish: got %d want %d", m.viewport.YOffset, saved)
	}
}

// TestScrollAccept_ScrollIndicatorLatestInView
func TestScrollAccept_ScrollIndicatorLatestInView(t *testing.T) {
	m := tallScrollModel(t, 6, 50)
	m.waiting = true
	m.noteUserScrolledUp()
	m.viewport.YOffset = 2
	plain := cli.StripANSI(m.View())
	if !strings.Contains(plain, "↓") || !strings.Contains(plain, "latest") {
		t.Fatalf("expected ↓ latest while waiting unfollowed, got %q", plain)
	}
	m.jumpToLatest()
	plain2 := cli.StripANSI(m.View())
	if strings.Contains(plain2, "latest") {
		t.Fatalf("latest indicator should clear at bottom, got %q", plain2)
	}
}
