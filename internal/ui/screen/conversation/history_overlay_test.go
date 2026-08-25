package conversation

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func TestConversation_HistoryOverlay_TriggerAndSelection(t *testing.T) {
	s := sized(t, 2)

	// Simulate loaded history with prior user messages
	s.LoadHistory([]ports.Message{
		{Role: "user", Text: "first prompt"},
		{Role: "assistant", Text: "first response"},
		{Role: "user", Text: "second prompt"},
		{Role: "assistant", Text: "second response"},
	})

	if s.history.Len() != 2 {
		t.Fatalf("expected 2 history items, got %d", s.history.Len())
	}
	if s.history.Active() {
		t.Fatal("history should not be active initially")
	}

	// Press Up arrow on empty composer -> opens history overlay
	upKey := tea.KeyPressMsg{Code: tea.KeyUp}
	next, _ := press(t, s, upKey)

	if !next.history.Active() {
		t.Fatal("expected history overlay to be active after pressing Up arrow")
	}
	if next.history.Selected() != "second prompt" {
		t.Fatalf("expected newest message selected, got %q", next.history.Selected())
	}

	// Navigate up to "first prompt"
	next, _ = press(t, next, upKey)
	if next.history.Selected() != "first prompt" {
		t.Fatalf("expected first prompt selected, got %q", next.history.Selected())
	}

	// Press Enter -> should populate composer and close history
	enterKey := tea.KeyPressMsg{Code: tea.KeyEnter}
	next, _ = press(t, next, enterKey)

	if next.history.Active() {
		t.Fatal("history should be closed after Enter")
	}
	if next.composer.Value() != "first prompt" {
		t.Fatalf("expected composer value to be 'first prompt', got %q", next.composer.Value())
	}
}

func TestConversation_HistoryOverlay_DismissOnEsc(t *testing.T) {
	s := sized(t, 1)
	s.LoadHistory([]ports.Message{
		{Role: "user", Text: "hello world"},
	})

	// Open history with Up arrow
	next, _ := press(t, s, tea.KeyPressMsg{Code: tea.KeyUp})
	if !next.history.Active() {
		t.Fatal("expected history active")
	}

	// Press Esc -> should close without changing composer
	escKey := tea.KeyPressMsg{Code: tea.KeyEsc}
	next, _ = press(t, next, escKey)

	if next.history.Active() {
		t.Fatal("expected history closed on Esc")
	}
	if next.composer.Value() != "" {
		t.Fatalf("expected empty composer, got %q", next.composer.Value())
	}
}

func TestConversation_HistoryOverlay_NotTriggeredOnMultiLine(t *testing.T) {
	s := sized(t, 1)
	s.LoadHistory([]ports.Message{
		{Role: "user", Text: "earlier message"},
	})

	// Set multi-line value in composer
	s.composer.SetValue("line 1\nline 2")
	// Position cursor on line 2
	s.composer.ClickToColumn(10) // stays on line 2 after SetValue which moves cursor to end

	// Press Up arrow -> should move cursor up within composer, NOT open history
	next, _ := press(t, s, tea.KeyPressMsg{Code: tea.KeyUp})
	if next.history.Active() {
		t.Fatal("history should not open when cursor is moving within multi-line input")
	}
}
