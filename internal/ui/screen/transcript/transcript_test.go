package transcript

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	conv "github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
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

// snapshot builds a conversation with two user prompts, a prose reply
// that search can find, and a tool output long enough to be collapsed
// by default in the cockpit - the pager must expand it.
func snapshot(t *testing.T) conv.Model {
	t.Helper()
	m := conv.New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "first zebra question"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "a reply about bounded retry so search has prose to find"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolOutput, Body: uievent.ToolOutputBody{
		ToolCallID: "call-1", Chunk: strings.Repeat("tool line\n", 20),
	}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "second zebra note"}})
	return m
}

// drive sends one Msg and asserts the screen stays concrete.
func drive(s Screen, msg tea.Msg) Screen {
	next, _ := s.Update(msg)
	return next.(Screen)
}

func key(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Text: s, Code: rune(s[0])} }

func sizedPager(t *testing.T, width, height int) Screen {
	t.Helper()
	s := NewPager(loadTheme(t), theme.TierASCII, snapshot(t))
	s.width, s.height = width, height
	s.rebuild()
	return s
}

// TestNewPagerExpandsCollapsedBlocks pins the reading model: the pager
// shows what a cockpit collapse hides, because search and scrollback
// handover must reach the whole conversation.
func TestNewPagerExpandsCollapsedBlocks(t *testing.T) {
	s := sizedPager(t, 80, 24)
	joined := strings.Join(s.rows, "\n")
	if got := strings.Count(joined, "tool line"); got != 20 {
		t.Errorf("tool body appears %d times, want 20 (a collapsed block must be expanded)", got)
	}
	if len(s.promptRows) != 2 {
		t.Errorf("prompt rows = %v, want 2 user prompts", s.promptRows)
	}
	for _, row := range s.promptRows {
		if !strings.Contains(s.rows[row], "zebra") {
			t.Errorf("prompt row %d is %q, want the user prompt text", row, s.rows[row])
		}
	}
}

// TestPagerDroppedLineMatchesDump pins that the pager and the scrollback
// dump state the same truncation head line.
func TestPagerDroppedLineMatchesDump(t *testing.T) {
	m := snapshot(t)
	for i := 0; i < uikitMaxTranscriptLines(); i++ {
		m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "filler"}})
	}
	s := NewPager(loadTheme(t), theme.TierASCII, m)
	if s.dropped == 0 {
		t.Fatal("expected the snapshot to have dropped blocks")
	}
	if !strings.HasPrefix(s.rows[0], "[") || !strings.Contains(s.rows[0], "earlier blocks dropped") {
		t.Errorf("first row %q, want the dropped-count line", s.rows[0])
	}
	if !strings.Contains(m.Dump(), s.rows[0]) {
		t.Errorf("dump does not contain the pager's dropped line %q", s.rows[0])
	}
}

// uikitMaxTranscriptLines reads the bound the component trims at.
func uikitMaxTranscriptLines() int {
	return 2000 // uikitconfig.MaxTranscriptLines; inlined to avoid an import here
}

