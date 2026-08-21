package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/charmbracelet/lipgloss"
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
	view := cli.StripANSI(sidebar.view(nil, 28, 8, true))
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

	view := cli.StripANSI(sidebar.view(rows, 28, 8, true))
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

	view := cli.StripANSI(sidebar.view(rows, 28, 5, true))
	if !strings.Contains(view, "▸ seven") {
		t.Fatalf("selected row is not visible: %q", view)
	}
}

func TestSessionsSidebarHeaderShowsSessionCount(t *testing.T) {
	rows := []chat.SessionInfo{{Name: "first"}, {Name: "second"}}
	view := cli.StripANSI(newSessionsSidebar().view(rows, 32, 8, false))
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

	view := cli.StripANSI(sidebar.view(rows, 36, 10, true))
	for _, want := range []string{"+ New session", "▸ second", "12 messages"} {
		if !strings.Contains(view, want) {
			t.Fatalf("polished sidebar view missing %q: %q", want, view)
		}
	}
}

func TestSessionsSidebarFooterChangesForNewAndSavedSession(t *testing.T) {
	rows := []chat.SessionInfo{{Name: "saved"}}
	sidebar := newSessionsSidebar()

	newView := cli.StripANSI(sidebar.view(rows, 36, 8, true))
	if !strings.Contains(newView, "Enter new · Esc close") {
		t.Fatalf("new-session footer is not compact and state-aware: %q", newView)
	}

	sidebar.move(rows, 1)
	savedView := cli.StripANSI(sidebar.view(rows, 36, 8, true))
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

	view := cli.StripANSI(sidebar.view(rows, 36, 8, true))
	if !strings.Contains(view, "/ 8") {
		t.Fatalf("long session list has no scroll position cue: %q", view)
	}
}

func TestSessionsSidebarMarksExactActiveSession(t *testing.T) {
	rows := []chat.SessionInfo{
		{Name: "same", Dir: "/one", MessageCount: 1},
		{Name: "same", Dir: "/two", MessageCount: 2},
	}
	view := cli.StripANSI(newSessionsSidebar().viewWithActive(rows, 28, 10, false, &rows[1], liveStatusIdle))

	if got := strings.Count(view, "current"); got != 1 {
		t.Fatalf("current markers = %d, want 1: %q", got, view)
	}
	if got := strings.Count(view, "●"); got != 1 {
		t.Fatalf("status dots = %d, want 1: %q", got, view)
	}
	lines := strings.Split(view, "\n")
	var activeLine string
	for _, line := range lines {
		if strings.Contains(line, "current") {
			activeLine = line
		}
	}
	if activeLine == "" || !strings.Contains(activeLine, "●") {
		t.Fatalf("active duplicate-name row missing its dot: %q", view)
	}
	for _, line := range lines {
		if line != activeLine && strings.Contains(line, "●") {
			t.Fatalf("dot on a non-active row: %q", line)
		}
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
	view := cli.StripANSI(newSessionsSidebar().viewWithActive([]chat.SessionInfo{row}, 20, 10, false, &row, liveStatusIdle))

	if !strings.Contains(view, "current") {
		t.Fatalf("active marker is not visible: %q", view)
	}
}

func TestSessionsSidebarRendersDestructiveConfirmationAsError(t *testing.T) {
	sidebar := newSessionsSidebar()
	sidebar.confirm = confirmDeleteOne
	view := sidebar.view([]chat.SessionInfo{{Name: "saved"}}, 28, 8, true)
	want := TUIErrorStyle.Render(sidebarPad(" delete selected session? y/n", 28))

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

// currentRowLine returns the first row line that carries the identity marker.
func currentRowLine(view string) string {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "current") {
			return line
		}
	}
	return ""
}

func TestSessionsSidebarActiveRowShowsStatusDotInEveryStatus(t *testing.T) {
	withANSI256(t)
	statuses := []sidebarLiveStatus{liveStatusIdle, liveStatusThinking, liveStatusStreaming, liveStatusTools}
	for _, status := range statuses {
		row := chat.SessionInfo{Name: "alpha"}
		view := newSessionsSidebar().viewWithActive([]chat.SessionInfo{row}, 28, 10, false, &row, status)
		line := currentRowLine(view)
		if !strings.Contains(line, "current") {
			t.Fatalf("status %v: identity marker missing: %q", status, line)
		}
		want := sidebarLiveDot(status, true)
		if !strings.Contains(line, want) {
			t.Fatalf("status %v: row line %q missing styled dot %q", status, line, want)
		}
	}
}

