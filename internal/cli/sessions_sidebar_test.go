package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

func TestSessionsSidebarMoveSelectsSession(t *testing.T) {
	rows := []chat.SessionInfo{{Name: "first"}, {Name: "second"}}
	sidebar := newSessionsSidebar()

	sidebar.move(rows, 2)
	selected, ok := sidebar.selected(rows)
	if !ok {
		t.Fatal("selected() returned no session after move")
	}
	if selected.Name != "second" {
		t.Fatalf("selected session = %q, want %q", selected.Name, "second")
	}
}

func TestSessionsSidebarDoubleClickRequiresSameRecentRow(t *testing.T) {
	sidebar := newSessionsSidebar()
	now := time.Now()

	if sidebar.doubleClick(1, now) {
		t.Fatal("first click activated a row")
	}
	if sidebar.doubleClick(2, now.Add(time.Millisecond)) {
		t.Fatal("different row activated a row")
	}
	if !sidebar.doubleClick(2, now.Add(2*time.Millisecond)) {
		t.Fatal("second click on the same row did not activate it")
	}
}

func TestSessionsSidebarFirstRowIsNewSession(t *testing.T) {
	sidebar := newSessionsSidebar()

	if !sidebar.selectsNewSession(nil) {
		t.Fatal("first sidebar row must select a new session without saved sessions")
	}
	view := stripANSI(sidebar.view(nil, 28, 8, true))
	for _, want := range []string{"▸ New session", "────────"} {
		if !strings.Contains(view, want) {
			t.Fatalf("sidebar view missing %q: %q", want, view)
		}
	}
}

func TestSessionsSidebarViewRendersRowsAndFocus(t *testing.T) {
	sidebar := newSessionsSidebar()
	rows := []chat.SessionInfo{{Name: "first"}, {Name: "second"}}
	sidebar.move(rows, 2)

	view := stripANSI(sidebar.view(rows, 28, 8, true))
	for _, want := range []string{"sessions", "New session", "first", "▸ second", "enter open"} {
		if !strings.Contains(view, want) {
			t.Fatalf("sidebar view missing %q: %q", want, view)
		}
	}
}

func TestSessionsSidebarMoveClampsAtBounds(t *testing.T) {
	rows := []chat.SessionInfo{{Name: "only"}}
	sidebar := newSessionsSidebar()

	sidebar.move(rows, -1)
	if sidebar.cursor != 0 {
		t.Fatalf("cursor = %d after move above first row, want 0", sidebar.cursor)
	}
	sidebar.move(rows, 2)
	if sidebar.cursor != 1 {
		t.Fatalf("cursor = %d after move below last row, want 1", sidebar.cursor)
	}
}

func TestSessionsSidebarSelectedClampsAfterRowsShrink(t *testing.T) {
	sidebar := newSessionsSidebar()
	sidebar.move([]chat.SessionInfo{{Name: "first"}, {Name: "second"}, {Name: "third"}}, 3)
	rows := []chat.SessionInfo{{Name: "first"}, {Name: "second"}}

	selected, ok := sidebar.selected(rows)
	if !ok {
		t.Fatal("selected() returned no session after rows shrink")
	}
	if selected.Name != "second" {
		t.Fatalf("selected session = %q, want %q", selected.Name, "second")
	}
}

func TestSessionsSidebarViewKeepsSelectedRowVisible(t *testing.T) {
	rows := []chat.SessionInfo{
		{Name: "one"}, {Name: "two"}, {Name: "three"}, {Name: "four"},
		{Name: "five"}, {Name: "six"}, {Name: "seven"},
	}
	sidebar := newSessionsSidebar()
	sidebar.move(rows, len(rows))

	view := stripANSI(sidebar.view(rows, 28, 5, true))
	if !strings.Contains(view, "▸ seven") {
		t.Fatalf("selected row is not visible: %q", view)
	}
}
