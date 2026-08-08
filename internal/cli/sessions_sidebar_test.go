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
	for _, want := range []string{"▸ + New session", "────────"} {
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
	for _, want := range []string{"sessions", "New session", "first", "▸ second", "Enter open"} {
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

func TestSessionsSidebarHeaderShowsSessionCount(t *testing.T) {
	rows := []chat.SessionInfo{{Name: "first"}, {Name: "second"}}
	view := stripANSI(newSessionsSidebar().view(rows, 32, 8, false))
	lines := strings.Split(view, "\n")

	if !strings.Contains(lines[0], "Sessions 2") {
		t.Fatalf("header = %q, want session title and count", lines[0])
	}
}

func TestSessionsSidebarPolishedViewShowsActionAndSelectedMetadata(t *testing.T) {
	rows := []chat.SessionInfo{
		{Name: "first", MessageCount: 3},
		{Name: "second", MessageCount: 12},
	}
	sidebar := newSessionsSidebar()
	sidebar.move(rows, 2)

	view := stripANSI(sidebar.view(rows, 36, 10, true))
	for _, want := range []string{"+ New session", "▸ second", "12 messages"} {
		if !strings.Contains(view, want) {
			t.Fatalf("polished sidebar view missing %q: %q", want, view)
		}
	}
}

func TestSessionsSidebarFooterChangesForNewAndSavedSession(t *testing.T) {
	rows := []chat.SessionInfo{{Name: "saved"}}
	sidebar := newSessionsSidebar()

	newView := stripANSI(sidebar.view(rows, 36, 8, true))
	if !strings.Contains(newView, "Enter new · Esc close") {
		t.Fatalf("new-session footer is not compact and state-aware: %q", newView)
	}

	sidebar.move(rows, 1)
	savedView := stripANSI(sidebar.view(rows, 36, 8, true))
	if !strings.Contains(savedView, "Enter open · d delete") {
		t.Fatalf("saved-session footer is not compact and state-aware: %q", savedView)
	}
}

func TestSessionsSidebarScrollShowsPositionCue(t *testing.T) {
	rows := []chat.SessionInfo{
		{Name: "one"}, {Name: "two"}, {Name: "three"}, {Name: "four"},
		{Name: "five"}, {Name: "six"}, {Name: "seven"}, {Name: "eight"},
	}
	sidebar := newSessionsSidebar()
	sidebar.move(rows, len(rows))

	view := stripANSI(sidebar.view(rows, 36, 8, true))
	if !strings.Contains(view, "/ 8") {
		t.Fatalf("long session list has no scroll position cue: %q", view)
	}
}

func TestSessionsSidebarMarksExactActiveSession(t *testing.T) {
	rows := []chat.SessionInfo{
		{Name: "same", Dir: "/one", MessageCount: 1},
		{Name: "same", Dir: "/two", MessageCount: 2},
	}
	view := stripANSI(newSessionsSidebar().viewWithActive(rows, 28, 10, false, &rows[1]))

	if got := strings.Count(view, "current"); got != 1 {
		t.Fatalf("current markers = %d, want 1: %q", got, view)
	}
}

func TestSessionsSidebarMetadataRowMapsToItsSession(t *testing.T) {
	rows := []chat.SessionInfo{{Name: "first"}, {Name: "second"}}
	sidebar := newSessionsSidebar()

	got, ok := sidebar.cursorAt(rows, 28, 10, sidebarRowsY+1)
	if !ok || got != 1 {
		t.Fatalf("metadata row cursor = %d, %t; want first session", got, ok)
	}
}

func TestSessionsSidebarNarrowRowsMapToTheirOwnMouseTarget(t *testing.T) {
	rows := []chat.SessionInfo{{Name: "first"}, {Name: "second"}}
	sidebar := newSessionsSidebar()

	got, ok := sidebar.cursorAt(rows, 20, 10, sidebarRowsY+1)
	if !ok || got != 2 {
		t.Fatalf("narrow row cursor = %d, %t; want second session", got, ok)
	}
}

func TestSessionsSidebarKeepsCurrentMarkerAtNarrowWidth(t *testing.T) {
	row := chat.SessionInfo{Name: "a-session-name-that-fills-the-sidebar"}
	view := stripANSI(newSessionsSidebar().viewWithActive([]chat.SessionInfo{row}, 20, 10, false, &row))

	if !strings.Contains(view, "current") {
		t.Fatalf("active marker is not visible: %q", view)
	}
}

func TestSessionsSidebarRendersDestructiveConfirmationAsError(t *testing.T) {
	sidebar := newSessionsSidebar()
	sidebar.confirm = confirmDeleteOne
	view := sidebar.view([]chat.SessionInfo{{Name: "saved"}}, 28, 8, true)
	want := tuiErrorStyle.Render(sidebarPad(" delete selected session? y/n", 28))

	if !strings.Contains(view, want) {
		t.Fatalf("confirmation is not error styled: %q", view)
	}
}

func TestSessionsSidebarDoesNotHitClippedRows(t *testing.T) {
	sidebar := newSessionsSidebar()
	rows := []chat.SessionInfo{{Name: "saved"}}

	if _, ok := sidebar.cursorAt(rows, 20, 1, sidebarNewSessionY); ok {
		t.Fatal("clipped new-session row accepted a mouse hit")
	}
	if _, ok := sidebar.cursorAt(rows, 20, 4, sidebarRowsY); ok {
		t.Fatal("clipped saved-session row accepted a mouse hit")
	}
}
