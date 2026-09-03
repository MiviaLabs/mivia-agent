package composer

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

func commands() []Command {
	return []Command{
		{Name: "agent", Desc: "pick an agent"},
		{Name: "agents", Desc: "list agents"},
		{Name: "clear", Desc: "clear the transcript"},
		{Name: "compact", Desc: "compact the context"},
		{Name: "context", Desc: "show context usage"},
		{Name: "cost", Desc: "show spend"},
		{Name: "help", Desc: "show help"},
		{Name: "model", Desc: "pick a model"},
		{Name: "quit", Desc: "exit"},
	}
}

func typed(t *testing.T, text string) Model {
	t.Helper()
	m := New(loadTheme(t), theme.TierASCII, 60)
	m.SetCommands(commands())
	m.SetValue(text)
	return m
}

// TestTriggerIsStartAnchored pins rule 5.1. A bare "/" anywhere would
// open the command menu inside "src/foo", which is the reported defect.
func TestTriggerIsStartAnchored(t *testing.T) {
	cases := []struct {
		text string
		want bool
		why  string
	}{
		{"/", true, "a lone slash opens the menu"},
		{"/ag", true, "a slash prefix opens the menu"},
		{"src/foo", false, "a slash mid-word must not trigger"},
		{"look at src/foo.go", false, "a slash inside a sentence must not trigger"},
		{" /agent", false, "the slash must be the first character"},
		{"/agent run", false, "arguments have started; the command is chosen"},
		{"", false, "an empty composer has no menu"},
	}
	for _, c := range cases {
		m := typed(t, c.text)
		if got := m.MenuActive(); got != c.want {
			t.Errorf("%q: MenuActive() = %v, want %v (%s)", c.text, got, c.want, c.why)
		}
	}
}

// TestEveryCandidateIsScored pins rule 5.7: the cap is on RENDERED ROWS,
// never on the candidate set. Capping candidates first is what made
// later matches unreachable in opencode.
func TestEveryCandidateIsScored(t *testing.T) {
	m := typed(t, "/c")
	// clear, compact, context, cost all match "c" - more than one screen
	// would show if the rendered cap were applied to the match set.
	if got := len(m.menu.matches); got != 4 {
		t.Fatalf("got %d matches, want every candidate containing \"c\" scored", got)
	}

	// A match that sorts last must still be reachable by moving down.
	last := m.menu.matches[len(m.menu.matches)-1].Name
	for i := 0; i < len(m.menu.matches)-1; i++ {
		m = m.MenuNext()
	}
	if got := m.menu.matches[m.menu.cursor].Name; got != last {
		t.Errorf("cursor reached %q, want the last match %q", got, last)
	}
}

// TestRenderedRowsAreCapped is the other half of rule 5.7.
func TestRenderedRowsAreCapped(t *testing.T) {
	m := typed(t, "/")
	if len(m.menu.matches) <= uikitconfig.MaxCompletionRows {
		t.Fatalf("fixture has %d matches; it must exceed the row cap %d to test it",
			len(m.menu.matches), uikitconfig.MaxCompletionRows)
	}
	view := m.menu.view(m.Theme, m.Tier, m.width)
	rows := strings.Split(view, "\n")
	// MaxCompletionRows candidate rows, plus the "n of m" count row.
	if len(rows) != uikitconfig.MaxCompletionRows+1 {
		t.Errorf("rendered %d rows, want %d plus one count row", len(rows), uikitconfig.MaxCompletionRows)
	}
	if !strings.Contains(ansi.Strip(view), "of 9") {
		t.Errorf("view does not state the full match count, so the cap is silent:\n%s", view)
	}
}

