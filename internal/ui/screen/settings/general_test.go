package settings

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// newHarnessScreen builds a settings screen wired to mockSettings - end to
// end through the ports interfaces.
func newHarnessScreen(t *testing.T, width, height int) (Screen, *mockSettings) {
	t.Helper()
	th := loadTheme(t)
	h := newMockSettings()
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
	s, _ := newHarnessScreen(t, 100, 50)
	plain := ansi.Strip(s.sections[0].View())
	for _, want := range []string{
		"mouse capture", "show reasoning", "iteration notice", "prompt cache notice", "scroll lines",
		"approval default", "screen reader", "reduced motion", "full disk access",
		"sync: include thinking", "sync: include tool io", "sync: stream assistant",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("General view is missing %q:\n%s", want, plain)
		}
	}
}

func TestGeneralRowsHaveVisualGroups(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 50)
	plain := ansi.Strip(s.sections[0].View())
	for _, want := range []string{"Interaction", "Behavior", "Accessibility", "Permissions", "Sync"} {
		if !strings.Contains(plain, want) {
			t.Errorf("General view is missing group label %q:\n%s", want, plain)
		}
	}
}

func TestGeneralChoiceRowsUseVisibleAffordance(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 50)
	plain := ansi.Strip(s.sections[0].View())
	for _, label := range []string{"scroll lines", "approval default"} {
		row := lineFor(t, plain, label)
		if !strings.Contains(row, "[") || !strings.Contains(row, "]") {
			t.Errorf("choice row %q has no visible value affordance: %q", label, row)
		}
	}
}

func TestGeneralSelectedRowUsesSelectionStyleAndMarker(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	raw := s.sections[0].View()
	plain := ansi.Strip(raw)
	if !strings.Contains(plain, "> "+"mouse capture") {
		t.Fatalf("selected row lost its marker:\n%s", plain)
	}
	selectionPrefix := styledPrefix(render.WithBg(render.Role(s.Theme, theme.TierTrueColor, theme.RoleFGSubtle), s.Theme, theme.TierTrueColor, theme.RoleBGSelection).Render("mouse capture"))
	if !strings.Contains(raw, selectionPrefix) {
		t.Fatalf("selected row does not use the selection background role:\n%s", raw)
	}
}

func TestSmokeGeneralSettingsView(t *testing.T) {
	s, _ := newHarnessScreen(t, 72, 50)
	plain := ansi.Strip(s.sections[0].View())
	for _, want := range []string{"Interaction", "Accessibility", "Permissions", "Sync", "[ ON  ]", "[ OFF ]"} {
		if !strings.Contains(plain, want) {
			t.Errorf("smoke view is missing %q:\n%s", want, plain)
		}
	}
	for i, line := range strings.Split(plain, "\n") {
		if ansi.StringWidth(line) > 72 {
			t.Errorf("smoke row %d exceeds width 72 (%d): %q", i, ansi.StringWidth(line), line)
		}
	}
}

func TestGeneralDisplayGroupsDoNotAddSelectableRows(t *testing.T) {
	s, _ := newHarnessScreen(t, 72, 24)
	sec := s.sections[0].(*generalSection)
	if got := len(sec.displayRows()); got != len(sec.rows)+5 {
		t.Fatalf("display rows = %d, want %d data rows plus five headers", got, len(sec.rows)+5)
	}
	for i := 0; i < 4; i++ {
		next, _ := sec.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
		sec = next.(*generalSection)
	}
	if sec.cursor != 4 {
		t.Fatalf("cursor = %d after four down presses, want data row 4; group headers must not be selectable", sec.cursor)
	}
}

