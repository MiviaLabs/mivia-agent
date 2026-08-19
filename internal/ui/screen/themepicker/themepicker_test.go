package themepicker

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func loadThemes(t *testing.T) []theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(themes) < 2 {
		t.Fatalf("need at least 2 embedded themes for these tests, got %d", len(themes))
	}
	return themes
}

func TestNewSatisfiesAppScreen(t *testing.T) {
	var _ app.Screen = New(theme.Theme{}, theme.TierASCII, nil)
}

func TestInitIsNil(t *testing.T) {
	s := New(theme.Theme{}, theme.TierASCII, nil)
	if cmd := s.Init(); cmd != nil {
		t.Error("expected a nil Init Cmd")
	}
}

func TestViewListsThemeNames(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierASCII, themes)
	got := s.View()
	for _, th := range themes {
		if !strings.Contains(got, th.Name) {
			t.Errorf("picker view missing theme %q:\n%s", th.Name, got)
		}
	}
	if !strings.Contains(got, "select a theme") || !strings.Contains(got, "[esc] cancel") {
		t.Errorf("picker view missing title/hint: %q", got)
	}
}

// TestPreviewFollowsTheHighlightedThemeNotTheAppliedOne pins the
// package doc comment's own promise ("live-previews and selects an
// app-wide theme"): moving the cursor must change the preview's
// colours to the highlighted theme's, before Enter ever applies
// anything. s.Theme (the still-active app theme) must not change.
func TestPreviewFollowsTheHighlightedThemeNotTheAppliedOne(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierTrueColor, themes)

	before := s.View()
	wantBefore := render.Role(themes[0], theme.TierTrueColor, theme.RoleAccent).Render(previewAccentGlyph)
	if !strings.Contains(before, wantBefore) {
		t.Fatalf("preview does not start styled with the first (highlighted) theme's accent colour:\n%s", before)
	}

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	after := s.View()

	if s.Theme.Name != themes[0].Name {
		t.Errorf("got applied theme %q, want it unchanged until Enter confirms", s.Theme.Name)
	}
	wantAfter := render.Role(themes[1], theme.TierTrueColor, theme.RoleAccent).Render(previewAccentGlyph)
	if !strings.Contains(after, wantAfter) {
		t.Errorf("preview did not follow the cursor to theme %q:\n%s", themes[1].Name, after)
	}
	if strings.Contains(after, wantBefore) {
		t.Errorf("preview still shows the previous theme %q's accent colour after moving down:\n%s", themes[0].Name, after)
	}
}

// TestPreviewShowsADiffInTheHighlightedTheme pins the mock
// (docs/design/mivia-ui-mock.html section 7): the preview is not just
// prose, it shows a diff hunk so an add/del-heavy theme choice (the
// pair most likely to fail contrast or CVD checks) is judged on the
// content that actually stresses those roles, not on a sentence that
// never exercises them.
func TestPreviewShowsADiffInTheHighlightedTheme(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierTrueColor, themes)

	got := s.View()
	wantAdd := render.WithBg(
		render.Role(themes[0], theme.TierTrueColor, theme.RoleDiffAddFG),
		themes[0], theme.TierTrueColor, theme.RoleDiffAddBG,
	).Render("+ " + previewDiffAddLine)
	if !strings.Contains(got, wantAdd) {
		t.Errorf("preview missing a diff add line styled with %q's diff-add roles:\n%s", themes[0].Name, got)
	}
	wantDel := render.WithBg(
		render.Role(themes[0], theme.TierTrueColor, theme.RoleDiffDelFG),
		themes[0], theme.TierTrueColor, theme.RoleDiffDelBG,
	).Render("- " + previewDiffDelLine)
	if !strings.Contains(got, wantDel) {
		t.Errorf("preview missing a diff del line styled with %q's diff-del roles:\n%s", themes[0].Name, got)
	}

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	after := s.View()
	wantAddAfter := render.WithBg(
		render.Role(themes[1], theme.TierTrueColor, theme.RoleDiffAddFG),
		themes[1], theme.TierTrueColor, theme.RoleDiffAddBG,
	).Render("+ " + previewDiffAddLine)
	if !strings.Contains(after, wantAddAfter) {
		t.Errorf("diff add line did not follow the cursor to theme %q:\n%s", themes[1].Name, after)
	}
	if strings.Contains(after, wantAdd) {
		t.Errorf("diff add line still shows theme %q's colours after moving down:\n%s", themes[0].Name, after)
	}
}

