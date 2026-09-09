package conversation

// Window-geometry and separator-budget tests for the session picker, split
// out of sessionpicker_test.go to keep it under the go-structure soft cap.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestSessionPickerSeparatorDrawsOnceAndKeepsSelectionVisible pins the
// window geometry the first draft got wrong: exactly one separator for
// the whole route block, rendered line count within budget, and the
// selected bottom route row never clipped out of the dialog.
func TestSessionPickerSeparatorDrawsOnceAndKeepsSelectionVisible(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)

	sessions := []ports.SessionSummary{}
	for i := range 5 {
		sessions = append(sessions, ports.SessionSummary{
			ID: fmt.Sprintf("s-%d", i), Title: fmt.Sprintf("Plain %d", i),
			UpdatedAt: now.Add(-time.Duration(i+1) * time.Hour), State: "done",
		})
	}
	for i := range 3 {
		name := fmt.Sprintf("wt%d", i)
		sessions = append(sessions, ports.SessionSummary{
			ID: "worktree:" + name, Title: "Worktree · " + name,
			UpdatedAt: now.Add(-time.Hour), State: "done",
			Worktree: name, WorktreeRoute: true, WorktreeDir: "/repo/wt/" + name,
		})
	}
	sp := newSessionPicker(th, theme.TierTrueColor, sessions)
	sp.cursor = len(sessions) - 1 // last route row

	const bodyRows = 6
	view := ansi.Strip(sp.ViewWindow(th, theme.TierTrueColor, 100, bodyRows, now))

	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > bodyRows {
		t.Fatalf("rendered %d lines in a %d-row budget:\n%s", len(lines), bodyRows, view)
	}
	if got := strings.Count(view, "-- in worktree --"); got != 1 {
		t.Fatalf("separator count = %d, want exactly 1:\n%s", got, view)
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "worktree:wt2") && !strings.Contains(last, ">") {
		t.Fatalf("selected row clipped from view; last line = %q\n%s", last, view)
	}

	// Scrolling with a tiny window that holds only route rows still shows
	// the block label once.
	sp2 := newSessionPicker(th, theme.TierTrueColor, sessions[6:])
	sp2.cursor = 1
	view2 := ansi.Strip(sp2.ViewWindow(th, theme.TierTrueColor, 100, 4, now))
	if strings.Count(view2, "-- in worktree --") != 1 {
		t.Fatalf("route-block window lost its label:\n%s", view2)
	}
	lines2 := strings.Split(strings.TrimRight(view2, "\n"), "\n")
	if len(lines2) > 4 {
		t.Fatalf("route-block window overflowed: %d lines\n%s", len(lines2), view2)
	}
}

// TestSessionPickerWindowHeadInsideRouteBlock covers the case the first
// geometry fix missed in tests: a window that OPENS inside the route
// block (start > 0 lands on a route row) still shows exactly one label,
// its drawn-line count stays within budget, and the selected row survives.
func TestSessionPickerWindowHeadInsideRouteBlock(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)

	sessions := []ports.SessionSummary{}
	for i := range 5 {
		sessions = append(sessions, ports.SessionSummary{
			ID: fmt.Sprintf("s-%d", i), Title: fmt.Sprintf("Plain %d", i),
			UpdatedAt: now.Add(-time.Duration(i+1) * time.Hour), State: "done",
		})
	}
	for i := range 3 {
		name := fmt.Sprintf("wt%d", i)
		sessions = append(sessions, ports.SessionSummary{
			ID: "worktree:" + name, Title: "Worktree · " + name,
			UpdatedAt: now.Add(-time.Hour), State: "done",
			Worktree: name, WorktreeRoute: true, WorktreeDir: "/repo/wt/" + name,
		})
	}
	sp := newSessionPicker(th, theme.TierTrueColor, sessions)
	sp.cursor = 7 // deep inside the route block

	view := ansi.Strip(sp.ViewWindow(th, theme.TierTrueColor, 100, 4, now))
	if got := strings.Count(view, "-- in worktree --"); got != 1 {
		t.Fatalf("separator count = %d, want 1:\n%s", got, view)
	}
	if strings.Contains(view, "Plain") {
		t.Errorf("window head inside route block must not show plain rows:\n%s", view)
	}
	if !strings.Contains(view, ">") {
		t.Fatalf("selected marker lost:\n%s", view)
	}
}

// TestSessionPickerSaturatedWorktreeRowStaysInBudget pins the glyph
// width-accounting fix: a worktree row whose title saturates the title
// column must render within innerWidth, timestamp included - the first
// draft charged the glyph to no one and dialogClip shaved the row tail.
func TestSessionPickerSaturatedWorktreeRowStaysInBudget(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)

	sessions := []ports.SessionSummary{
		{ID: "s-plain", Title: strings.Repeat("P", 60), UpdatedAt: now.Add(-time.Hour), State: "done"},
		{
			ID: "bound-wt1", Title: strings.Repeat("W", 60), UpdatedAt: now.Add(-2 * time.Hour),
			State: "done", Worktree: "wt1", WorktreeDir: "/repo/.mivia/worktrees/wt1",
		},
		{
			ID: "worktree:wt2", Title: "Worktree · " + strings.Repeat("R", 60),
			UpdatedAt: now.Add(-3 * time.Hour), State: "done",
			Worktree: "wt2", WorktreeRoute: true, WorktreeDir: "/repo/.mivia/worktrees/wt2",
		},
	}
	sp := newSessionPicker(th, theme.TierTrueColor, sessions)

	const width = 44
	view := ansi.Strip(sp.ViewWindow(th, theme.TierTrueColor, width, 5, now))
	for i, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("row %d is %d cols wide in a %d-col budget:\n%q", i, w, width, line)
		}
	}
	// The timestamp must survive on the saturated worktree row too.
	if !strings.Contains(view, "1h ago") || !strings.Contains(view, "2h ago") {
		t.Errorf("timestamp clipped from a saturated row:\n%s", view)
	}
}

