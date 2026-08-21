package cli

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModalMouseEventsAreFullySwallowed(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(focusComposer)
	m.textarea.SetValue("draft")
	m.selectedBlockID = "selected"
	m.viewport.YOffset = 2
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = "line"
	}
	m.setOverlay(newDialog("modal", lines))
	beforeOffset := m.viewport.YOffset
	for _, msg := range []tea.MouseMsg{
		{X: 2, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress},
		{X: 2, Y: 2, Button: tea.MouseButtonMiddle, Action: tea.MouseActionRelease},
		{X: 2, Y: 2, Button: tea.MouseButtonRight, Action: tea.MouseActionPress},
		{X: 2, Y: 2, Action: tea.MouseActionMotion},
	} {
		_, _ = m.Update(msg)
	}
	if m.selectedBlockID != "selected" || m.textarea.Value() != "draft" || m.viewport.YOffset != beforeOffset {
		t.Fatalf("modal click mutated chat state: selected=%q draft=%q offset=%d", m.selectedBlockID, m.textarea.Value(), m.viewport.YOffset)
	}
	start := m.overlay.yOffset
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.overlay.yOffset <= start {
		t.Fatalf("modal wheel did not scroll overlay: before=%d after=%d", start, m.overlay.yOffset)
	}
	_, _ = m.Update(tea.MouseMsg{Type: tea.MouseWheelUp})
	if m.viewport.YOffset != beforeOffset {
		t.Fatalf("modal legacy wheel reached transcript: offset=%d", m.viewport.YOffset)
	}
}

func TestAsyncPasteDroppedWhileModalOpens(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.textarea.SetValue("draft")
	m.setOverlay(newDialog("modal", []string{"one"}))
	_, _ = m.Update(pasteTextMsg{text: "late clipboard"})
	if got := m.textarea.Value(); got != "draft" {
		t.Fatalf("async paste leaked into hidden composer: %q", got)
	}
}

func TestFailedPasteDisarmsQuitWhileModalOpen(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.armQuit()
	m.setOverlay(newDialog("modal", []string{"one"}))
	_, _ = m.Update(pasteFailedMsg{err: errors.New("clipboard unavailable")})
	if m.quitArmed() {
		t.Fatal("failed modal paste left the quit arm active")
	}
}

func TestModalOpenSwallowsMouseBeforeView(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.hitMap.rebuild(80, 24, 0, 10, 0, -1, 10, 14, nil, 0)
	m.setOverlay(newDialog("modal", []string{"one"}))
	_, _ = m.Update(tea.MouseMsg{Y: 10, Type: tea.MouseRight})
	if m.selectedBlockID != "" {
		t.Fatalf("stale hit map handled a modal click before View: %q", m.selectedBlockID)
	}
}

func TestModalOpenSwallowsMouseAfterView(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setOverlay(newDialog("modal", []string{"one"}))
	m.View()
	_, _ = m.Update(tea.MouseMsg{Y: 10, Type: tea.MouseRight})
	if m.selectedBlockID != "" {
		t.Fatal("modal click reached hit map after View")
	}
}

func TestModalCloseInvalidatesHitMapBeforeView(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setOverlay(newDialog("modal", []string{"one"}))
	m.View()
	if m.hitMap.frame == 0 {
		t.Fatal("View did not rebuild the base hit map")
	}
	m.setOverlay(nil)
	if m.hitMap.frame != 0 || len(m.hitMap.zones) != 0 {
		t.Fatalf("closing modal left hit map valid: frame=%d zones=%d", m.hitMap.frame, len(m.hitMap.zones))
	}
	_, _ = m.Update(tea.MouseMsg{Y: 10, Type: tea.MouseRight})
	if m.selectedBlockID != "" {
		t.Fatal("close transition ghost-hit stale chat zone")
	}
}

func TestModalCloseRebuildsHitMapAfterView(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setOverlay(newDialog("modal", []string{"one"}))
	m.View()
	m.setOverlay(nil)
	m.View()
	if m.hitMap.frame == 0 {
		t.Fatal("closing modal did not rebuild the chat hit map")
	}
}

func TestModalKeyPrecedenceBeforeAndAfterView(t *testing.T) {
	for _, afterView := range []bool{false, true} {
		m := newReadyChatModel(24, 80)
		m.setOverlay(newDialog("modal", []string{"one"}))
		if afterView {
			m.View()
		}
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
		if cmd == nil {
			t.Fatalf("ctrl+q lost modal precedence afterView=%v", afterView)
		}
	}
}

func TestDialogResizeClampsScrollThroughWindowSize(t *testing.T) {
	m := newReadyChatModel(30, 100)
	m.setOverlay(newDialog("modal", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"}))
	m.overlay.yOffset = 100
	_, _ = m.Update(tea.WindowSizeMsg{Width: 20, Height: 8})
	if m.overlay.yOffset < 0 {
		t.Fatal("resize produced negative offset")
	}
	_, layout := m.overlay.ViewAt(20, 8)
	_ = layout
	rows := m.overlay.displayRows(layout.InnerW)
	if m.overlay.yOffset > Max(0, len(rows)-layout.PageH) {
		t.Fatalf("resize left offset beyond final page: offset=%d rows=%d page=%d", m.overlay.yOffset, len(rows), layout.PageH)
	}
}
