package transcript

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// collapsedReads returns n collapsed read-only tool-end blocks, the shape
// a run of fast lookups arrives in. Detail carries a path-like string so
// the leader row has targets to list.
func collapsedReads(n int, label string) []Block {
	blocks := make([]Block, n)
	for i := range blocks {
		blocks[i] = Block{
			Kind: uievent.KindToolEnd,
			Header: Header{
				Label:  label,
				Detail: "internal/storage/file" + string(rune('a'+i)) + ".go",
			},
			Body:        []string{"output line"},
			Collapsible: true,
			Collapsed:   true,
		}
	}
	return blocks
}

// runModel returns a sized model whose blocks are one coalesced run of n
// same-class lookups.
func runModel(t *testing.T, width, height, n int, label string) Model {
	t.Helper()
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(width, height)
	m.blocks = collapsedReads(n, label)
	return m
}

// leaderSpan finds the span and index of the first leader row.
func leaderSpan(m Model) (int, span, bool) {
	spans := m.layout()
	for i := range spans {
		if spans[i].runSize > 0 {
			return i, spans[i], true
		}
	}
	return 0, span{}, false
}

// TestExpandRunWithoutARunIsANoOp pins the guard: expandRun on a block
// that leads no run must leave the blocks untouched.
func TestExpandRunWithoutARunIsANoOp(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 20)
	m.blocks = collapsedReads(1, "read_file") // a lone block is not a run
	before := len(m.blocks)
	m.expandRun(0)
	if len(m.blocks) != before {
		t.Errorf("expandRun changed the block count")
	}
	if m.blocks[0].Collapsed != true {
		t.Errorf("expandRun expanded a block that leads no run")
	}
}

// TestLeaderHeadOf pins run-membership routing: the head routes to
// itself, a hidden member routes to its head, and a block outside any
// run reports no head.
func TestLeaderHeadOf(t *testing.T) {
	m := runModel(t, 80, 20, 3, "read_file")
	m.blocks = append(m.blocks, Block{Kind: uievent.KindNotice, Header: Header{Label: "notice"}})

	if head, ok := m.leaderHeadOf(0); !ok || head != 0 {
		t.Errorf("head of the run's own head: got (%d,%v), want (0,true)", head, ok)
	}
	if head, ok := m.leaderHeadOf(2); !ok || head < 0 || head >= 2 {
		t.Errorf("hidden member: got (%d,%v), want a head inside its own run", head, ok)
	}
	if head, ok := m.leaderHeadOf(3); ok {
		t.Errorf("block outside any run: got (%d,%v), want (0,false)", head, ok)
	}
}

// TestLeaderTarget pins the detail shortening: a path collapses to its
// base name, an empty detail contributes nothing, and a bare separator
// falls through whole.
func TestLeaderTarget(t *testing.T) {
	if got := leaderTarget(""); got != "" {
		t.Errorf("empty detail: got %q, want \"\"", got)
	}
	if got := leaderTarget("internal/storage/store.go"); got != "store.go" {
		t.Errorf("path detail: got %q, want the base name", got)
	}
	if got := leaderTarget("/"); got != "/" {
		t.Errorf("bare separator: got %q, want it whole", got)
	}
}

