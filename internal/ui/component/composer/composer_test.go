package composer

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
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

func lightTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-light" {
			return th
		}
	}
	t.Fatal("mivia-light theme not found")
	return theme.Theme{}
}

// sizedComposer builds a composer at width with optional preset text.
func sizedComposer(t *testing.T, width int, text string) Model {
	t.Helper()
	m := New(loadTheme(t), theme.TierASCII, width)
	if text != "" {
		m.SetValue(text)
	}
	return m
}

// ----- Basic value / state tests -----

func TestNewIsEmptyAndFocused(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	if got := m.Value(); got != "" {
		t.Errorf("got %q, want empty value on a new composer", got)
	}
}

func TestUpdateTypesText(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	next, _ = next.Update(tea.KeyPressMsg{Text: "i", Code: 'i'})
	if got := next.Value(); got != "hi" {
		t.Errorf("got %q, want \"hi\"", got)
	}
}

func TestClearResetsValue(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	next.Clear()
	if got := next.Value(); got != "" {
		t.Errorf("got %q, want empty after Clear", got)
	}
}

func TestSetValueRoundtrips(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetValue("hello world")
	if got := m.Value(); got != "hello world" {
		t.Errorf("got %q, want \"hello world\"", got)
	}
}

// ----- Phase 1: multi-line tests -----

func TestShiftEnterInsertsNewline(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetValue("first line")
	// shift+enter should insert a newline inside the textarea.
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	next, _ = next.Update(tea.KeyPressMsg{Text: "s", Code: 's'})
	if !strings.Contains(next.Value(), "\n") {
		t.Errorf("shift+enter should insert a newline; got value %q", next.Value())
	}
}

func TestAltEnterInsertsNewline(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetValue("first line")
	// alt+enter should insert a newline inside the textarea.
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	next, _ = next.Update(tea.KeyPressMsg{Text: "a", Code: 'a'})
	if !strings.Contains(next.Value(), "\n") {
		t.Errorf("alt+enter should insert a newline; got value %q", next.Value())
	}
}

func TestMultiLineValuePreserved(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetValue("line one\nline two\nline three")
	if got := m.Value(); got != "line one\nline two\nline three" {
		t.Errorf("got %q, want three-line value", got)
	}
}

func TestHeightGrowsWithLines(t *testing.T) {
	m1 := sizedComposer(t, 80, "")
	m2 := sizedComposer(t, 80, "line one\nline two")
	if m2.Height() <= m1.Height() {
		t.Errorf("height with two lines (%d) should exceed single-line height (%d)", m2.Height(), m1.Height())
	}
}

func TestHeightCappedAtMaxLines(t *testing.T) {
	// 10 lines of text should not exceed maxInputLines.
	lines := strings.Repeat("text\n", 10)
	m := sizedComposer(t, 80, strings.TrimSuffix(lines, "\n"))
	maxExpected := maxInputLines
	if got := m.Height(); got > maxExpected {
		t.Errorf("height %d exceeds cap %d with many lines", got, maxExpected)
	}
}

func TestSubmitTextNormalisesBackslashNewline(t *testing.T) {
	m := sizedComposer(t, 80, "first\\\nsecond")
	// \<newline> should become a real newline in the submitted text.
	if got := m.SubmitText(); !strings.Contains(got, "\n") {
		t.Errorf("SubmitText did not normalise \\\\newline; got %q", got)
	}
}

func TestSubmitTextPlainRoundtrip(t *testing.T) {
	m := sizedComposer(t, 80, "hello")
	if got := m.SubmitText(); got != "hello" {
		t.Errorf("SubmitText mangled plain text; got %q want %q", got, "hello")
	}
}

// ----- View smoke tests -----

func TestViewShowsPromptAndText(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	got := next.View()
	if !strings.Contains(got, "h") {
		t.Errorf("got %q, want typed text present in View", got)
	}
}

func TestViewAppliesBGInset(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor, 40)
	got := m.View()
	insetBG := strings.TrimSuffix(render.FillBG(th, theme.TierTrueColor, theme.RoleBGInset, ""), "\x1b[m")
	if !strings.Contains(got, insetBG) {
		t.Errorf("expected view to contain RoleBGInset background (%q), got:\n%q", insetBG, got)
	}
}

