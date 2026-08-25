package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
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

func newScreen(t *testing.T, width, height int) Screen {
	t.Helper()
	th := loadTheme(t)
	tb := topbar.New(th, theme.TierTrueColor, ports.ModelInfo{Name: "mivia-fast"}, ports.Usage{}, width)
	s := New(th, theme.TierTrueColor, tb, ports.Settings{}, 0)
	next, _ := s.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return next.(Screen)
}

func TestNewSatisfiesAppScreen(t *testing.T) {
	var _ app.Screen = Screen{}
}

func TestViewFlagsHoldsAltScreen(t *testing.T) {
	s := newScreen(t, 100, 30)
	if !s.ViewFlags().AltScreen {
		t.Error("expected the settings screen to hold the alternate screen")
	}
}

// TestViewEmitsExactlyHeightRowsNoneOverWidth is the frame's contract
// (settings-screen.md §2): every View() call returns exactly height
// rows, and no row exceeds width, across a size sweep.
func TestViewEmitsExactlyHeightRowsNoneOverWidth(t *testing.T) {
	for _, dims := range [][2]int{{80, 24}, {100, 30}, {120, 36}, {40, 12}, {200, 50}} {
		width, height := dims[0], dims[1]
		s := newScreen(t, width, height)
		rows := strings.Split(s.View(), "\n")
		if len(rows) != height {
			t.Errorf("%dx%d: got %d rows, want %d", width, height, len(rows), height)
		}
		for i, r := range rows {
			if w := ansi.StringWidth(r); w > width {
				t.Errorf("%dx%d: row %d is %d columns wide", width, height, i, w)
			}
		}
	}
}

// TestNavListsEveryTitle proves all five sections are present and the
// active one is marked, without depending on the marker glyph.
func TestNavListsEveryTitle(t *testing.T) {
	s := newScreen(t, 100, 30)
	plain := ansi.Strip(s.View())
	for _, want := range []string{"General", "Projects", "Automations", "Agents", "Skills", "Models", "MCP"} {
		if !strings.Contains(plain, want) {
			t.Errorf("nav is missing %q:\n%s", want, plain)
		}
	}
}

// TestArrowsMoveNavAndUpdateBreadcrumb: up/down while the nav pane has
// focus moves the highlighted section and the top bar's breadcrumb
// follows it - the same live-preview convention the theme picker uses.
func TestArrowsMoveNavAndUpdateBreadcrumb(t *testing.T) {
	s := newScreen(t, 100, 30)
	if got := s.sections[s.nav].Title(); got != "General" {
		t.Fatalf("got initial section %q, want General", got)
	}
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	if got := s.sections[s.nav].Title(); got != "Projects" {
		t.Errorf("got %q after down, want Projects", got)
	}
	if !strings.Contains(ansi.Strip(s.top.View()), "Projects") {
		t.Errorf("breadcrumb did not follow the nav move:\n%s", ansi.Strip(s.top.View()))
	}
}

// TestNavClampsRatherThanWraps: repeated presses past either end hold
// at the boundary - a wrap would make the key look like it did nothing
// on the press that actually mattered.
func TestNavClampsRatherThanWraps(t *testing.T) {
	s := newScreen(t, 100, 30)
	for i := 0; i < 10; i++ {
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyUp})
		s = next.(Screen)
	}
	if s.nav != 0 {
		t.Errorf("nav = %d, want clamped to 0", s.nav)
	}
	for i := 0; i < 10; i++ {
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	if s.nav != len(s.sections)-1 {
		t.Errorf("nav = %d, want clamped to %d", s.nav, len(s.sections)-1)
	}
}

// TestEscFocusesNavBeforePoppingTheScreen: esc backs out of the detail
// pane first, and only pops the screen once the nav pane already has
// focus - so a user editing a section's detail cannot lose the whole
// screen on one stray esc.
func TestEscFocusesNavBeforePoppingTheScreen(t *testing.T) {
	s := newScreen(t, 100, 30)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	s = next.(Screen)
	if s.focus != render.Right {
		t.Fatal("expected right (h/l or arrow) to focus the detail pane")
	}
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	s = next.(Screen)
	if cmd != nil {
		t.Fatal("esc from the detail pane must not pop the screen yet")
	}
	next, cmd = s.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc from the nav pane must pop the screen")
	}
	if _, ok := cmd().(app.PopScreenMsg); !ok {
		t.Errorf("got %T, want app.PopScreenMsg", cmd())
	}
}