// TestIoaFormats pins the transcript's tiny formatter, including the
// zero case the leader row can never reach on its own.
func TestItoaFormats(t *testing.T) {
	cases := map[int]string{0: "0", 1: "1", 7: "7", 12: "12", 345: "345"}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Errorf("itoa(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestLeaderRowRendersBothClasses pins the leader row's wording: read
// lookups say "Read N files", search lookups say "Searched N queries",
// and the targets list names each member's base.
func TestLeaderRowRendersBothClasses(t *testing.T) {
	m := runModel(t, 80, 20, 2, "read_file")
	i, s, ok := leaderSpan(m)
	if !ok {
		t.Fatal("no leader run in the layout")
	}
	row := ansi.Strip(m.leaderRow(s, i))
	if !strings.Contains(row, "Read") || !strings.Contains(row, "2 files") {
		t.Errorf("read leader row: %q", row)
	}
	if !strings.Contains(row, "filea.go, fileb.go") {
		t.Errorf("read leader row misses its targets: %q", row)
	}

	sm := runModel(t, 80, 20, 2, "grep")
	i, s, ok = leaderSpan(sm)
	if !ok {
		t.Fatal("no leader run in the search layout")
	}
	srow := ansi.Strip(sm.leaderRow(s, i))
	if !strings.Contains(srow, "Searched") || !strings.Contains(srow, "2 queries") {
		t.Errorf("search leader row: %q", srow)
	}
}

// TestLeaderRowClampsHeaderWidth pins the width clamp: at a very wide
// terminal the leader row still renders at the reading measure, not the
// full surface width.
func TestLeaderRowClampsHeaderWidth(t *testing.T) {
	m := runModel(t, 200, 20, 2, "read_file")
	i, s, ok := leaderSpan(m)
	if !ok {
		t.Fatal("no leader run in the layout")
	}
	row := m.leaderRow(s, i)
	if w := ansi.StringWidth(row); w > 92+16 {
		t.Errorf("leader row is %d columns wide, want at most %d", w, 92+16)
	}
}

// TestLeaderRowReverseVideoWhenFocused pins the focus treatment: a
// focused member draws the leader row reverse-video - same visible text
// as the unfocused row, different styling.
func TestLeaderRowReverseVideoWhenFocused(t *testing.T) {
	m := runModel(t, 80, 20, 2, "read_file")
	i, s, ok := leaderSpan(m)
	if !ok {
		t.Fatal("no leader run in the layout")
	}
	plain := m.leaderRow(s, i)

	m.blocks[1].Focused = true // a hidden member focuses the run's row
	focused := m.leaderRow(s, i)

	if focused == plain {
		t.Errorf("a focused run must restyle the leader row")
	}
	if ansi.Strip(focused) != ansi.Strip(plain) {
		t.Errorf("focus must not change the leader row's text:\n%q\n%q", plain, focused)
	}
}

// TestScrollToFocusAnchorsHiddenRunMember pins the hidden-member anchor:
// a run member with no visible row of its own anchors on the leader's
// row, so scrolling to it lands on the row the user can see.
func TestScrollToFocusAnchorsHiddenRunMember(t *testing.T) {
	m := runModel(t, 80, 10, 3, "read_file")
	m.focus = 2 // the last member: hidden inside the run
	m = m.syncFocus().ScrollToFocus()
	if got := m.Offset(); got != 0 {
		t.Errorf("hidden member anchored at offset %d, want the leader's row 0", got)
	}

	// A run further down pulls the window just far enough to show the
	// leader row above a normal block.
	m.blocks = append(collapsedReads(2, "read_file"), m.blocks...)
	m.focus = 1
	m = m.syncFocus().ScrollToFocus()
	spans := m.layout()
	if want := spans[1].top; m.Offset() > want {
		t.Errorf("offset %d lands past the run's leader row %d", m.Offset(), want)
	}
	if m.Offset()+m.Height() < spans[1].top+1 {
		t.Errorf("offset %d hides the focused run's leader row", m.Offset())
	}
}

// TestRowsBreakBeforeSeparatorPastTheLimit pins the viewport's early
// stop: when the next separator row would draw past the window, the row
// walk stops before emitting it.
func TestRowsBreakBeforeSeparatorPastTheLimit(t *testing.T) {
	m := sizedModel(t, 40, 1, 3).ScrollToTop() // follow pins the offset to the tail
	rows := m.Rows()
	if len(rows) != 1 {
		t.Errorf("got %d rows, want exactly the viewport height 1: %q", len(rows), rows)
	}
}

// TestRowsSeparatorBreaksBeforeProsePastTheLimit covers the separator guard:
// a prose block after activity carries sepBefore, and when the walk already
// passed the viewport limit the loop must break before emitting that blank.
func TestRowsSeparatorBreaksBeforeProsePastTheLimit(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(40, 1)
	evs := []uievent.Event{
		noticeEvent("n0|"),
		{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "answer body"}},
		{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "after|"}},
	}
	m = drain(t, m, evs).ScrollToTop()
	rows := m.Rows()
	if len(rows) != 1 {
		t.Errorf("got %d rows, want exactly the viewport height 1: %q", len(rows), rows)
	}
}