// TestBooleanRowsRenderExplicitOnOffControls pins the semantic control
// itself: every boolean row must show the explicit "[ ON  ]"/"[ OFF ]"
// word, not the bare "on"/"off" choice text a KindChoice field renders
// by default - the word is what carries the state, so it must be
// visible in the plain (ANSI-stripped) view on its own.
func TestBooleanRowsRenderExplicitOnOffControls(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(s.sections[0].View())
	v := h.SettingsAdapters().General.General()
	for _, tc := range []struct {
		label string
		on    bool
	}{
		{"mouse capture", v.Mouse},
		{"show reasoning", v.ShowReasoning},
		{"iteration notice", v.ShowIterationNotices},
		{"prompt cache notice", v.ShowPromptCacheNotices},
		{"screen reader", v.ScreenReader},
		{"reduced motion", v.ReducedMotion},
		{"full disk access", v.FullDiskAccess},
		{"sync: include thinking", v.SyncIncludeThinking},
		{"sync: include tool io", v.SyncIncludeToolIO},
		{"sync: stream assistant", v.SyncStreamAssistant},
	} {
		row := lineFor(t, plain, tc.label)
		want := boolControlText(tc.on)
		if !strings.Contains(row, want) {
			t.Errorf("row %q = %q, want to contain %q", tc.label, row, want)
		}
	}
}

// TestBooleanRowsAreNotBareOnOffText: the value column must not fall
// back to the plain "on"/"off" choice text field.View() would otherwise
// produce for a KindChoice row - the row must go through the semantic
// control, not the field's own default rendering.
func TestBooleanRowsAreNotBareOnOffText(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(s.sections[0].View())
	row := lineFor(t, plain, "mouse capture")
	if strings.Contains(row, "[ ON  ]") && strings.Contains(row, " on ") {
		t.Errorf("row mixes semantic control and bare choice text: %q", row)
	}
	// The row must contain the control's brackets, not a bare word.
	if !strings.Contains(row, "[") || !strings.Contains(row, "]") {
		t.Errorf("row %q does not use the bracketed semantic control", row)
	}
}

// TestScrollLinesAndApprovalStayExplicitValues: the two non-boolean rows
// must keep showing their raw value (a number, a posture name) rather
// than an ON/OFF control, which would misdescribe a preset choice.
func TestScrollLinesAndApprovalStayExplicitValues(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(s.sections[0].View())
	v := h.SettingsAdapters().General.General()

	scrollRow := lineFor(t, plain, "scroll lines")
	if strings.Contains(scrollRow, "[ ON") || strings.Contains(scrollRow, "[ OFF") {
		t.Errorf("scroll lines row rendered a boolean control: %q", scrollRow)
	}
	if !strings.Contains(scrollRow, strconv.Itoa(v.ScrollLines)) {
		t.Errorf("scroll lines row = %q, want to contain %d", scrollRow, v.ScrollLines)
	}

	approvalRow := lineFor(t, plain, "approval default")
	if strings.Contains(approvalRow, "[ ON") || strings.Contains(approvalRow, "[ OFF") {
		t.Errorf("approval default row rendered a boolean control: %q", approvalRow)
	}
	if !strings.Contains(approvalRow, v.ApprovalDefault) {
		t.Errorf("approval default row = %q, want to contain %q", approvalRow, v.ApprovalDefault)
	}
}

// TestOnRowsUseSuccessRoleOffRowsUseMutedRole pins the styling half of
// the semantic control: an enabled row's ANSI SGR must resolve to the
// theme's RoleSuccess colour and a disabled row's to RoleFGMuted -
// checked by comparing each row's raw (unstripped) rendering against
// render.Role's own output for that role, so the assertion tracks the
// theme rather than a hard-coded escape sequence.
func TestOnRowsUseSuccessRoleOffRowsUseMutedRole(t *testing.T) {
	th := loadTheme(t)
	s, h := newHarnessScreen(t, 100, 30)
	v := h.SettingsAdapters().General.General()

	raw := s.sections[0].View()
	lines := strings.Split(raw, "\n")

	successPrefix := styledPrefix(render.Role(th, theme.TierTrueColor, theme.RoleSuccess).Render(boolControlText(true)))
	mutedPrefix := styledPrefix(render.Role(th, theme.TierTrueColor, theme.RoleFGMuted).Render(boolControlText(false)))

	for _, tc := range []struct {
		label string
		on    bool
	}{
		{"mouse capture", v.Mouse},
		{"screen reader", v.ScreenReader},
	} {
		line := rawLineFor(t, lines, tc.label)
		if tc.on {
			if !strings.Contains(line, successPrefix) {
				t.Errorf("row %q (on) does not carry the RoleSuccess SGR prefix %q:\n%q", tc.label, successPrefix, line)
			}
		} else {
			if !strings.Contains(line, mutedPrefix) {
				t.Errorf("row %q (off) does not carry the RoleFGMuted SGR prefix %q:\n%q", tc.label, mutedPrefix, line)
			}
		}
	}
}