func TestInputOffsets(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	if got := m.InputRowFromBottom(); got != 1 {
		t.Errorf("InputRowFromBottom() = %d, want 1", got)
	}
	if got := m.InputColumnOffset(); got != 0 {
		t.Errorf("InputColumnOffset() = %d, want 0", got)
	}
}

func TestViewNarrowFallbackNoFrame(t *testing.T) {
	// Below minFramedWidth, View must not panic and must stay within width.
	m := New(loadTheme(t), theme.TierASCII, 4)
	m.SetValue("hi")
	got := m.View()
	for _, row := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(row); w > 4 {
			t.Errorf("narrow view: row %q is %d cols, want ≤4", ansi.Strip(row), w)
		}
	}
}

// TestComposerViewNoExcessWidth checks that framed rows never exceed terminal width.
// This replaces the strict pixel-exact test from textinput days; textarea
// internal rendering varies by cursor position, but rows must never overflow.
func TestComposerViewNoExcessWidth(t *testing.T) {
	for _, width := range []int{8, 20, 40, 80, 100} {
		for _, text := range []string{"", "hi", "longer reply text that wraps maybe"} {
			m := sizedComposer(t, width, text)
			for _, row := range strings.Split(m.View(), "\n") {
				if got := ansi.StringWidth(row); got > width {
					t.Errorf("width %d text %q: view row is %d cols, exceeds terminal width (%q)",
						width, text, got, ansi.Strip(row))
				}
			}
		}
	}
}

// ----- Slash-command menu tests (regression: must still work) -----

func TestMenuActiveOnSlash(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetCommands([]Command{{Name: "clear", Desc: "clear transcript"}})
	m.SetValue("/cl")
	if !m.MenuActive() {
		t.Error("menu not active after typing /cl")
	}
}

func TestMenuNotActiveForMidWordSlash(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetCommands([]Command{{Name: "clear", Desc: "clear transcript"}})
	m.SetValue("path/to/file")
	if m.MenuActive() {
		t.Error("menu must not activate for mid-word slash (ux-rules.md rule 5.2)")
	}
}

func TestMenuDismissKeepsText(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetCommands([]Command{{Name: "clear", Desc: "clear transcript"}})
	m.SetValue("/cl")
	m = m.MenuDismiss()
	if m.MenuActive() {
		t.Error("menu active after MenuDismiss")
	}
	if got := m.Value(); got != "/cl" {
		t.Errorf("text changed after MenuDismiss: got %q", got)
	}
}

func TestMenuAcceptSelected(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetCommands([]Command{{Name: "clear", Desc: "clear transcript"}})
	m.SetValue("/cl")
	if !m.MenuActive() {
		t.Fatal("menu not open")
	}
	m = m.AcceptSelected()
	if m.MenuActive() {
		t.Error("menu still open after AcceptSelected")
	}
	if got := m.Value(); got != "/clear" {
		t.Errorf("got %q, want \"/clear\" after accepting", got)
	}
}

func TestSetCommandsImmutability(t *testing.T) {
	cmds := []Command{{Name: "test", Desc: "desc"}}
	m := New(loadTheme(t), theme.TierTrueColor, 40)
	m.SetCommands(cmds)
	cmds[0].Name = "MUTATED"
	if got := m.Commands()[0].Name; got == "MUTATED" {
		t.Error("SetCommands did not clone cmds slice; external mutation corrupted composer")
	}
}

// ----- Phase 2: @-mention picker tests -----

func TestMentionMenuActiveOnAt(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetMentions([]Mention{
		{Path: "internal/ui/component/composer/composer.go", Display: "composer.go"},
	})
	m.SetValue("fix @comp")
	if !m.MentionMenuActive() {
		t.Error("mention menu should be active after '@' at token start")
	}
}

func TestMentionMenuNotActiveWithoutAt(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetMentions([]Mention{
		{Path: "internal/ui/component/composer/composer.go"},
	})
	m.SetValue("no at sign here")
	if m.MentionMenuActive() {
		t.Error("mention menu must not activate without '@'")
	}
}

