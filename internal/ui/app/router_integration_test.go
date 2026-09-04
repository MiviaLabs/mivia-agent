package app_test

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	settingsscreen "github.com/MiviaLabs/mivia-agent/internal/ui/screen/settings"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/themepicker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// routerFixture bundles the dark/light themes a router integration test
// needs, split out so each test function stays within the repo's
// per-function line budget.
type routerFixture struct {
	dark, light theme.Theme
	themes      []theme.Theme
}

func newRouterFixture(t *testing.T) routerFixture {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var f routerFixture
	f.themes = themes
	for _, th := range themes {
		switch th.Name {
		case "mivia-dark":
			f.dark = th
		case "mivia-light":
			f.light = th
		}
	}
	if f.dark.Name == "" || f.light.Name == "" {
		t.Fatal("need both mivia-dark and mivia-light embedded")
	}
	return f
}

// openThemePicker builds a router over a real conversation.Screen, types
// "h" into the composer (to later prove state survives the round trip),
// and opens the theme picker via ctrl+t.
func openThemePicker(t *testing.T, f routerFixture) app.Model {
	t.Helper()
	base := conversation.New(f.dark, theme.TierTrueColor, f.themes, replay.New(nil, 0), nil, 40, nil)
	m := app.New(base, f.dark, theme.TierTrueColor, f.themes)

	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	m = next.(app.Model)

	next, cmd := m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m = next.(app.Model)
	if cmd == nil {
		t.Fatal("expected a PushScreenMsg Cmd from ctrl+t")
	}
	next, _ = m.Update(cmd())
	return next.(app.Model)
}