// TestScrollingKeepsTheCursorVisible pins the window: moving past the
// last rendered row scrolls rather than losing the highlight.
func TestScrollingKeepsTheCursorVisible(t *testing.T) {
	m := typed(t, "/")
	for i := 0; i < uikitconfig.MaxCompletionRows+1; i++ {
		m = m.MenuNext()
	}
	if m.menu.cursor < m.menu.offset || m.menu.cursor >= m.menu.offset+uikitconfig.MaxCompletionRows {
		t.Errorf("cursor %d is outside the rendered window [%d,%d)",
			m.menu.cursor, m.menu.offset, m.menu.offset+uikitconfig.MaxCompletionRows)
	}
	want := m.menu.matches[m.menu.cursor].Name
	if !strings.Contains(ansi.Strip(m.menu.view(m.Theme, m.Tier, m.width)), want) {
		t.Errorf("the highlighted match %q is not among the rendered rows", want)
	}
}

func TestMenuWrapsInBothDirections(t *testing.T) {
	m := typed(t, "/")
	n := len(m.menu.matches)
	m = m.MenuPrev()
	if m.menu.cursor != n-1 {
		t.Errorf("got cursor %d, want a wrap to the last match %d", m.menu.cursor, n-1)
	}
	m = m.MenuNext()
	if m.menu.cursor != 0 {
		t.Errorf("got cursor %d, want a wrap back to the first match", m.menu.cursor)
	}
}

// TestAcceptCommonPrefix pins rule 6.1's Tab behaviour.
func TestAcceptCommonPrefix(t *testing.T) {
	m := typed(t, "/ag") // agent, agents
	next, grew := m.AcceptCommonPrefix()
	if !grew {
		t.Fatal("expected Tab to extend the input to the shared prefix")
	}
	if got := next.Value(); got != "/agent" {
		t.Errorf("got %q, want the common prefix of agent and agents", got)
	}
	if !next.MenuActive() {
		t.Error("the menu must stay open after a prefix accept: agents is still reachable")
	}
	// A second Tab adds nothing, so the caller falls back to selecting.
	if _, grew := next.AcceptCommonPrefix(); grew {
		t.Error("expected no further growth once the input is the common prefix")
	}
}

func TestAcceptSelectedAddsNoTrailingSpace(t *testing.T) {
	// Rule 5.6: a trailing space runs the default subcommand on Enter.
	m := typed(t, "/mo").AcceptSelected()
	if got := m.Value(); got != "/model" {
		t.Errorf("got %q, want exactly the command with no trailing space", got)
	}
	if m.MenuActive() {
		t.Error("the menu must close once a command is accepted")
	}
}

func TestMenuDismissKeepsTheText(t *testing.T) {
	m := typed(t, "/mo").MenuDismiss()
	if m.MenuActive() {
		t.Error("expected the menu closed")
	}
	if got := m.Value(); got != "/mo" {
		t.Errorf("got %q, want the typed text untouched by a dismiss", got)
	}
}

// TestRankingOrder pins the ranking, which is what stops the list
// reshuffling between keystrokes for no visible reason.
//
// Among prefix matches the SHORTER name leads: it is the closer match to
// what was typed, and it is the one the user reaches with fewer keys.
// Equal-length names stay alphabetical.
func TestRankingOrder(t *testing.T) {
	got := rank(commands(), "co")
	if len(got) < 3 {
		t.Fatalf("got %d matches, want every prefix match", len(got))
	}
	for i, want := range []string{"cost", "compact", "context"} {
		if got[i].Name != want {
			t.Errorf("rank %d = %q, want %q", i, got[i].Name, want)
		}
	}
}

// TestSubsequenceRanksBelowPrefix pins the tier order itself.
func TestSubsequenceRanksBelowPrefix(t *testing.T) {
	cmds := []Command{{Name: "zebra-cat"}, {Name: "cat"}}
	got := rank(cmds, "cat")
	if got[0].Name != "cat" {
		t.Errorf("got %q first, want the prefix match \"cat\" ahead of the substring match", got[0].Name)
	}
}

func TestRankEmptyQueryKeepsEveryCandidate(t *testing.T) {
	if got, want := len(rank(commands(), "")), len(commands()); got != want {
		t.Errorf("got %d matches for an empty query, want all %d", got, want)
	}
}