// TestBooleanControlsReadableInASCIITier pins the NO_COLOR/ASCII
// degradation: with TierASCII (no colour resolves for any role), the
// ON/OFF word itself - not colour - must still distinguish the two
// states in the plain view.
func TestBooleanControlsReadableInASCIITier(t *testing.T) {
	th := loadTheme(t)
	store := &failingGeneralStore{view: ports.GeneralView{Mouse: true, FullDiskAccess: false}}
	sec := newGeneralSection(store)
	sec.SetSize(100, 30)
	sec.SetTheme(th, theme.TierASCII)

	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, boolControlText(true)) {
		t.Errorf("ASCII-tier view missing the ON control:\n%s", plain)
	}
	if !strings.Contains(plain, boolControlText(false)) {
		t.Errorf("ASCII-tier view missing the OFF control:\n%s", plain)
	}
}

// TestSelectedRowMarkerSurvivesTheSemanticControl: the "> " cursor
// marker on the highlighted row must still be present and lead the
// line once boolean rows render through the semantic control instead of
// field.View()'s own text - the marker is drawn by the section, not the
// field, so this should be unaffected, but it is the kind of thing a
// Columns-based rewrite could silently break.
func TestSelectedRowMarkerSurvivesTheSemanticControl(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(s.sections[0].View())
	lines := strings.Split(plain, "\n")
	found := false
	for _, l := range lines {
		if strings.HasPrefix(l, "> ") && strings.Contains(l, "mouse capture") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the initially-selected row (mouse capture) to lead with \"> \":\n%s", plain)
	}
	// Every non-selected visible row must lead with the "  " (two-space)
	// non-marker instead.
	for _, l := range lines {
		if l == "" {
			continue
		}
		if !strings.HasPrefix(l, "> ") && !strings.HasPrefix(l, "  ") {
			t.Errorf("row %q does not lead with a marker or its two-space placeholder", l)
		}
	}
}

// TestNarrowWidthKeepsRowsReadable: at a narrow width the label and
// value must still both render legibly and no row may exceed the
// available width - the Columns-based alignment must not force a wide
// minimum.
func TestNarrowWidthKeepsRowsReadable(t *testing.T) {
	s, _ := newHarnessScreen(t, 40, 20)
	plain := ansi.Strip(s.sections[0].View())
	if !strings.Contains(plain, "mouse capture") {
		t.Errorf("narrow view missing the mouse capture label:\n%s", plain)
	}
	if !strings.Contains(plain, boolControlText(false)) && !strings.Contains(plain, boolControlText(true)) {
		t.Errorf("narrow view missing a boolean control:\n%s", plain)
	}
}

// TestHintsIncludesCycleBack pins the plan requirement: "-" is a
// supported key on every row (commit(-1)), so Hints must advertise
// keymap.IDSettingsCycleBack alongside the existing up/down/toggle
// hints, not just the forward-cycling toggle.
func TestHintsIncludesCycleBack(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	sec := s.sections[0].(*generalSection)
	hints := sec.Hints()
	found := false
	for _, h := range hints {
		if h == keymap.IDSettingsCycleBack {
			found = true
		}
	}
	if !found {
		t.Errorf("Hints() = %v, want it to include keymap.IDSettingsCycleBack", hints)
	}
}