// selectTheme filters the open picker down to name and confirms it.
func selectTheme(t *testing.T, m app.Model, name string) app.Model {
	t.Helper()
	var cmd tea.Cmd
	var next tea.Model
	for _, r := range name {
		next, cmd = m.Update(tea.KeyPressMsg{Text: string(r), Code: r})
		m = next.(app.Model)
		if cmd != nil {
			t.Fatal("filtering should not itself emit a Cmd")
		}
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(app.Model)
	if cmd == nil {
		t.Fatal("expected a ThemeSelectedMsg Cmd from enter")
	}
	next, _ = m.Update(cmd())
	return next.(app.Model)
}

// TestThemePickerOpensAsAltScreenModal is the cross-package integration
// test no per-package test file can be: it drives app.Model and the
// real conversation.Screen/themepicker.Screen together through ctrl+t,
// the composition Step 7 actually introduces.
func TestThemePickerOpensAsAltScreenModal(t *testing.T) {
	f := newRouterFixture(t)
	m := openThemePicker(t, f)

	view := m.View()
	if !view.AltScreen {
		t.Error("expected the pushed theme picker to render full alt-screen")
	}
	if !strings.Contains(view.Content, "select a theme") || !strings.Contains(view.Content, f.light.Name) {
		t.Errorf("expected the real themepicker.Screen's view, got:\n%s", view.Content)
	}
}

// TestSelectingThemeChangesRenderedColourAndPreservesState is the
// regression this package exists to catch: selecting a theme must
// actually reach the base screen's rendering, not just app.Model.Theme.
func TestSelectingThemeChangesRenderedColourAndPreservesState(t *testing.T) {
	f := newRouterFixture(t)
	m := openThemePicker(t, f)
	m = selectTheme(t, m, "light")

	if m.Theme.Name != f.light.Name {
		t.Errorf("got theme %q, want the router to have adopted %q", m.Theme.Name, f.light.Name)
	}
	// The picker is gone, so its own chrome is gone with it. AltScreen
	// cannot tell us this any more: the cockpit renders every screen on
	// the alternate buffer, so the content is the only evidence.
	view := m.View()
	if strings.Contains(view.Content, "select a theme") {
		t.Error("expected the picker popped off the stack")
	}
	if !strings.Contains(view.Content, "h") {
		t.Error("expected the composer's typed text to survive the picker round trip")
	}

	// The composer's accent prompt must render in the newly-adopted
	// theme's accent colour, not the original mivia-dark's.
	wantAccent := render.Role(f.light, theme.TierTrueColor, theme.RoleAccent).Render("› ")
	if !strings.Contains(view.Content, wantAccent) {
		t.Errorf("expected the composer prompt styled with %s's accent colour, got:\n%q", f.light.Name, view.Content)
	}
	darkAccent := render.Role(f.dark, theme.TierTrueColor, theme.RoleAccent).Render("› ")
	if darkAccent != wantAccent && strings.Contains(view.Content, darkAccent) {
		t.Errorf("expected the original mivia-dark accent colour gone after switching to %s, got:\n%q", f.light.Name, view.Content)
	}
}

// TestSettingsNoticeReachesTheConversationTranscript pins the routing of
// the full-disk live re-arm's never-silent disclosure: the message lands in
// the base conversation screen's transcript as a permanent notice, even
// while a pushed modal (Settings) sits on top.
func TestSettingsNoticeReachesTheConversationTranscript(t *testing.T) {
	f := newRouterFixture(t)
	base := conversation.New(f.dark, theme.TierTrueColor, f.themes, replay.New(nil, 0), nil, 80, nil)
	m := app.New(base, f.dark, theme.TierTrueColor, f.themes)

	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(app.Model)

	next, _ = m.Update(app.SettingsNoticeMsg{Text: "workspace: FULL DISK ACCESS — file tools are not confined to the workspace"})
	m = next.(app.Model)

	if got := m.View().Content; !strings.Contains(got, "FULL DISK ACCESS") {
		t.Errorf("transcript view missing the never-silent disclosure:\n%s", got)
	}
}

// TestTranscriptPagerAndBaseScreenReceiveBroadcastEvents verifies that while the
// transcript pager is open, broadcast event messages update both the pager on top
// and the underlying conversation screen.
func TestTranscriptPagerAndBaseScreenReceiveBroadcastEvents(t *testing.T) {
	f := newRouterFixture(t)
	base := conversation.New(f.dark, theme.TierTrueColor, f.themes, replay.New(nil, 0), nil, 80, nil)
	m := app.New(base, f.dark, theme.TierTrueColor, f.themes)

	// Measure router
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(app.Model)

	// Open transcript pager with ctrl+o
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = next.(app.Model)
	if cmd == nil {
		t.Fatal("expected PushScreenMsg from ctrl+o")
	}
	next, _ = m.Update(cmd())
	m = next.(app.Model)

	// Broadcast an event while pager is open
	ev := uievent.Event{
		Kind: uievent.KindNotice,
		Body: uievent.NoticeBody{Text: "live router broadcast notice"},
	}
	next, _ = m.Update(uievent.EventMsg{Event: ev})
	m = next.(app.Model)

	// Pager on top should show the notice
	pagerView := m.View().Content
	if !strings.Contains(pagerView, "live router broadcast notice") {
		t.Errorf("pager view does not contain live notice:\n%s", pagerView)
	}

	// Pop the pager with ctrl+o (or leave key)
	next, popCmd := m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = next.(app.Model)
	if popCmd == nil {
		t.Fatal("expected PopScreenMsg from pager leave key")
	}
	next, _ = m.Update(popCmd())
	m = next.(app.Model)

	// Base conversation screen underneath should also show the notice
	baseView := m.View().Content
	if !strings.Contains(baseView, "live router broadcast notice") {
		t.Errorf("base conversation view does not contain live notice after pop:\n%s", baseView)
	}
}

// TestSelectingThemeRepaintsTheScreenSurface is the end-to-end shape of
// the reported bug: Enter on a theme row must change what the base
// screen actually paints, background included. Foreground roles alone
// are not enough - the largest coloured area on screen is the surface
// under everything, and a theme switch that leaves it on the terminal's
// own colour reads as "nothing happened".
func TestSelectingThemeRepaintsTheScreenSurface(t *testing.T) {
	f := newRouterFixture(t)
	m := openThemePicker(t, f)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(app.Model)
	m = selectTheme(t, m, "light")

	got := m.View().Content
	want := bgSeq(f.light)
	old := bgSeq(f.dark)
	if want == old {
		t.Fatal("the two themes share a bg colour; this test needs them to differ")
	}
	if !strings.Contains(got, want) {
		t.Errorf("the base screen is not painted with %s's surface:\n%q", f.light.Name, got)
	}
	if strings.Contains(got, old) {
		t.Errorf("mivia-dark's surface survived the switch:\n%q", got)
	}

	// The typed text must be legible on that surface: the composer's
	// embedded textinput ships library-default styles and kept them
	// before this, so a light theme drew white text on a light surface.
	wantFG := render.Role(f.light, theme.TierTrueColor, theme.RoleFG).Render("h")
	if !strings.Contains(got, wantFG) {
		t.Errorf("the composer's text is not styled with %s's fg role:\n%q", f.light.Name, got)
	}
}

// bgSeq is the escape sequence that paints th's surface: what FillBG
// emits around empty content.
func bgSeq(th theme.Theme) string {
	return strings.TrimSuffix(render.FillBG(th, theme.TierTrueColor, theme.RoleBG, ""), "\x1b[m")
}

// TestSelectingThemeLeavesNoValueOnTheOldTheme is the exhaustive form of
// the switch: Theme and Tier are plain value fields on the router, every
// Screen, and every component they own - there is no shared pointer - so
// a single missed assignment leaves part of the tree rendering in the
// previous theme. Rather than list the components (a list goes stale the
// moment one is added), this walks the whole live router and asserts
// every theme.Theme and theme.Tier value reachable from it is the one
// just selected.
func TestSelectingThemeLeavesNoValueOnTheOldTheme(t *testing.T) {
	f := newRouterFixture(t)
	m := openThemePicker(t, f)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(app.Model)
	m = selectTheme(t, m, "light")

	for _, bad := range staleThemes(reflect.ValueOf(m), "app.Model", f.light.Name, theme.TierTrueColor, map[uintptr]bool{}) {
		t.Error(bad)
	}
}

// themeType and tierType are the two value types the walk looks for.
var (
	themeType = reflect.TypeOf(theme.Theme{})
	tierType  = reflect.TypeOf(theme.TierTrueColor)
)

// staleThemes reports every theme.Theme or theme.Tier reachable from v
// that is not wantName/wantTier.
//
// A []theme.Theme is the candidate set a picker offers, not a value in
// use, so element themes are skipped - but the slice is still walked for
// nothing else, and its own fields are not values in use either.
// Unexported fields are read, never called: reflect allows reading a
// read-only Value's contents, which is all this needs.
func staleThemes(v reflect.Value, path, wantName string, wantTier theme.Tier, seen map[uintptr]bool) []string {
	var bad []string
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == themeType {
			if got := v.FieldByName("Name").String(); got != wantName {
				bad = append(bad, path+".Name = "+got+", want "+wantName)
			}
			return bad
		}
		for i := 0; i < v.NumField(); i++ {
			bad = append(bad, staleThemes(v.Field(i), path+"."+v.Type().Field(i).Name, wantName, wantTier, seen)...)
		}
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		if v.Kind() == reflect.Pointer {
			if seen[v.Pointer()] {
				return nil
			}
			seen[v.Pointer()] = true
		}
		bad = append(bad, staleThemes(v.Elem(), path, wantName, wantTier, seen)...)
	case reflect.Slice, reflect.Array:
		if v.Type().Elem() == themeType {
			return nil // the picker's candidate set, not a theme in use
		}
		for i := 0; i < v.Len(); i++ {
			bad = append(bad, staleThemes(v.Index(i), path+"[]", wantName, wantTier, seen)...)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			bad = append(bad, staleThemes(v.MapIndex(k), path+"[k]", wantName, wantTier, seen)...)
		}
	default:
		if v.Type() != tierType {
			return nil
		}
		got := theme.Tier(v.Uint())
		if got != wantTier {
			bad = append(bad, path+" tier = "+strconv.Itoa(int(got))+", want "+strconv.Itoa(int(wantTier)))
		}
	}
	return bad
}