func TestSessionsSidebarStatusDotDistinctPerStatus(t *testing.T) {
	withANSI256(t)
	cases := []struct {
		status sidebarLiveStatus
		glyph  string
		color  string
	}{
		{liveStatusIdle, "●", brandColorIdle},
		{liveStatusThinking, "◔", BrandColorThinking},
		{liveStatusStreaming, "◐", brandColorStream},
		{liveStatusTools, "◉", brandColorTools},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		row := chat.SessionInfo{Name: "alpha"}
		view := newSessionsSidebar().viewWithActive([]chat.SessionInfo{row}, 28, 10, false, &row, tc.status)
		line := currentRowLine(view)
		if !strings.Contains(line, tc.glyph) {
			t.Fatalf("status %v: row line %q missing glyph %q", tc.status, line, tc.glyph)
		}
		// Lipgloss folds 256-color indices 0-15 into the basic 16-color SGR
		// codes, so pin the mapped brand color through lipgloss itself rather
		// than a fixed 38;5;<n>m form.
		want := lipgloss.NewStyle().Foreground(lipgloss.Color(tc.color)).Render(tc.glyph)
		if !strings.Contains(line, want) {
			t.Fatalf("status %v: row line %q missing styled dot %q for color %s", tc.status, line, want, tc.color)
		}
		if seen[want] {
			t.Fatalf("status %v: styled dot %q duplicates another status", tc.status, want)
		}
		seen[want] = true
	}
}

func TestSessionsSidebarNonActiveRowsHaveNoDot(t *testing.T) {
	rows := []chat.SessionInfo{{Name: "one"}, {Name: "two"}, {Name: "three"}}
	view := cli.StripANSI(newSessionsSidebar().viewWithActive(rows, 28, 10, false, &rows[1], liveStatusIdle))
	lines := strings.Split(view, "\n")

	if got := strings.Count(view, "current"); got != 1 {
		t.Fatalf("current markers = %d, want 1: %q", got, view)
	}
	if got := strings.Count(view, "●"); got != 1 {
		t.Fatalf("status dots = %d, want 1: %q", got, view)
	}
	for _, line := range lines {
		switch {
		case strings.Contains(line, "current"):
			if !strings.Contains(line, "●") {
				t.Fatalf("active row missing its dot: %q", line)
			}
		case strings.Contains(line, "one"), strings.Contains(line, "three"):
			if strings.Contains(line, "●") {
				t.Fatalf("non-active row shows a dot: %q", line)
			}
		}
	}
}

func TestSessionsSidebarNarrowWidthKeepsIdentityDropsStatusColor(t *testing.T) {
	withANSI256(t)
	row := chat.SessionInfo{Name: "a-session-name-that-fills-the-sidebar"}
	view := newSessionsSidebar().viewWithActive([]chat.SessionInfo{row}, 20, 10, false, &row, liveStatusStreaming)
	line := currentRowLine(view)

	if !strings.Contains(line, "current") {
		t.Fatalf("identity marker missing at narrow width: %q", line)
	}
	if !strings.Contains(line, "◐") {
		t.Fatalf("narrow row dropped the status glyph: %q", line)
	}
	if strings.Contains(line, "38;5;"+brandColorStream+"m") {
		t.Fatalf("narrow row kept the status color: %q", line)
	}
	plain := cli.StripANSI(line)
	if strings.Count(plain, "\n") > 0 || cli.RuneWidth(plain) > 20 {
		t.Fatalf("narrow row overflows or wraps: %q", plain)
	}
}

func TestSessionsSidebarNilActiveRendersNoDot(t *testing.T) {
	rows := []chat.SessionInfo{{Name: "one"}, {Name: "two"}}
	view := cli.StripANSI(newSessionsSidebar().viewWithActive(rows, 28, 10, false, nil, liveStatusIdle))

	if strings.Contains(view, "current") {
		t.Fatalf("nil active session renders an identity marker: %q", view)
	}
	for _, glyph := range []string{"●", "◔", "◐", "◉"} {
		if strings.Contains(view, glyph) {
			t.Fatalf("nil active session renders dot %q: %q", glyph, view)
		}
	}
}

func TestSessionsSidebarEmptyRowsRenderNoDot(t *testing.T) {
	view := cli.StripANSI(newSessionsSidebar().viewWithActive(nil, 28, 10, false, nil, liveStatusIdle))

	if !strings.Contains(view, "no saved sessions") {
		t.Fatalf("empty list missing its placeholder: %q", view)
	}
	for _, glyph := range []string{"●", "◔", "◐", "◉"} {
		if strings.Contains(view, glyph) {
			t.Fatalf("empty list renders dot %q: %q", glyph, view)
		}
	}
}