// lineFor returns the single line of plain (ANSI-stripped) view text
// containing label, failing the test if there isn't exactly one.
func lineFor(t *testing.T, plain, label string) string {
	t.Helper()
	for _, l := range strings.Split(plain, "\n") {
		if strings.Contains(l, label) {
			return l
		}
	}
	t.Fatalf("no line found containing %q in:\n%s", label, plain)
	return ""
}

// rawLineFor is lineFor's raw (ANSI-carrying) counterpart: it matches by
// searching the ANSI-stripped form of each raw line for label, so ANSI
// codes surrounding the label do not break the match, and returns the
// RAW (unstripped) line so the caller can inspect its SGR codes.
func rawLineFor(t *testing.T, rawLines []string, label string) string {
	t.Helper()
	for _, l := range rawLines {
		if strings.Contains(ansi.Strip(l), label) {
			return l
		}
	}
	t.Fatalf("no raw line found containing %q in:\n%s", label, strings.Join(rawLines, "\n"))
	return ""
}

// styledPrefix returns the leading SGR escape sequence(s) of a
// lipgloss-rendered string - the part before its first plain
// character - so a test can assert two renderings share the same
// colour code without hard-coding the escape sequence itself.
func styledPrefix(rendered string) string {
	stripped := ansi.Strip(rendered)
	if stripped == "" {
		return rendered
	}
	if idx := strings.Index(rendered, stripped[:1]); idx > 0 {
		return rendered[:idx]
	}
	return ""
}