// TestReopeningThePickerOpensOnTheAppliedTheme is the round trip: after
// a switch, opening the picker again must start on the theme now in use
// and render in it. The picker is built fresh from the base screen's
// theme each time, so this proves that screen really adopted the change
// - and that the modal no longer opens in whichever name sorts first.
func TestReopeningThePickerOpensOnTheAppliedTheme(t *testing.T) {
	f := newRouterFixture(t)
	m := openThemePicker(t, f)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(app.Model)
	m = selectTheme(t, m, "light")

	next, cmd := m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m = next.(app.Model)
	if cmd == nil {
		t.Fatal("expected a PushScreenMsg Cmd from the second ctrl+t")
	}
	next, _ = m.Update(cmd())
	m = next.(app.Model)

	view := m.View().Content
	if !strings.Contains(ansi.Strip(view), "> "+f.light.Name) {
		t.Errorf("the reopened picker does not highlight %q:\n%s", f.light.Name, ansi.Strip(view))
	}
	want := render.Role(f.light, theme.TierTrueColor, theme.RoleAccent).Render("> ")
	if !strings.Contains(view, want) {
		t.Errorf("the reopened picker is not drawn in %q:\n%q", f.light.Name, view)
	}
}

// TestSelectingThemeLeavesNoValueOnTheOldThemeWithSettingsPushed is
// TestSelectingThemeLeavesNoValueOnTheOldTheme's fixture, extended to
// cover the settings screen: the reflective walk only reaches values
// live on the router, so a settings screen with a themed value the
// walk never visited would pass silently.
//
// The stack is built as base -> settings -> picker via two direct
// PushScreenMsg sends (app.Model.Update handles that message itself,
// never delegating to the current top screen, so this does not need
// settings to forward ctrl+t - it doesn't, by design: ContextSettings
// does not cascade to ContextGlobal). This matches applyTheme's real
// shape: the broadcast reaches every stack entry before the pop
// removes only the top one (the picker), so settings survives into the
// final stack the walk inspects, updated in place.
func TestSelectingThemeLeavesNoValueOnTheOldThemeWithSettingsPushed(t *testing.T) {
	f := newRouterFixture(t)
	base := conversation.New(f.dark, theme.TierTrueColor, f.themes, replay.New(nil, 0), nil, 40, nil)
	m := app.New(base, f.dark, theme.TierTrueColor, f.themes)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(app.Model)

	tb := topbar.New(f.dark, theme.TierTrueColor, ports.ModelInfo{}, ports.Usage{}, 100)
	settingsScr := settingsscreen.New(f.dark, theme.TierTrueColor, tb, ports.Settings{}, 0)
	next, _ = m.Update(app.PushScreenMsg{Screen: settingsScr})
	m = next.(app.Model)

	next, _ = m.Update(app.PushScreenMsg{Screen: themepicker.New(f.dark, theme.TierTrueColor, f.themes)})
	m = next.(app.Model)
	m = selectTheme(t, m, "light")

	for _, bad := range staleThemes(reflect.ValueOf(m), "app.Model", f.light.Name, theme.TierTrueColor, map[uintptr]bool{}) {
		t.Error(bad)
	}
}