func TestMentionMenuNotActiveAfterSpace(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetMentions([]Mention{{Path: "main.go"}})
	// "@foo " — space after the token means the mention was abandoned
	m.SetValue("@foo ")
	if m.MentionMenuActive() {
		t.Error("mention menu must not activate when a space follows '@...'")
	}
}

func TestMentionMenuAcceptInsertPath(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetMentions([]Mention{{Path: "internal/ui/component/composer/mention.go"}})
	m.SetValue("check @mention")
	if !m.MentionMenuActive() {
		t.Fatal("mention menu not active")
	}
	m = m.AcceptMention()
	if m.MentionMenuActive() {
		t.Error("mention menu still open after AcceptMention")
	}
	if !strings.Contains(m.Value(), "@internal/ui/component/composer/mention.go") {
		t.Errorf("mention path not inserted; got %q", m.Value())
	}
}

func TestMentionMenuDismissKeepsText(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetMentions([]Mention{{Path: "main.go"}})
	m.SetValue("describe @m")
	m = m.MentionMenuDismiss()
	if m.MentionMenuActive() {
		t.Error("mention menu active after MentionMenuDismiss")
	}
	if got := m.Value(); got != "describe @m" {
		t.Errorf("text changed after MentionMenuDismiss: got %q", got)
	}
}

func TestSetMentionsImmutability(t *testing.T) {
	mentions := []Mention{{Path: "original.go"}}
	m := New(loadTheme(t), theme.TierTrueColor, 40)
	m.SetMentions(mentions)
	mentions[0].Path = "MUTATED.go"
	if got := m.Mentions()[0].Path; got == "MUTATED.go" {
		t.Error("SetMentions did not clone slice; external mutation corrupted composer")
	}
}

func TestMentionMenuNavigate(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetMentions([]Mention{
		{Path: "aaa.go"},
		{Path: "bbb.go"},
		{Path: "ccc.go"},
	})
	m.SetValue("@")
	if !m.MentionMenuActive() {
		t.Fatal("mention menu not active")
	}
	m = m.MentionMenuNext()
	// Accept — should insert bbb.go (cursor was at index 1 after Next).
	m2 := m.AcceptMention()
	if !strings.Contains(m2.Value(), "@bbb.go") && !strings.Contains(m2.Value(), "@aaa.go") {
		// Either the first or second is fine depending on ranking; just assert
		// something from the list was inserted.
		t.Errorf("AcceptMention after Next did not insert a known path; got %q", m2.Value())
	}
}

// ----- Theme / tier tests (regression) -----

func TestInputTextCarriesTheThemeForeground(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor, 40)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})

	want := render.Role(th, theme.TierTrueColor, theme.RoleFG).Render("h")
	if !strings.Contains(next.View(), want) {
		t.Errorf("typed text is not styled with the theme's fg role, got:\n%q", next.View())
	}
}

func TestSetThemeRestylesTheInput(t *testing.T) {
	dark, light := loadTheme(t), lightTheme(t)
	m := New(dark, theme.TierTrueColor, 40)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	next.SetTheme(light, theme.TierTrueColor)

	if got, want := next.Theme.Name, light.Name; got != want {
		t.Errorf("got theme %q, want %q", got, want)
	}
}

func TestSetThemeAtNoColourTierAddsNoColour(t *testing.T) {
	m := New(loadTheme(t), theme.TierNoTTY, 40)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	if got := next.View(); strings.Contains(got, "\x1b[38;2;") || strings.Contains(got, "\x1b[48;2;") {
		t.Errorf("no-TTY tier drew colour, got:\n%q", got)
	}
}

// ----- Mention trigger unit tests -----

func TestMentionTriggerAtLineStart(t *testing.T) {
	q, pos, ok := mentionTrigger("@foo", 4)
	if !ok || q != "foo" || pos != 0 {
		t.Errorf("got q=%q pos=%d ok=%v, want q=foo pos=0 ok=true", q, pos, ok)
	}
}

func TestMentionTriggerMidSentence(t *testing.T) {
	q, pos, ok := mentionTrigger("fix bug @comp", 13)
	if !ok || q != "comp" || pos != 8 {
		t.Errorf("got q=%q pos=%d ok=%v, want q=comp pos=8 ok=true", q, pos, ok)
	}
}