// TestPreviewShowsACodeReadWithSyntaxRoles pins the other half of the
// mock: a function-with-params read, syntax-highlighted with the
// theme's own keyword/function/type roles - the picker's syntax roles
// (RoleKeyword, RoleFunction, RoleType) had no renderer anywhere in
// this codebase before this, only contrast-checker test data.
func TestPreviewShowsACodeReadWithSyntaxRoles(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierTrueColor, themes)

	got := s.View()
	wantKeyword := render.Role(themes[0], theme.TierTrueColor, theme.RoleKeyword).Render("func")
	if !strings.Contains(got, wantKeyword) {
		t.Errorf("preview missing the code read's %q keyword styled with RoleKeyword:\n%s", "func", got)
	}
	wantFunc := render.Role(themes[0], theme.TierTrueColor, theme.RoleFunction).Render(previewFuncName)
	if !strings.Contains(got, wantFunc) {
		t.Errorf("preview missing the function name %q styled with RoleFunction:\n%s", previewFuncName, got)
	}
	wantType := render.Role(themes[0], theme.TierTrueColor, theme.RoleType).Render(previewParamType)
	if !strings.Contains(got, wantType) {
		t.Errorf("preview missing the param type %q styled with RoleType:\n%s", previewParamType, got)
	}
}

func TestThemeChangedMsgUpdatesScreenAndPicker(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierASCII, themes)
	next, cmd := s.Update(app.ThemeChangedMsg{Theme: themes[1], Tier: theme.TierTrueColor})
	if cmd != nil {
		t.Error("expected no Cmd from a theme change")
	}
	got := next.(Screen)
	if got.Theme.Name != themes[1].Name || got.Tier != theme.TierTrueColor {
		t.Errorf("got Theme=%q Tier=%v, want Theme=%q Tier=TierTrueColor", got.Theme.Name, got.Tier, themes[1].Name)
	}
	if got.picker.Theme.Name != themes[1].Name {
		t.Errorf("got picker.Theme=%q, want %q", got.picker.Theme.Name, themes[1].Name)
	}
}

func TestEnterEmitsThemeSelectedMsg(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierASCII, themes)
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a Cmd on enter")
	}
	msg, ok := cmd().(app.ThemeSelectedMsg)
	if !ok {
		t.Fatalf("got %T, want app.ThemeSelectedMsg", cmd())
	}
	if msg.Name != themes[0].Name {
		t.Errorf("got %q, want the first (selected) theme %q", msg.Name, themes[0].Name)
	}
	if _, ok := next.(Screen); !ok {
		t.Errorf("got %T, want Update to return a themepicker.Screen", next)
	}
}

func TestEscEmitsPopScreenMsg(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierASCII, themes)
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected a Cmd on esc")
	}
	if _, ok := cmd().(app.PopScreenMsg); !ok {
		t.Errorf("got %T, want app.PopScreenMsg", cmd())
	}
}

func TestNavigationKeyProducesNoCmd(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierASCII, themes)
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if cmd != nil {
		t.Error("expected no Cmd from a plain cursor movement")
	}
	got := next.View()
	if !strings.Contains(got, themes[1].Name) {
		t.Errorf("expected the cursor move reflected in the view: %q", got)
	}
}

// TestViewFlagsHoldsAltScreen pins the modal's surface contract with the
// router: a theme preview is a cockpit modal.
func TestViewFlagsHoldsAltScreen(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierASCII, themes)
	if !s.ViewFlags().AltScreen {
		t.Error("the theme picker must hold the alternate screen")
	}
}

// TestResizeRecentersTheDialog covers the sizing path: a WindowSizeMsg
// must reach the screen, and the dialog must fill exactly that frame.
func TestResizeRecentersTheDialog(t *testing.T) {
	s := New(loadThemes(t)[0], theme.TierASCII, loadThemes(t))
	next, _ := s.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	scr := next.(Screen)
	rows := strings.Split(scr.View(), "\n")
	if len(rows) != 20 {
		t.Fatalf("view is %d rows, want 20", len(rows))
	}
	if !strings.Contains(ansi.Strip(rows[0]), "╭") && !strings.Contains(ansi.Strip(rows[2]), "╭") {
		// The box is centered, so its top border sits near the middle,
		// not necessarily row 0; assert it exists somewhere.
		found := false
		for _, r := range rows {
			if strings.Contains(ansi.Strip(r), "╭") {
				found = true
			}
		}
		if !found {
			t.Errorf("no dialog frame in the sized view:\n%s", scr.View())
		}
	}
}

// TestPreviewDrawsOnTheDialogSurface is the "dark rectangles" regression.
// The preview's prompt line, code read, and diff are each a chain of
// styled runs, and every run ends in an SGR reset. Before the fix only
// the first run of a row carried the dialog's inset background, so every
// later run drew a box of the terminal's own colour behind its text -
// black rectangles inside a white mivia-light dialog.
func TestPreviewDrawsOnTheDialogSurface(t *testing.T) {
	themes := loadThemes(t)
	var light theme.Theme
	for _, th := range themes {
		if th.Name == "mivia-light" {
			light = th
		}
	}
	if light.Name == "" {
		t.Fatal("need mivia-light embedded")
	}

	s := New(light, theme.TierTrueColor, themes)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := next.View()

	// Every sample the preview draws must land on the dialog's surface.
	for _, sample := range []string{previewSample, previewFuncName, previewDiffAddLine} {
		row, ok := rowWith(view, sample)
		if !ok {
			t.Fatalf("preview row for %q not rendered:\n%s", sample, view)
		}
		if n := unpaintedCells(dialogInterior(row)); n > 0 {
			t.Errorf("%q draws %d cell(s) on the terminal's own background: %q", sample, n, row)
		}
	}
}