// TestHelpOverlayOpensAndAnyKeyClears mirrors conversation's own overlay
// convention: "?" draws the generated keymap, and any key dismisses it.
func TestHelpOverlayOpensAndAnyKeyClears(t *testing.T) {
	s := newScreen(t, 100, 30)
	next, _ := s.Update(tea.KeyPressMsg{Text: "?", Code: '?'})
	s = next.(Screen)
	if s.overlay == "" {
		t.Fatal("expected \"?\" to open the help overlay")
	}
	next, _ = s.Update(tea.KeyPressMsg{Text: "z", Code: 'z'})
	s = next.(Screen)
	if s.overlay != "" {
		t.Error("expected any key to clear the overlay")
	}
}

// TestUnavailableSectionsSayUnavailable: with no store wired (the zero
// ports.Settings, every field nil), every section must say so rather
// than panic or render nothing (settings-screen.md §4).
func TestUnavailableSectionsSayUnavailable(t *testing.T) {
	s := newScreen(t, 100, 30)
	for i := range s.sections {
		s.nav = i
		if got := ansi.Strip(s.sections[i].View()); !strings.Contains(got, "unavailable") {
			t.Errorf("section %d (%s) does not say unavailable: %q", i, s.sections[i].Title(), got)
		}
	}
}

func TestSectionIndexResolvesNamesCaseInsensitively(t *testing.T) {
	cases := map[string]int{
		"": 0, "general": 0, "General": 0, "projects": 1, "automations": 2,
		"agents": 3, "skills": 4, "MODELS": 5, "mcp": 6,
	}
	for name, want := range cases {
		got, ok := SectionIndex(name)
		if !ok || got != want {
			t.Errorf("SectionIndex(%q) = %d, %v; want %d, true", name, got, ok, want)
		}
	}
	if _, ok := SectionIndex("nope"); ok {
		t.Error("expected an unknown section name to resolve false")
	}
}

func TestNewClampsAnOutOfRangeInitialNav(t *testing.T) {
	th := loadTheme(t)
	tb := topbar.New(th, theme.TierTrueColor, ports.ModelInfo{}, ports.Usage{}, 80)
	s := New(th, theme.TierTrueColor, tb, ports.Settings{}, 99)
	if s.nav != len(s.sections)-1 {
		t.Errorf("nav = %d, want clamped to %d", s.nav, len(s.sections)-1)
	}
	s = New(th, theme.TierTrueColor, tb, ports.Settings{}, -1)
	if s.nav != 0 {
		t.Errorf("nav = %d, want clamped to 0", s.nav)
	}
}

func keySettingsMsg(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Text: s, Code: rune(s[0])} }

// TestSettingsOwnsQuit pins app.OwnsQuit's contract: the router forwards
// ctrl+c to this screen instead of quitting on the first press.
func TestSettingsOwnsQuit(t *testing.T) {
	s := newScreen(t, 100, 30)
	if !s.OwnsQuit() {
		t.Error("settings.Screen must report OwnsQuit() == true")
	}
}

// TestCtrlCArmsQuitAndShowsWarningInStatusRow mirrors
// conversation.Screen's own double-press quit guard (UX Rule 1.3): the
// first ctrl+c must not quit, and must show the warning in this
// screen's own bottom status row - not silently exit like a modal.
func TestCtrlCArmsQuitAndShowsWarningInStatusRow(t *testing.T) {
	s := newScreen(t, 100, 30)
	next, cmd := s.Update(keySettingsMsg("ctrl+c"))
	s = next.(Screen)
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("the first ctrl+c must not quit")
		}
	}
	if !s.quitArmed {
		t.Fatal("expected quitArmed after the first ctrl+c")
	}
	if got := ansi.Strip(s.View()); !strings.Contains(got, "ctrl+c:press again to quit") {
		t.Errorf("expected the quit warning in the status row, got:\n%s", got)
	}
}