// TestSpaceCommitsTheFullDiskRow drives the real path for the full-disk
// row (the LAST row): cycling it must reach the store as
// ports.SetFullDiskAccess and round-trip through General() - the row is
// wired to its own edit variant, not a repurposed boolean.
func TestSpaceCommitsTheFullDiskRow(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	before := h.SettingsAdapters().General.General().FullDiskAccess

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // focus detail
	s = next.(Screen)
	for i := 0; i < 8; i++ { // down to the 9th (last) row
		next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	next, cmd := s.Update(tea.KeyPressMsg{Text: " ", Code: ' '})
	s = awaitGeneralSave(t, next.(Screen), cmd)

	after := h.SettingsAdapters().General.General().FullDiskAccess
	if after == before {
		t.Errorf("full disk did not change after committing the row: still %v", after)
	}
	plain := ansi.Strip(s.sections[0].View())
	if !strings.Contains(plain, boolControlText(after)) {
		t.Errorf("the section did not rebuild to show the new value %v:\n%s", after, plain)
	}
}

// failingGeneralStore is a ports.GeneralSettings whose full-disk save
// fails the way the real store does on the same-file refusal (nil error
// from Apply, SaveFailed on the handle), so the section's failure path can
// be driven end to end.
type failingGeneralStore struct{ view ports.GeneralView }

func (f *failingGeneralStore) General() ports.GeneralView { return f.view }

func (f *failingGeneralStore) Apply(_ context.Context, _ ports.Scope, e ports.GeneralEdit) (ports.SaveHandle, error) {
	state := ports.SaveSaved
	if _, ok := e.(ports.SetFullDiskAccess); ok {
		state = ports.SaveFailed
	}
	ch := make(chan ports.SaveEvent, 1)
	ch <- ports.SaveEvent{State: state, Field: "full disk", Message: "persist full-disk setting: refusing"}
	close(ch)
	return stubSaveHandle{events: ch}, nil
}

type stubSaveHandle struct{ events chan ports.SaveEvent }

func (h stubSaveHandle) ID() string                     { return "stub" }
func (h stubSaveHandle) Events() <-chan ports.SaveEvent { return h.events }
func (h stubSaveHandle) Cancel()                        {}

// TestRefusedFullDiskToggleShowsConfirmedValue pins the failure-path
// rebuild (bug-audit ec8a9ef4 a2-2): commit() cycles the row BEFORE the
// save resolves, and a refused save must rebuild back to the store's last
// CONFIRMED value - the row may not keep rendering the refused "on".
func TestRefusedFullDiskToggleShowsConfirmedValue(t *testing.T) {
	th := loadTheme(t)
	store := &failingGeneralStore{view: ports.GeneralView{Mouse: true, FullDiskAccess: false}}
	sec := newGeneralSection(store)
	sec.SetSize(100, 30)
	sec.SetTheme(th, theme.TierTrueColor) // triggers rebuild
	// The full-disk row is no longer the last row (three sync opt-out
	// rows follow it). Find it by label so the test does not silently
	// drift if more rows are appended in either direction.
	sec.cursor = generalRowIndexByLabel(t, sec, "full disk access")

	next, cmd := sec.commit(1)
	if cmd == nil {
		t.Fatal("expected a save Cmd from committing the row")
	}
	got, _ := next.Update(cmd()) // delivers generalFailedMsg
	gs, ok := got.(*generalSection)
	if !ok {
		t.Fatalf("Update returned %T, want *generalSection", got)
	}
	if gs.notice == "" {
		t.Fatal("refusal notice not shown")
	}
	if got := gs.rows[gs.cursor].f.Value(); got != "off" {
		t.Fatalf("full-disk row renders %q after a refused save, want the confirmed \"off\"", got)
	}
}

// TestGeneralSectionHasNoThemeRow: Ctrl-T's dedicated theme picker
// dialog live-previews every theme as the cursor moves, a strictly
// better picking experience than a KindChoice cycler, so General does
// not duplicate the choice.
func TestGeneralSectionHasNoThemeRow(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(s.sections[0].View())
	if strings.Contains(plain, "theme") {
		t.Errorf("General view still shows a theme row:\n%s", plain)
	}
}

// TestSpaceCommitsTheHighlightedRow drives the real path: focus the
// detail pane, cycle the first row (mouse capture), and confirm the
// change round-trips through the harness's own General() read.
func TestSpaceCommitsTheHighlightedRow(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	before := h.SettingsAdapters().General.General().Mouse

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // focus detail
	s = next.(Screen)
	next, cmd := s.Update(tea.KeyPressMsg{Text: " ", Code: ' '})
	s = awaitGeneralSave(t, next.(Screen), cmd)

	after := h.SettingsAdapters().General.General().Mouse
	if after == before {
		t.Errorf("mouse capture did not change after committing the row: still %v", after)
	}
	plain := ansi.Strip(s.sections[0].View())
	if !strings.Contains(plain, boolControlText(after)) {
		t.Errorf("the section did not rebuild to show the new value %v:\n%s", after, plain)
	}
}

// TestDownMovesToTheSecondRowThenSpaceCommitsIt proves cursor movement
// and commit act on the row actually highlighted, not always the
// first: committing row 1 (show reasoning) must change ShowReasoning
// and must NOT touch row 0's (mouse capture) value.
func TestDownMovesToTheSecondRowThenSpaceCommitsIt(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	beforeReason := h.SettingsAdapters().General.General().ShowReasoning
	beforeMouse := h.SettingsAdapters().General.General().Mouse

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	if got := s.sections[0].(*generalSection).cursor; got != 1 {
		t.Fatalf("cursor = %d, want 1 after one down press", got)
	}
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = awaitGeneralSave(t, next.(Screen), cmd)

	afterReason := h.SettingsAdapters().General.General().ShowReasoning
	if afterReason == beforeReason {
		t.Error("show reasoning (row 1) did not change after enter committed it")
	}
	if afterMouse := h.SettingsAdapters().General.General().Mouse; afterMouse != beforeMouse {
		t.Errorf("row 0 (mouse capture) changed to %v even though only row 1 was committed", afterMouse)
	}
}

// TestScrollLinesCyclesThroughThePresetOnly is the field-cannot-hold-an-
// invalid-value contract: committing a KindChoice field never produces
// anything outside its declared set.
func TestScrollLinesCyclesThroughThePresetOnly(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	s = next.(Screen)
	for i := 0; i < 2; i++ {
		next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	if got := s.sections[0].(*generalSection).cursor; got != 2 {
		t.Fatalf("cursor = %d, want 2 (scroll lines row)", got)
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
// scroll-lines value (the "reject non-positive intervals" rule
// Automations follows, extended here to General's own numeric field) and
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

// TestApprovalRowNeverTransitsAutoApprove drives the real key path from the
// prompting default to "deny". Every value the row commits is applied AND
// persisted immediately (there is no preview step), and the runtime half now
// fans out to every pooled session - so a cycle order that passes through
// "always" grants blanket auto-approval to every running session, including
// backgrounded and worktree ones, on the way to tightening the gate. An
// operator interrupted mid-cycle is left there durably.
func TestApprovalRowNeverTransitsAutoApprove(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // focus detail
	s = next.(Screen)
	for i := 0; i < 5; i++ { // down to the approval row
		next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	start := h.SettingsAdapters().General.General().ApprovalDefault
	if start == "always" {
		t.Fatalf("precondition: fixture starts at %q", start)
	}
	// Tighten with the backward key: the forward direction loosens, and the
	// point of the strength ordering is that tightening never has to route
	// through a weaker posture.
	for i := 0; i < 3; i++ {
		var cmd tea.Cmd
		next, cmd = s.Update(tea.KeyPressMsg{Text: "-", Code: '-'})
		s = awaitGeneralSave(t, next.(Screen), cmd)
		got := h.SettingsAdapters().General.General().ApprovalDefault
		if got == "always" {
			t.Fatalf("cycling the approval row reached %q - auto-approve every tool call, applied to every pooled session and persisted, on the way from %q to deny", got, start)
		}
		if got == "deny" {
			return
		}
	}
	t.Fatalf("cycling never reached deny from %q", start)
}

// generalRowIndexByLabel returns the cursor index of the row whose label
// matches want, or fails the test if no row matches. Used by tests that
// must target a specific row by name (e.g. the refused-full-disk test)
// so they do not silently drift when rows are appended or reordered.
func generalRowIndexByLabel(t *testing.T, sec *generalSection, want string) int {
	t.Helper()
	for i, row := range sec.rows {
		if row.label == want {
			return i
		}
	}
	t.Fatalf("general section has no row labelled %q (have %d rows)", want, len(sec.rows))
	return 0
}

// TestSpaceCommitsEachRemainingBooleanRow drives cursor + space/enter to
// every boolean General row not already covered by a dedicated test
// (mouse capture, show reasoning, and full disk access have their own),
// proving each row's closure in the s.rows table literal actually wires
// its apply func to the right field.
func TestSpaceCommitsEachRemainingBooleanRow(t *testing.T) {
	cases := []struct {
		name string
		down int // presses of "down" from row 0 to reach this row
		get  func(ports.GeneralView) bool
	}{
		{"prompt cache notice", 3, func(v ports.GeneralView) bool { return v.ShowPromptCacheNotices }},
		{"screen reader", 6, func(v ports.GeneralView) bool { return v.ScreenReader }},
		{"reduced motion", 7, func(v ports.GeneralView) bool { return v.ReducedMotion }},
		{"sync include thinking", 9, func(v ports.GeneralView) bool { return v.SyncIncludeThinking }},
		{"sync include tool io", 10, func(v ports.GeneralView) bool { return v.SyncIncludeToolIO }},
		{"sync stream assistant", 11, func(v ports.GeneralView) bool { return v.SyncStreamAssistant }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, h := newHarnessScreen(t, 100, 30)
			before := tc.get(h.SettingsAdapters().General.General())

			next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
			s = next.(Screen)
			for i := 0; i < tc.down; i++ {
				next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
				s = next.(Screen)
			}
			if got := s.sections[0].(*generalSection).cursor; got != tc.down {
				t.Fatalf("cursor = %d, want %d (%s row)", got, tc.down, tc.name)
			}
			next, cmd := s.Update(tea.KeyPressMsg{Text: " ", Code: ' '})
			s = awaitGeneralSave(t, next.(Screen), cmd)

			after := tc.get(h.SettingsAdapters().General.General())
			if after == before {
				t.Errorf("%s did not change after committing the row: still %v", tc.name, after)
			}
		})
	}
}

// TestCommitFailureShowsErrorNotice pins commit's own err != nil branch
// (Apply itself failing, as opposed to a Failed SaveEvent, which
// TestFailedApplyKeepsTheOldValue covers): the section must surface the
// error text as its notice and rebuild rather than leaving a stale row.
func TestCommitFailureShowsErrorNotice(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	h.generalApplyErr = errors.New("boom: apply rejected")

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Text: " ", Code: ' '})
	s = next.(Screen)

	sec := s.sections[0].(*generalSection)
	if sec.notice != "boom: apply rejected" {
		t.Fatalf("notice = %q, want the Apply error surfaced", sec.notice)
	}
}

// TestAwaitSaveIncompleteChannelCloseIsAFailure pins awaitSave's own
// defensive branch: a SaveHandle whose channel closes without ever
// emitting a terminal Saved/Failed event (a contract violation a faulty
// or future SaveHandle implementation could still produce) must still
// resolve to a failure rather than silently reporting success.
func TestAwaitSaveIncompleteChannelCloseIsAFailure(t *testing.T) {
	ch := make(chan ports.SaveEvent)
	close(ch)
	msg := awaitSave(fakeIncompleteSaveHandle{ch: ch})()
	failed, ok := msg.(generalFailedMsg)
	if !ok {
		t.Fatalf("awaitSave() = %#v, want generalFailedMsg", msg)
	}
	if failed.message != "save incomplete" {
		t.Fatalf("message = %q, want the \"save incomplete\" fallback", failed.message)
	}
}

type fakeIncompleteSaveHandle struct{ ch chan ports.SaveEvent }

func (h fakeIncompleteSaveHandle) ID() string                     { return "fake-incomplete" }
func (h fakeIncompleteSaveHandle) Events() <-chan ports.SaveEvent { return h.ch }
func (h fakeIncompleteSaveHandle) Cancel()                        {}

// TestHeaderWidthFloorsAtFortyWhenNarrow pins View's own headerWidth
// fallback for a screen too narrow for width-4 to stay positive.
func TestHeaderWidthFloorsAtFortyWhenNarrow(t *testing.T) {
	s, _ := newHarnessScreen(t, 2, 30)
	if got := s.sections[0].View(); got == "" {
		t.Fatal("View() returned empty output for a narrow screen")
	}
}

// TestSpaceCommitsTheScrollLinesRowThroughItsOwnRowClosure drives the
// cursor onto the scroll-lines row (index 4) and commits it via the UI's
// own key path, exercising that row's apply closure (strconv.Atoi) rather
// than calling store.Apply directly the way TestFailedApplyKeepsTheOldValue
// and TestScrollLinesCyclesThroughThePresetOnly's mis-landed cursor do.
func TestSpaceCommitsTheScrollLinesRowThroughItsOwnRowClosure(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	before := h.SettingsAdapters().General.General().ScrollLines

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	s = next.(Screen)
	for i := 0; i < 4; i++ {
		next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	if got := s.sections[0].(*generalSection).cursor; got != 4 {
		t.Fatalf("cursor = %d, want 4 (scroll lines row)", got)
	}
	next, cmd := s.Update(tea.KeyPressMsg{Text: " ", Code: ' '})
	s = awaitGeneralSave(t, next.(Screen), cmd)

	after := h.SettingsAdapters().General.General().ScrollLines
	if after == before {
		t.Errorf("scroll lines did not change after committing its own row: still %d", after)
	}
}