func TestIsSubsequence(t *testing.T) {
	cases := []struct {
		q, s string
		want bool
	}{
		{"", "abc", true},
		{"ac", "abc", true},
		{"abc", "abc", true},
		{"ca", "abc", false},
		{"abcd", "abc", false},
	}
	for _, c := range cases {
		if got := isSubsequence(c.q, c.s); got != c.want {
			t.Errorf("isSubsequence(%q,%q) = %v, want %v", c.q, c.s, got, c.want)
		}
	}
}

// TestViewKeepsTheInputAboveTheBottomPadding pins rule 2.8: with a menu
// open, View still draws only the bar (the menu is an overlay), and the
// input row sits one above the bottom padding row, exactly where it sits
// with the menu closed.
func TestViewKeepsTheInputAboveTheBottomPadding(t *testing.T) {
	m := typed(t, "/c")
	rows := strings.Split(m.View(), "\n")
	if len(rows) < 2 {
		t.Fatalf("got %d rows, want the input plus its padding rows", len(rows))
	}
	if !strings.Contains(ansi.Strip(rows[len(rows)-2]), "/c") {
		t.Errorf("the input must sit one row above the bottom padding, got %q", rows[len(rows)-2])
	}
	if got, want := m.Height(), len(rows); got != want {
		t.Errorf("Height() = %d but View drew %d rows", got, want)
	}
}

func TestHeightWithNoMenu(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	if got := m.Height(); got != 3 {
		t.Errorf("got height %d, want 3 with no menu showing (1 input row + 2 padding rows)", got)
	}
	narrow := New(loadTheme(t), theme.TierASCII, 4)
	if got := narrow.Height(); got != 1 {
		t.Errorf("narrow height %d, want 1", got)
	}
}

func TestClearClosesTheMenu(t *testing.T) {
	m := typed(t, "/ag")
	m.Clear()
	if m.MenuActive() {
		t.Error("the menu must close when the composer is cleared")
	}
}

// TestTypingRefreshesTheMenu drives the real key path rather than
// SetValue, so it proves Update keeps the menu in step with the text.
func TestTypingRefreshesTheMenu(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 60)
	m.SetCommands(commands())
	for _, r := range "/ag" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if !m.MenuActive() {
		t.Fatalf("typing %q did not open the menu", m.Value())
	}
	if got := len(m.menu.matches); got != 2 {
		t.Errorf("got %d matches for /ag, want agent and agents", got)
	}
}

func TestAcceptOnAClosedMenuIsANoOp(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	m.SetCommands(commands())
	m.SetValue("hello")
	if got := m.AcceptSelected().Value(); got != "hello" {
		t.Errorf("got %q, want the text untouched with no menu open", got)
	}
	if _, grew := m.AcceptCommonPrefix(); grew {
		t.Error("expected no prefix growth with no menu open")
	}
	if m.MenuNext().Value() != "hello" || m.MenuPrev().Value() != "hello" {
		t.Error("menu movement with no menu open changed the text")
	}
}

func TestViewIsEmptyWhenNothingMatches(t *testing.T) {
	m := typed(t, "/zzzz")
	if m.MenuActive() {
		t.Error("expected no menu when nothing matches")
	}
	if got := m.menu.view(m.Theme, m.Tier, m.width); got != "" {
		t.Errorf("got %q, want no menu rows", got)
	}
}

func TestSubsequenceOnlyMatchStillRanks(t *testing.T) {
	// "abt" is not a prefix and not a substring of "abstract", but its
	// letters appear in order, so the command stays reachable.
	got := rank([]Command{{Name: "abstract"}}, "abt")
	if len(got) != 1 {
		t.Fatalf("got %d matches, want the subsequence match kept", len(got))
	}
}