// TestSessionPickerWindowFillsBudgetWhenBoundaryOffWindow pins the other
// half of the reservation rule: when route rows exist but the block
// boundary is NOT inside the window (cursor in the plain region), no line
// is reserved for the separator - the window shows a full budget of rows.
func TestSessionPickerWindowFillsBudgetWhenBoundaryOffWindow(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)

	sessions := []ports.SessionSummary{}
	for i := range 12 {
		sessions = append(sessions, ports.SessionSummary{
			ID: fmt.Sprintf("s-%d", i), Title: fmt.Sprintf("Plain %d", i),
			UpdatedAt: now.Add(-time.Duration(i+1) * time.Hour), State: "done",
		})
	}
	for i := range 2 {
		name := fmt.Sprintf("wt%d", i)
		sessions = append(sessions, ports.SessionSummary{
			ID: "worktree:" + name, Title: "Worktree · " + name,
			UpdatedAt: now.Add(-time.Hour), State: "done",
			Worktree: name, WorktreeRoute: true, WorktreeDir: "/repo/wt/" + name,
		})
	}
	sp := newSessionPicker(th, theme.TierTrueColor, sessions)
	sp.cursor = 0 // window stays in the plain region; boundary at index 12 is off-window

	const bodyRows = 10
	view := ansi.Strip(sp.ViewWindow(th, theme.TierTrueColor, 100, bodyRows, now))
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if strings.Contains(view, "-- in worktree --") {
		t.Fatalf("boundary off-window but separator rendered:\n%s", view)
	}
	if len(lines) != bodyRows {
		t.Fatalf("rendered %d lines in a %d-row budget with the boundary off-window:\n%s",
			len(lines), bodyRows, view)
	}
}

// TestSessionPickerDegenerateHeightsNeverOverflow pins the tiny-terminal
// contract: the picker never emits more lines than the budget, preferring
// the selected ROW over its chrome (separator label, filter footer) -
// render.Dialog clips surplus bottom lines, which previously hid the
// selection behind the label.
func TestSessionPickerDegenerateHeightsNeverOverflow(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)

	sessions := []ports.SessionSummary{}
	for i := range 4 {
		sessions = append(sessions, ports.SessionSummary{
			ID: fmt.Sprintf("s-%d", i), Title: fmt.Sprintf("Plain %d", i),
			UpdatedAt: now.Add(-time.Duration(i+1) * time.Hour), State: "done",
		})
	}
	for i := range 3 {
		name := fmt.Sprintf("wt%d", i)
		sessions = append(sessions, ports.SessionSummary{
			ID: "worktree:" + name, Title: "Worktree · " + name,
			UpdatedAt: now.Add(-time.Hour), State: "done",
			Worktree: name, WorktreeRoute: true, WorktreeDir: "/repo/wt/" + name,
		})
	}

	// bodyRows=1, cursor deep in the route block (start > 0): the one
	// visible line must be the selected row, not the block label.
	sp := newSessionPicker(th, theme.TierTrueColor, sessions)
	sp.cursor = 6
	view := ansi.Strip(sp.ViewWindow(th, theme.TierTrueColor, 100, 1, now))
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("bodyRows=1 rendered %d lines:\n%s", len(lines), view)
	}
	if !strings.Contains(lines[0], "wt2") {
		t.Fatalf("bodyRows=1 shows %q, want the selected route row", lines[0])
	}

	// bodyRows=1 with an active filter: the footer is dropped, the row wins.
	sp1 := newSessionPicker(th, theme.TierTrueColor, sessions)
	sp1.filter = "Plain"
	sp1.clampCursor()
	view1 := ansi.Strip(sp1.ViewWindow(th, theme.TierTrueColor, 100, 1, now))
	lines1 := strings.Split(strings.TrimRight(view1, "\n"), "\n")
	if len(lines1) != 1 {
		t.Fatalf("bodyRows=1 with filter rendered %d lines:\n%s", len(lines1), view1)
	}
	if strings.TrimSpace(lines1[0]) == "/Plain" {
		t.Fatal("bodyRows=1 spent its only line on the filter footer")
	}

	// bodyRows=2 with an active filter inside the route block: budget holds.
	sp2 := newSessionPicker(th, theme.TierTrueColor, sessions)
	sp2.filter = "wt"
	sp2.clampCursor()
	sp2.cursor = 2
	view2 := ansi.Strip(sp2.ViewWindow(th, theme.TierTrueColor, 100, 2, now))
	lines2 := strings.Split(strings.TrimRight(view2, "\n"), "\n")
	if len(lines2) > 2 {
		t.Fatalf("bodyRows=2 rendered %d lines:\n%s", len(lines2), view2)
	}
}

// TestSeparatorVisible_EmptyWindow pins the degenerate guard directly: an
// empty [start,end) window renders nothing, so no label is visible.
func TestSeparatorVisible_EmptyWindow(t *testing.T) {
	rows := []ports.SessionSummary{{ID: "r", WorktreeRoute: true, Worktree: "wt1"}}
	if separatorVisible(rows, 1, 1) {
		t.Fatal("empty window reported a visible separator")
	}
	if !separatorVisible(rows, 0, 1) {
		t.Fatal("route-opening window lost its separator")
	}
}
