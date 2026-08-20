package settings

import (
	"context"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/demoharness"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// newHarnessScreen builds a settings screen wired to a real
// demoharness.Harness, the same fake production wiring uses - end to
// end through the ports interfaces, not a hand-rolled mock.
func newHarnessScreen(t *testing.T, width, height int) (Screen, *demoharness.Harness) {
	t.Helper()
	th := loadTheme(t)
	h, err := demoharness.New("smalltalk", 0)
	if err != nil {
		t.Fatal(err)
	}
	tb := topbar.New(th, theme.TierTrueColor, ports.ModelInfo{}, ports.Usage{}, width)
	s := New(th, theme.TierTrueColor, tb, h.SettingsAdapters(), 0)
	next, _ := s.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return next.(Screen), h
}

// awaitGeneralSave drains cmd (a blocking awaitSave call) so the test
// observes the same generalSavedMsg/generalFailedMsg the real program
// loop would deliver, rather than asserting on still-in-flight state.
func awaitGeneralSave(t *testing.T, s Screen, cmd tea.Cmd) Screen {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a Cmd from committing a General row")
	}
	next, _ := s.Update(cmd())
	return next.(Screen)
}

func TestGeneralSectionListsEveryRow(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(s.sections[0].View())
	for _, want := range []string{
		"theme", "mouse capture", "show reasoning", "scroll lines",
		"approval default", "screen reader", "reduced motion",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("General view is missing %q:\n%s", want, plain)
		}
	}
}

// TestSpaceCommitsTheHighlightedRow drives the real path: focus the
// detail pane, cycle the first row (theme), and confirm the change
// round-trips through the harness's own General() read.
func TestSpaceCommitsTheHighlightedRow(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	before := h.SettingsAdapters().General.General().Theme

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // focus detail
	s = next.(Screen)
	next, cmd := s.Update(tea.KeyPressMsg{Text: " ", Code: ' '})
	s = awaitGeneralSave(t, next.(Screen), cmd)

	after := h.SettingsAdapters().General.General().Theme
	if after == before {
		t.Errorf("theme did not change after committing the row: still %q", after)
	}
	plain := ansi.Strip(s.sections[0].View())
	if !strings.Contains(plain, after) {
		t.Errorf("the section did not rebuild to show the new value %q:\n%s", after, plain)
	}
}

// TestDownMovesToTheSecondRowThenSpaceCommitsIt proves cursor movement
// and commit act on the row actually highlighted, not always the
// first: committing row 2 (mouse capture) must change Mouse and must
// NOT touch row 1's (theme) value.
func TestDownMovesToTheSecondRowThenSpaceCommitsIt(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	beforeMouse := h.SettingsAdapters().General.General().Mouse
	beforeTheme := h.SettingsAdapters().General.General().Theme

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	if got := s.sections[0].(*generalSection).cursor; got != 1 {
		t.Fatalf("cursor = %d, want 1 after one down press", got)
	}
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = awaitGeneralSave(t, next.(Screen), cmd)

	afterMouse := h.SettingsAdapters().General.General().Mouse
	if afterMouse == beforeMouse {
		t.Error("mouse capture (row 2) did not change after enter committed it")
	}
	if afterTheme := h.SettingsAdapters().General.General().Theme; afterTheme != beforeTheme {
		t.Errorf("row 1 (theme) changed to %q even though only row 2 was committed", afterTheme)
	}
}

// TestScrollLinesCyclesThroughThePresetOnly is the field-cannot-hold-an-
// invalid-value contract: committing a KindChoice field never produces
// anything outside its declared set.
func TestScrollLinesCyclesThroughThePresetOnly(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	s = next.(Screen)
	for i := 0; i < 3; i++ {
		next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	if got := s.sections[0].(*generalSection).cursor; got != 3 {
		t.Fatalf("cursor = %d, want 3 (scroll lines row)", got)
	}
	next, cmd := s.Update(tea.KeyPressMsg{Text: " ", Code: ' '})
	s = awaitGeneralSave(t, next.(Screen), cmd)

	n := h.SettingsAdapters().General.General().ScrollLines
	got := strconv.Itoa(n)
	found := false
	for _, want := range scrollChoices {
		if want == got {
			found = true
		}
	}
	if !found {
		t.Errorf("ScrollLines = %d, not a member of the preset %v", n, scrollChoices)
	}
}

// TestFailedApplyKeepsTheOldValue: the fake rejects a non-positive
// scroll-lines value (settings-screen.md §12's "reject non-positive
// intervals" rule, extended here to General's own numeric field) and
// the read-back value must be unaffected by the rejected write.
func TestFailedApplyKeepsTheOldValue(t *testing.T) {
	_, h := newHarnessScreen(t, 100, 30)
	before := h.SettingsAdapters().General.General()

	handle, err := h.SettingsAdapters().General.Apply(context.Background(), ports.ScopeUser, ports.SetScrollLines{N: -1})
	if err != nil {
		t.Fatal(err)
	}
	var last ports.SaveEvent
	for ev := range handle.Events() {
		last = ev
	}
	if last.State != ports.SaveFailed {
		t.Fatalf("expected the fake to reject a non-positive scroll-lines value, got %v", last.State)
	}
	if got := h.SettingsAdapters().General.General(); got.ScrollLines != before.ScrollLines {
		t.Errorf("a failed apply changed ScrollLines: %d -> %d", before.ScrollLines, got.ScrollLines)
	}
}

func TestUnavailableGeneralSectionSaysSo(t *testing.T) {
	th := loadTheme(t)
	tb := topbar.New(th, theme.TierTrueColor, ports.ModelInfo{}, ports.Usage{}, 80)
	s := New(th, theme.TierTrueColor, tb, ports.Settings{}, 0)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)
	if got := ansi.Strip(s.sections[0].View()); !strings.Contains(got, "unavailable") {
		t.Errorf("expected the nil-store General section to say unavailable, got %q", got)
	}
}