func TestCommonPrefixDegenerateCases(t *testing.T) {
	cases := []struct {
		name  string
		in    []Command
		want  string
		notes string
	}{
		{"no matches", nil, "", "nothing to share"},
		{"one match", []Command{{Name: "model"}}, "model", "the whole name"},
		{"shared prefix", []Command{{Name: "model"}, {Name: "modes"}}, "mode", "shrinks to the shared run"},
		{"nothing shared", []Command{{Name: "model"}, {Name: "quit"}}, "", "no common first character"},
		{"one is a prefix of the other", []Command{{Name: "agent"}, {Name: "agents"}}, "agent", "the shorter name wins"},
	}
	for _, c := range cases {
		m := menu{matches: c.in}
		if got := m.commonPrefix(); got != c.want {
			t.Errorf("%s: got %q, want %q (%s)", c.name, got, c.want, c.notes)
		}
	}
}

func TestItoa(t *testing.T) {
	for _, c := range []struct {
		in   int
		want string
	}{{0, "0"}, {1, "1"}, {9, "9"}, {10, "10"}, {12345, "12345"}} {
		if got := itoa(c.in); got != c.want {
			t.Errorf("itoa(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestOffsetNeverGoesNegative covers the clamp: a shrinking match set
// must not leave the render window pointing before the first row.
func TestOffsetNeverGoesNegative(t *testing.T) {
	m := menu{matches: make([]Command, 3), cursor: 0, offset: -5}
	m.clampOffset()
	if m.offset < 0 {
		t.Errorf("got offset %d, want it clamped to 0", m.offset)
	}
}

// TestComposerPopupCarriesTheHintNotTheBar: the navigation hint lives in
// the popup's footer row while a menu is open; the bar itself stays bare.
func TestComposerPopupCarriesTheHintNotTheBar(t *testing.T) {
	m := typed(t, "/mo")
	m.SetWidth(80)
	if !m.MenuActive() {
		t.Fatal("expected menu active")
	}
	pop := strings.Join(m.Popup(), "\n")
	if !strings.Contains(pop, "navigate") || !strings.Contains(pop, "complete") {
		t.Errorf("expected navigation and completion hint in the popup, got:\n%s", pop)
	}
	if bar := ansi.Strip(m.View()); strings.Contains(bar, "navigate") || strings.Contains(bar, "[") {
		t.Errorf("the bar must not carry the hint, got:\n%s", bar)
	}

	// Narrow terminal fallback
	m.SetWidth(40)
	popNarrow := strings.Join(m.Popup(), "\n")
	if !strings.Contains(popNarrow, "[ / Commands ]") {
		t.Errorf("expected fallback hint in the narrow popup, got:\n%s", popNarrow)
	}
}

// TestPopupIsAnOverlayNotRows: opening a menu adds rows to Popup and none
// to the bar - Height is what it was, so the transcript never reflows.
func TestPopupIsAnOverlayNotRows(t *testing.T) {
	closed := New(loadTheme(t), theme.TierTrueColor, 80)
	open := typed(t, "/mo")
	open.SetWidth(80)
	if !open.MenuActive() {
		t.Fatal("expected menu active")
	}
	if got, want := open.Height(), closed.Height(); got != want {
		t.Errorf("Height with menu open = %d, want %d (the popup must not claim bar rows)", got, want)
	}
	if got := strings.Count(open.View(), "\n") + 1; got != open.Height() {
		t.Errorf("View draws %d rows, Height says %d", got, open.Height())
	}
	pop := open.Popup()
	if len(pop) < 2 || open.MenuRows() != len(pop) {
		t.Fatalf("popup rows = %d, MenuRows = %d; want items + footer and the two to agree", len(pop), open.MenuRows())
	}
	for i, row := range pop {
		if w := ansi.StringWidth(row); w != open.PopupWidth() {
			t.Errorf("popup row %d is %d cols, want %d", i, w, open.PopupWidth())
		}
	}
	// The popup spans the bar's padded region: left padding, popup, right padding.
	if 2*open.PopupOffset()+open.PopupWidth() != 80 {
		t.Errorf("popup must sit inside the bar's padding: 2*%d + %d != 80", open.PopupOffset(), open.PopupWidth())
	}
	if closed.Popup() != nil {
		t.Error("no menu, no popup")
	}
}