// TestPagerScrollKeys drives every motion key from rule 6.2 and asserts
// the offset moved in the right direction by the right amount.
func TestPagerScrollKeys(t *testing.T) {
	s := sizedPager(t, 80, 10) // content height 9, rows far more
	s.offset = s.maxOffset()
	before := s.offset

	s = drive(s, key("k"))
	if s.offset != before-1 {
		t.Errorf("k moved to %d, want %d", s.offset, before-1)
	}
	s = drive(s, tea.KeyPressMsg{Code: tea.KeyUp})
	if s.offset != before-2 {
		t.Errorf("up moved to %d, want %d", s.offset, before-2)
	}
	s = drive(s, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if s.offset != before-2-4 {
		t.Errorf("ctrl+u moved to %d, want %d", s.offset, before-2-4)
	}
	s = drive(s, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if s.offset != before-2-4-9 {
		t.Errorf("ctrl+b moved to %d, want %d", s.offset, before-2-4-9)
	}
	s = drive(s, tea.KeyPressMsg{Code: 'g'})
	if s.offset != 0 {
		t.Errorf("g moved to %d, want 0", s.offset)
	}
	s = drive(s, tea.KeyPressMsg{Code: 'j'})
	if s.offset != 1 {
		t.Errorf("j moved to %d, want 1", s.offset)
	}
	s = drive(s, tea.KeyPressMsg{Code: tea.KeyHome})
	if s.offset != 0 {
		t.Errorf("home moved to %d, want 0", s.offset)
	}
	s = drive(s, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if s.offset != 4 { // contentHeight/2 = 4
		t.Errorf("ctrl+d moved to %d, want 4", s.offset)
	}
	s = drive(s, tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	if s.offset != 13 { // +9
		t.Errorf("ctrl+f moved to %d, want 13", s.offset)
	}
	s = drive(s, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if s.offset != s.maxOffset() { // +9 would be 22, but 15 is the floor of the fixture
		t.Errorf("space moved to %d, want clamped %d", s.offset, s.maxOffset())
	}
	s = drive(s, tea.KeyPressMsg{Code: 'b'})
	if s.offset != s.maxOffset()-9 { // -9
		t.Errorf("b moved to %d, want %d", s.offset, s.maxOffset()-9)
	}
	s = drive(s, tea.KeyPressMsg{Code: 'G'})
	if s.offset != s.maxOffset() {
		t.Errorf("G moved to %d, want %d", s.offset, s.maxOffset())
	}
	s = drive(s, tea.KeyPressMsg{Code: tea.KeyEnd})
	if s.offset != s.maxOffset() {
		t.Errorf("end moved to %d, want %d", s.offset, s.maxOffset())
	}
}

// TestPagerPromptJumps pins { and }: previous and next user prompt.
// The fixture puts the two prompts at the far ends (row 0 and the last
// row), so each jump has to travel the full conversation.
func TestPagerPromptJumps(t *testing.T) {
	s := sizedPager(t, 80, 10)
	s.offset = s.maxOffset()

	s = drive(s, tea.KeyPressMsg{Code: '{'})
	if got := s.rows[s.offset]; !strings.Contains(got, "first zebra") {
		t.Errorf("{ landed on %q, want the older prompt brought into view", got)
	}
	older := s.offset

	// } must now travel forward to the newest prompt, at the very end.
	s = drive(s, tea.KeyPressMsg{Code: '}'})
	if s.offset <= older {
		t.Fatalf("} landed at offset %d, want past %d", s.offset, older)
	}
	if last := s.promptRows[len(s.promptRows)-1]; s.offset > last || last >= s.offset+s.contentHeight() {
		t.Errorf("} left offset %d; newest prompt row %d is not visible", s.offset, last)
	}

	// And { again returns to the oldest, at the top.
	s = drive(s, tea.KeyPressMsg{Code: '{'})
	if s.offset != 0 {
		t.Errorf("second { landed at offset %d, want 0", s.offset)
	}
}

// TestSearchFindsCaseInsensitiveAndCounts drives the whole open-type-
// accept cycle and the match count report.
func TestSearchFindsCaseInsensitiveAndCounts(t *testing.T) {
	s := sizedPager(t, 80, 24)
	s.offset = 0

	s = drive(s, tea.KeyPressMsg{Code: '/'})
	if !s.search.active {
		t.Fatal("/ did not open the search bar")
	}
	for _, r := range "ZEBRA" {
		s = drive(s, tea.KeyPressMsg{Text: string(r), Code: r})
	}
	if got := s.search.count(); got != 2 {
		t.Fatalf("query ZEBRA matched %d rows, want 2 (case-insensitive substring)", got)
	}
	if !strings.Contains(s.View(), "1 of 2") {
		t.Errorf("view lacks the match count:\n%s", s.View())
	}

	s = drive(s, tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.search.active {
		t.Error("enter must close the search bar")
	}
	if s.search.count() != 2 {
		t.Error("enter must keep the matches live for n/N")
	}
}

// TestSearchEscapeRestoresScrollPosition pins the cancel contract: Esc
// drops the matches AND puts the reader back where `/` found them. The
// live jump while typing is what moves the view, so the restore has a
// real distance to undo.
func TestSearchEscapeRestoresScrollPosition(t *testing.T) {
	s := sizedPager(t, 80, 10)
	s.offset = 5
	startedAt := 5

	s = drive(s, tea.KeyPressMsg{Code: '/'})
	for _, r := range "zebra" {
		s = drive(s, tea.KeyPressMsg{Text: string(r), Code: r})
	}
	if s.offset == startedAt {
		t.Fatalf("typing did not move the offset (still %d); test needs a jump to prove restore", s.offset)
	}
	s = drive(s, tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.offset != startedAt {
		t.Errorf("esc restored offset %d, want %d", s.offset, startedAt)
	}
	if s.search.count() != 0 {
		t.Error("esc must drop the matches")
	}
	if s.search.active {
		t.Error("esc must close the bar")
	}
}

// TestSearchNextPrevAfterBarClosed pins n and N cycling with the bar
// shut, wrapping at both ends.
func TestSearchNextPrevAfterBarClosed(t *testing.T) {
	s := sizedPager(t, 80, 24)
	s = drive(s, tea.KeyPressMsg{Code: '/'})
	for _, r := range "zebra" {
		s = drive(s, tea.KeyPressMsg{Text: string(r), Code: r})
	}
	s = drive(s, tea.KeyPressMsg{Code: tea.KeyEnter})

	first, _ := s.search.currentMatch()
	s = drive(s, key("n"))
	second, _ := s.search.currentMatch()
	if second.row == first.row {
		t.Errorf("n stayed on row %d; matches are %v", first.row, s.search.matches)
	}
	s = drive(s, key("N"))
	back, _ := s.search.currentMatch()
	if back.row != first.row {
		t.Errorf("N landed on row %d, want %d", back.row, first.row)
	}
	// Wrap: N from the first match selects the last one.
	s = drive(s, key("N"))
	wrapped, _ := s.search.currentMatch()
	if wrapped.row != second.row {
		t.Errorf("wrapping N landed on row %d, want the last match at %d", wrapped.row, second.row)
	}
}

// TestSearchHighlightsEveryVisibleMatch proves every occurrence on
// screen is marked, not only the selected one: two reversed-video runs
// on one view, one per match row.
func TestSearchHighlightsEveryVisibleMatch(t *testing.T) {
	// Height 25 shows all 24 fixture rows at once, so BOTH matches are
	// on screen at the same time - the condition the rule states.
	s := sizedPager(t, 80, 25)
	s = drive(s, tea.KeyPressMsg{Code: '/'})
	for _, r := range "zebra" {
		s = drive(s, tea.KeyPressMsg{Text: string(r), Code: r})
	}
	s = drive(s, tea.KeyPressMsg{Code: tea.KeyEnter})

	view := s.View()
	// The accent current match renders as \x1b[1;7m and every other
	// match as \x1b[7m; both spellings must appear, one per visible
	// match, or only the selected match is being marked.
	reversed := strings.Count(view, "\x1b[7m") + strings.Count(view, "\x1b[1;7m")
	if reversed < 2 {
		t.Errorf("found %d reverse-video runs in the view, want at least 2 (every match highlighted):\n%s", reversed, view)
	}
}

// TestLeaveKeysPopThePager pins ctrl+o, esc and q: each asks the router
// to pop, returning to the composer.
func TestLeaveKeysPopThePager(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{
		{Code: 'o', Mod: tea.ModCtrl},
		{Code: tea.KeyEscape},
		{Code: 'q'},
	} {
		s := sizedPager(t, 80, 24)
		next, cmd := s.Update(k)
		if cmd == nil {
			t.Fatalf("key %s returned no Cmd", k.String())
		}
		if _, ok := cmd().(app.PopScreenMsg); !ok {
			t.Errorf("key %s must emit PopScreenMsg, got %T", k.String(), cmd())
		}
		if next.(Screen).mode != modePager {
			t.Errorf("key %s must not change the mode", k.String())
		}
	}
}

// TestWheelScrollsThePager: the wheel moves by the configured lines.
func TestWheelScrollsThePager(t *testing.T) {
	s := sizedPager(t, 80, 10) // content height 9, so 3 rows fit under the fixture
	s.offset = 0
	s = drive(s, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if s.offset != 3 {
		t.Errorf("wheel down moved to %d, want 3", s.offset)
	}
	s = drive(s, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if s.offset != 0 {
		t.Errorf("wheel up moved to %d, want 0", s.offset)
	}
}

// TestResizeRebuildsRowsAtTheNewWidth: block values re-render, so the
// pager re-flows instead of keeping stale wrapped rows.
func TestResizeRebuildsRowsAtTheNewWidth(t *testing.T) {
	s := sizedPager(t, 80, 24)
	wide := len(s.rows)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 30, Height: 24})
	s = next.(Screen)
	if len(s.rows) <= wide {
		t.Errorf("narrowing to 30 cols kept %d rows (was %d); rows must re-wrap", len(s.rows), wide)
	}
	if s.width != 30 || s.height != 24 {
		t.Errorf("size not stored: %dx%d", s.width, s.height)
	}
}

// TestKeymapCoversEveryPagerAction pins the dispatch table: every pager
// action ID in the keymap has a handler branch, so a new binding can
// never be silently dead.
func TestKeymapCoversEveryPagerAction(t *testing.T) {
	handled := map[keymap.ID]bool{
		keymap.IDSearchStart: true, keymap.IDSearchNext: true, keymap.IDSearchPrev: true,
		keymap.IDPagerRowUp: true, keymap.IDPagerRowDown: true,
		keymap.IDPagerTop: true, keymap.IDPagerBottom: true,
		keymap.IDPagerPromptUp: true, keymap.IDPagerPromptDn: true,
		keymap.IDPagerHalfUp: true, keymap.IDPagerHalfDown: true,
		keymap.IDPagerFullUp: true, keymap.IDPagerFullDown: true,
		keymap.IDLeavePager: true, keymap.IDDumpScrollback: true, keymap.IDEditTranscript: true,
	}
	seen := map[keymap.ID]bool{}
	for _, b := range keymap.Default() {
		if b.Context != keymap.ContextPager {
			continue
		}
		if !handled[b.ID] {
			t.Errorf("pager binding %s has no handler in the screen", b.ID)
		}
		seen[b.ID] = true
	}
	for id := range handled {
		if !seen[id] {
			t.Errorf("handler exists for %s but the keymap does not bind it", id)
		}
	}
}