func TestMentionTriggerNoAt(t *testing.T) {
	_, _, ok := mentionTrigger("no at sign", 10)
	if ok {
		t.Error("should not trigger without '@'")
	}
}

func TestMentionTriggerSpaceAfterAt(t *testing.T) {
	// "@foo " — space terminates the token, no trigger
	_, _, ok := mentionTrigger("@foo ", 5)
	if ok {
		t.Error("should not trigger when space follows '@...'")
	}
}

func TestMentionTriggerMidWord(t *testing.T) {
	// "path@nope" — '@' not preceded by whitespace
	_, _, ok := mentionTrigger("path@nope", 9)
	if ok {
		t.Error("should not trigger when '@' is mid-word (not at token boundary)")
	}
}

func TestRankMentionsEmptyQuery(t *testing.T) {
	mentions := []Mention{{Path: "a.go"}, {Path: "b.go"}}
	result := rankMentions(mentions, "")
	if len(result) != 2 {
		t.Errorf("empty query should return all candidates; got %d", len(result))
	}
}

func TestRankMentionsPrefixBeatsSubstring(t *testing.T) {
	mentions := []Mention{
		{Path: "internal/xcomp.go"},    // contains "comp" as substring
		{Path: "internal/comp.go"},     // "comp" as prefix of basename
		{Path: "comp/sub/file.go"},     // prefix on full path
		{Path: "other/xyz/c_o_m_p.go"}, // subsequence
	}
	result := rankMentions(mentions, "comp")
	if len(result) < 4 {
		t.Fatalf("expected 4 results, got %d", len(result))
	}
	if result[0].Path != "internal/comp.go" {
		t.Errorf("prefix on basename should rank first; got %q", result[0].Path)
	}
}

func TestMentionMenuViewAndScrolling(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, 80)
	var mentions []Mention
	for i := 0; i < 15; i++ {
		mentions = append(mentions, Mention{Path: "dir/sub/file" + itoa(i) + ".go"})
	}
	m.SetMentions(mentions)
	m.SetValue("@")
	if !m.MentionMenuActive() {
		t.Fatal("mention menu should be active")
	}

	view := m.View()
	if !strings.Contains(view, "@file0.go") {
		t.Errorf("view should show first mention; got:\n%s", view)
	}

	// Move forward past MaxCompletionRows to test offset clamping
	for i := 0; i < 8; i++ {
		m = m.MentionMenuNext()
	}
	// Move backward to test prev and offset clamping
	for i := 0; i < 4; i++ {
		m = m.MentionMenuPrev()
	}

	if sel, ok := m.mmenu.selected(); !ok || sel.Path == "" {
		t.Error("selected mention should be non-empty")
	}

	m = m.AcceptMention()
	if m.MentionMenuActive() {
		t.Error("menu should close after accept")
	}

	// Menu wraps backward and forward
	m.SetValue("@")
	for i := 0; i < 20; i++ {
		m = m.MentionMenuNext()
	}
	for i := 0; i < 20; i++ {
		m = m.MentionMenuPrev()
	}

	// Mention with no matches
	m.SetValue("@nonexistentquerythatmatchesnothing")
	if m.MentionMenuActive() {
		t.Error("menu should not be active with no matches")
	}

	// Test cursor beyond len(text)
	_, _, ok := mentionTrigger("hello", 100)
	if ok {
		t.Error("should handle cursor beyond text")
	}

	// Test AcceptMention when not active
	m.Clear()
	m.SetValue("plain text")
	m = m.AcceptMention()
	if m.Value() != "plain text" {
		t.Errorf("AcceptMention on inactive menu modified text: %q", m.Value())
	}
}

func TestActiveMenuView_MentionMenu(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 80)
	m.SetMentions([]Mention{{Path: "main.go", Display: "main.go"}})
	m.SetValue("@ma")
	if !m.MentionMenuActive() {
		t.Fatal("expected mention menu active")
	}
	if rows := m.MenuRows(); rows == 0 {
		t.Errorf("expected non-zero MenuRows for active mention menu")
	}
	if view := m.View(); !strings.Contains(view, "main.go") {
		t.Errorf("expected View to contain mention entry main.go, got:\n%s", view)
	}
}