// rowWith finds the rendered row whose visible text contains want.
func rowWith(view, want string) (string, bool) {
	for _, row := range strings.Split(view, "\n") {
		if strings.Contains(ansi.Strip(row), want) {
			return row, true
		}
	}
	return "", false
}

// dialogInterior is the part of a dialog row between its border glyphs -
// the region the inset background covers. A row with no border (the
// whitespace around the box) has no interior and contributes nothing.
func dialogInterior(row string) string {
	first := strings.Index(row, "│")
	last := strings.LastIndex(row, "│")
	if first < 0 || last <= first {
		return ""
	}
	return row[first+len("│") : last]
}

// unpaintedCells counts the printable cells drawn while no background is
// set, so the terminal's own colour shows through. It replays the row's
// SGR state instead of pattern-matching escapes, so a reset that only
// precedes more escapes (or ends the row) correctly counts for nothing.
func unpaintedCells(s string) int {
	n, painted := 0, false
	for i := 0; i < len(s); {
		if seq, ok := sgrAt(s, i); ok {
			painted = sgrSetsBackground(seq, painted)
			i += len(seq)
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if !painted {
			n++
		}
		i += size
	}
	return n
}

func sgrAt(s string, i int) (string, bool) {
	if !strings.HasPrefix(s[i:], "\x1b[") {
		return "", false
	}
	end := strings.IndexByte(s[i:], 'm')
	if end < 0 {
		return "", false
	}
	return s[i : i+end+1], true
}

func sgrSetsBackground(seq string, painted bool) bool {
	params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"), ";")
	for i := 0; i < len(params); i++ {
		switch p := params[i]; {
		case p == "" || p == "0":
			painted = false
		case p == "48":
			return true // the rest of this sequence is the colour's arguments
		case p == "49":
			painted = false
		case len(p) == 2 && p[0] == '4' && p[1] >= '0' && p[1] <= '7':
			painted = true
		case len(p) == 3 && p[0] == '1' && p[1] == '0' && p[2] >= '0' && p[2] <= '7':
			painted = true
		}
	}
	return painted
}

// TestNewHighlightsTheActiveTheme: the picker opens on the theme that is
// already in use, not on whichever name sorts first. Opening it is a
// request to change from HERE, so the current choice must be the one
// under the cursor - and because the modal renders in the highlighted
// row's theme, a cursor parked elsewhere also opens the whole dialog in
// a theme the user did not choose.
func TestNewHighlightsTheActiveTheme(t *testing.T) {
	themes := loadThemes(t)
	for _, want := range themes {
		s := New(want, theme.TierTrueColor, themes)
		got, ok := s.picker.Selected()
		if !ok {
			t.Fatalf("theme %q: picker highlights nothing", want.Name)
		}
		if got != want.Name {
			t.Errorf("opened on %q, want the active theme %q", got, want.Name)
		}
	}
}

// TestViewOpensInTheActiveTheme is the same fact seen from the frame the
// user gets: the dialog's own chrome must be the active theme's on the
// very first render.
func TestViewOpensInTheActiveTheme(t *testing.T) {
	themes := loadThemes(t)
	var active, other theme.Theme
	for _, th := range themes {
		switch th.Name {
		case "mivia-light":
			active = th
		case "mivia-dark":
			other = th
		}
	}
	if active.Name == "" || other.Name == "" {
		t.Fatal("need both mivia-light and mivia-dark embedded")
	}

	s := New(active, theme.TierTrueColor, themes)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := next.View()

	want := render.Role(active, theme.TierTrueColor, theme.RoleAccent).Render(previewAccentGlyph)
	if !strings.Contains(view, want) {
		t.Errorf("the picker did not open in the active theme %q:\n%s", active.Name, view)
	}
	stale := render.Role(other, theme.TierTrueColor, theme.RoleAccent).Render(previewAccentGlyph)
	if stale != want && strings.Contains(view, stale) {
		t.Errorf("the picker opened showing %q's chrome:\n%s", other.Name, view)
	}
}

// TestNewWithAnUnlistedThemeHighlightsTheFirstRow: a theme that is not
// among the candidates (a name the list no longer carries) leaves the
// cursor on the first row rather than nowhere.
func TestNewWithAnUnlistedThemeHighlightsTheFirstRow(t *testing.T) {
	themes := loadThemes(t)
	s := New(theme.Theme{Name: "not-embedded"}, theme.TierTrueColor, themes)
	got, ok := s.picker.Selected()
	if !ok {
		t.Fatal("picker highlights nothing")
	}
	if got != themes[0].Name {
		t.Errorf("got %q, want the first row %q", got, themes[0].Name)
	}
}
