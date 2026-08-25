package queue

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func loadTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}

func TestQueue_OpenAndNavigation(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	items := []string{"msg 1", "msg 2", "msg 3", "msg 4", "msg 5"}
	m.Open(items)

	if !m.Active() {
		t.Fatal("queue overlay should be active after Open()")
	}
	if m.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", m.cursor)
	}

	m.Down()
	if m.cursor != 1 {
		t.Errorf("expected cursor at 1, got %d", m.cursor)
	}

	m.Up()
	if m.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", m.cursor)
	}
}

func TestQueue_DeleteSelected(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	items := []string{"msg 1", "msg 2", "msg 3"}
	m.Open(items)

	m.Down() // select "msg 2"
	deleted, ok := m.DeleteSelected()
	if !ok || deleted != "msg 2" {
		t.Fatalf("expected deleted 'msg 2', got %q (ok=%v)", deleted, ok)
	}

	remaining := m.Items()
	if len(remaining) != 2 || remaining[0] != "msg 1" || remaining[1] != "msg 3" {
		t.Fatalf("unexpected remaining items: %v", remaining)
	}
}

func TestQueue_ViewEmpty(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	m.Open(nil)
	if m.Height() != 3 {
		t.Errorf("expected empty height 3, got %d", m.Height())
	}

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "No queued messages.") {
		t.Errorf("expected 'No queued messages.' in view:\n%s", view)
	}
}