// TestSecondCtrlCQuits pins the second half of the guard: a second
// ctrl+c while armed actually quits.
func TestSecondCtrlCQuits(t *testing.T) {
	s := newScreen(t, 100, 30)
	next, _ := s.Update(keySettingsMsg("ctrl+c"))
	s = next.(Screen)
	_, cmd := s.Update(keySettingsMsg("ctrl+c"))
	if cmd == nil {
		t.Fatal("expected a Cmd from the second ctrl+c")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("got %T, want tea.QuitMsg", cmd())
	}
}

// TestAnyOtherKeyDisarmsQuit pins the same "any key other than a second
// ctrl+c clears the arm" rule conversation.Screen's handleKey applies,
// so a stray keystroke cannot leave the screen one press from quitting.
func TestAnyOtherKeyDisarmsQuit(t *testing.T) {
	s := newScreen(t, 100, 30)
	next, _ := s.Update(keySettingsMsg("ctrl+c"))
	s = next.(Screen)
	if !s.quitArmed {
		t.Fatal("setup: expected quitArmed after the first ctrl+c")
	}
	next, _ = s.Update(keySettingsMsg("down"))
	s = next.(Screen)
	if s.quitArmed {
		t.Error("expected an unrelated key to disarm quit")
	}
}

// TestCtrlCDismissesOverlayInsteadOfArmingQuit pins the help overlay's
// "any key dismisses it" contract (matching conversation.Screen's own
// handleKey ordering): ctrl+c while the overlay is open must close the
// overlay, not arm or trigger quit.
func TestCtrlCDismissesOverlayInsteadOfArmingQuit(t *testing.T) {
	s := newScreen(t, 100, 30)
	next, _ := s.Update(keySettingsMsg("?"))
	s = next.(Screen)
	if s.overlay == "" {
		t.Fatal("setup: expected '?' to open the help overlay")
	}

	next, cmd := s.Update(keySettingsMsg("ctrl+c"))
	s = next.(Screen)
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("ctrl+c over the overlay must not quit")
		}
	}
	if s.overlay != "" {
		t.Error("expected ctrl+c to dismiss the overlay")
	}
	if s.quitArmed {
		t.Error("expected ctrl+c over the overlay to leave quit unarmed")
	}
}

// TestGutterClipsAnOverflowingRowWithTheClipMarker mirrors
// conversation.Screen's own gutter contract: the screen-edge fallback
// clip must mark a cut row with the shared marker, not truncate it
// silently (wireframes-panes.md section 8/14).
func TestGutterClipsAnOverflowingRowWithTheClipMarker(t *testing.T) {
	s := newScreen(t, 40, 20)
	got := s.gutter([]string{strings.Repeat("x", 200)})
	if !strings.Contains(got, uikitconfig.ClipMarker) {
		t.Errorf("got %q, want the clip marker %q on the overflowing row", got, uikitconfig.ClipMarker)
	}
}

func TestTabAndShiftTabSwitchPanes(t *testing.T) {
	s := newScreen(t, 100, 30)
	if s.focus != render.Left {
		t.Fatalf("expected initial focus = Left, got %v", s.focus)
	}

	// Press Tab: switches focus to Right (options pane)
	next, _ := s.Update(keySettingsMsg("tab"))
	s = next.(Screen)
	if s.focus != render.Right {
		t.Fatalf("expected Tab to switch focus to Right, got %v", s.focus)
	}

	// Press Shift+Tab: switches focus back to Left (sidebar tabs)
	next, _ = s.Update(keySettingsMsg("shift+tab"))
	s = next.(Screen)
	if s.focus != render.Left {
		t.Fatalf("expected Shift+Tab to switch focus to Left, got %v", s.focus)
	}
}

func TestDialogTitleFollowsActiveSection(t *testing.T) {
	s := newScreen(t, 100, 30)
	if !strings.Contains(s.title(), "general") {
		t.Errorf("expected initial title to contain 'general', got %q", s.title())
	}
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	if !strings.Contains(s.title(), "projects") {
		t.Errorf("expected title after Down to contain 'projects', got %q", s.title())
	}
}

func TestSmallScreenSettingsNavKeepsCursorVisible(t *testing.T) {
	s := newScreen(t, 80, 12)
	// Move down to the last section in nav
	for i := 0; i < len(s.sections); i++ {
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	view := s.View()
	lastTitle := s.sections[len(s.sections)-1].Title()
	if !strings.Contains(view, lastTitle) {
		t.Errorf("expected last section %q to be visible on small screen when selected:\n%s", lastTitle, view)
	}
}
