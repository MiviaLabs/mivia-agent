package transcript

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// readEndEvent pushes one finished read_file call as a direct tool.end
// (no live start block), the shape a fast lookup arrives in.
func readEndEvent(id, path string) uievent.Event {
	return uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{
			ToolCallID: id,
			Name:       "read_file",
			OK:         true,
			Result:     fmt.Sprintf("contents of %s\nline two\n", path),
			DurationMS: 12,
		},
	}
}

// TestReadOnlyRunsCoalesceIntoOneLeaderRow pins transcript-polish.md R2:
// two consecutive collapsed read-only lookups draw as ONE leader row -
// display-only coalescing. The children stay real blocks: clicking the
// leader row dissolves the run back into per-block headers, each still
// individually collapsible, and the dump keeps every block.
func TestReadOnlyRunsCoalesceIntoOneLeaderRow(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 30)
	for _, id := range []string{"a", "b"} {
		m, _ = m.HandleEvent(readEndEvent(id, "internal/storage/"+id+".go"))
	}

	rows := strings.Split(ansi.Strip(m.View()), "\n")
	leaders := 0
	headers := 0
	for _, r := range rows {
		switch {
		case strings.Contains(r, "Read") && strings.Contains(r, "2 files"):
			leaders++
		case strings.Contains(r, "read_file"):
			headers++
		}
	}
	if leaders != 1 {
		t.Errorf("got %d leader rows, want exactly one:\\n%s", leaders, strings.Join(rows, "\n"))
	}
	if headers != 0 {
		t.Errorf("coalesced runs must hide the per-block headers, got %d:\\n%s", headers, strings.Join(rows, "\n"))
	}

	// Click the leader row: the run dissolves into per-block headers.
	spans := m.layout()
	leaderRow := -1
	for _, s := range spans {
		if s.runSize > 0 {
			leaderRow = s.top
		}
	}
	if leaderRow < 0 {
		t.Fatal("no leader run found in the layout")
	}
	m, ok := m.ToggleBlockAtScreenRow(leaderRow - m.Offset())
	if !ok {
		t.Fatal("clicking the leader row did not register")
	}
	rows = strings.Split(ansi.Strip(m.View()), "\n")
	headers = 0
	for _, r := range rows {
		if strings.Contains(r, "read_file") {
			headers++
		}
	}
	if headers != 2 {
		t.Errorf("after the click got %d per-block headers, want 2:\\n%s", headers, strings.Join(rows, "\n"))
	}

	// The dump never coalesces: every read keeps its own header.
	dump := m.Dump()
	if got := strings.Count(dump, "read_file"); got != 2 {
		t.Errorf("dump shows %d read_file headers, want 2 (coalescing is display-only)", got)
	}
}

// TestSpaceOnHiddenChildOpensTheRun pins the keyboard contract of R2:
// the focus walk still lands on each coalesced child, and toggling a
// hidden child opens the whole run - the row the user sees is the run's
// leader, so space must mean "show me these".
func TestSpaceOnHiddenChildOpensTheRun(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 30)
	for _, id := range []string{"a", "b", "c"} {
		m, _ = m.HandleEvent(readEndEvent(id, id+".go"))
	}
	m.focus = 2 // the LAST member: deep inside the run
	m = m.syncFocus()

	next, ok := m.ToggleFocused()
	if !ok {
		t.Fatal("toggle on a hidden run member must open the run")
	}
	rows := strings.Split(ansi.Strip(next.View()), "\n")
	headers := 0
	for _, r := range rows {
		if strings.Contains(r, "read_file") {
			headers++
		}
	}
	if headers != 3 {
		t.Errorf("got %d per-block headers after opening the run, want 3:\\n%s", headers, strings.Join(rows, "\n"))
	}
}

// TestFailedOrStateChangingBlocksNeverCoalesce pins R2's guard rails: a
// failed lookup and a state-changing tool break any run around them,
// and a lone read never grows a leader row.
func TestFailedOrStateChangingBlocksNeverCoalesce(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 30)
	m, _ = m.HandleEvent(readEndEvent("a", "a.go"))
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "b", Name: "read_file", OK: false, Err: "denied", DurationMS: 3},
	})
	m, _ = m.HandleEvent(readEndEvent("c", "c.go"))

	rows := strings.Split(ansi.Strip(m.View()), "\n")
	for _, r := range rows {
		if strings.Contains(r, "Read") && strings.Contains(r, "files") {
			t.Errorf("a failed lookup must break the run, got leader row %q:\\n%s", r, strings.Join(rows, "\n"))
		}
	}
}
