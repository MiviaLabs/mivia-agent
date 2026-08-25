package conversation

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestConversation_QueueOverlay_NavigationAndDeletion(t *testing.T) {
	s := sized(t, 2)
	s.queue = []string{"item A", "item B", "item C"}

	// Open queue overlay via ctrl+up
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if !s.queueOverlay.Active() {
		t.Fatal("expected queueOverlay to be active")
	}

	// Down moves to item B
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyDown})

	// Press 'd' to delete selected (item B)
	s, _ = press(t, s, key("d"))

	if len(s.queue) != 2 || s.queue[0] != "item A" || s.queue[1] != "item C" {
		t.Fatalf("unexpected queue after deletion: %v", s.queue)
	}

	// Press Enter on item C -> should remove from queue and set composer text
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyEnter})

	if s.queueOverlay.Active() {
		t.Fatal("expected queueOverlay to close after Enter")
	}
	if len(s.queue) != 1 || s.queue[0] != "item A" {
		t.Fatalf("unexpected queue after Enter: %v", s.queue)
	}
	if s.composer.Value() != "item C" {
		t.Fatalf("expected composer value 'item C', got %q", s.composer.Value())
	}
}

func TestConversation_QueueOverlay_DismissOnEsc(t *testing.T) {
	s := sized(t, 1)
	s.queue = []string{"task 1"}

	// Open queue
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if !s.queueOverlay.Active() {
		t.Fatal("expected queue active")
	}

	// Press Esc
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyEsc})
	if s.queueOverlay.Active() {
		t.Fatal("expected queue closed on Esc")
	}
	if len(s.queue) != 1 {
		t.Fatalf("expected queue intact, got %v", s.queue)
	}
}
