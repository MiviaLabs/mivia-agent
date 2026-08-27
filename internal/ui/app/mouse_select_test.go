package app

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// gridScreen renders a fixed 3-row grid so drag-select tests have known
// text at known coordinates.
type gridScreen struct {
	flags ViewFlags
}

func (s gridScreen) Init() tea.Cmd                        { return nil }
func (s gridScreen) ViewFlags() ViewFlags                 { return s.flags }
func (s gridScreen) Update(msg tea.Msg) (Screen, tea.Cmd) { return s, nil }
func (s gridScreen) View() string {
	return strings.Join([]string{"hello world", "second row!", "third line."}, "\n")
}

func newGridModel(t *testing.T) Model {
	t.Helper()
	return New(gridScreen{flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil).
		WithOptions(Options{Mouse: true})
}

// TestDragReleaseCopiesSelectedText pins the feature end to end: a
// press, a motion past the jitter threshold, then a release on a
// different cell produces a tea.SetClipboard Cmd carrying the plain
// text between the two points.
func TestDragReleaseCopiesSelectedText(t *testing.T) {
	m := newGridModel(t)

	next, cmd := m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	m = next.(Model)
	if cmd != nil {
		t.Error("a press alone must not yet produce a Cmd")
	}

	next, _ = m.Update(tea.MouseMotionMsg{X: 4, Y: 0, Button: tea.MouseLeft})
	m = next.(Model)
	if !m.sel.dragging {
		t.Fatal("motion past the threshold must mark the selection as dragging")
	}

	next, cmd = m.Update(tea.MouseReleaseMsg{X: 4, Y: 0, Button: tea.MouseLeft})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected a clipboard Cmd from the drag release")
	}
	msg := cmd()
	if got := stringifyClipboardCmdMsg(msg); got != "hello" {
		t.Errorf("copied %q, want %q", got, "hello")
	}
	if m.sel.dragging {
		t.Error("selection state must clear after release")
	}
}

// TestDragAcrossRowsJoinsWithNewline pins the multi-row stream-selection
// shape: the anchor row runs to its end, the end row runs from its
// start, joined by a newline.
func TestDragAcrossRowsJoinsWithNewline(t *testing.T) {
	m := newGridModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 6, Y: 0, Button: tea.MouseLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMotionMsg{X: 5, Y: 1, Button: tea.MouseLeft})
	m = next.(Model)
	next, cmd := m.Update(tea.MouseReleaseMsg{X: 5, Y: 1, Button: tea.MouseLeft})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected a clipboard Cmd")
	}
	got := stringifyClipboardCmdMsg(cmd())
	want := "world\nsecond"
	if got != want {
		t.Errorf("copied %q, want %q", got, want)
	}
}

// TestPlainClickIsUnaffectedByDragSelect pins that a press-and-release
// with no meaningful motion between them is NOT swallowed: it must
// still reach the top screen exactly as before this feature existed
// (conversation.go's handleClick / handleTopbarDoubleClick contract).
func TestPlainClickIsUnaffectedByDragSelect(t *testing.T) {
	m := newGridModel(t)
	next, cmd := m.Update(tea.MouseClickMsg{X: 2, Y: 0, Button: tea.MouseLeft})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("a press must not itself produce a Cmd")
	}
	next, cmd = m.Update(tea.MouseReleaseMsg{X: 2, Y: 0, Button: tea.MouseLeft})
	if cmd != nil {
		t.Error("a plain click's release must not produce a clipboard Cmd")
	}
	_ = next
}

// TestDragSelectInertWhenMouseOptionOff pins rule 7.1's scope: with
// Options.Mouse false (the default), this feature does nothing at all,
// so the terminal's own native selection is never shadowed.
func TestDragSelectInertWhenMouseOptionOff(t *testing.T) {
	m := New(gridScreen{flags: ViewFlags{AltScreen: true}}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMotionMsg{X: 5, Y: 0, Button: tea.MouseLeft})
	m = next.(Model)
	if m.sel.dragging {
		t.Error("drag-select must stay inert while Options.Mouse is false")
	}
}

// TestResizeClearsInFlightSelection pins that a resize invalidates the
// selection: the coordinates were measured against a frame that no
// longer exists.
func TestResizeClearsInFlightSelection(t *testing.T) {
	m := newGridModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMotionMsg{X: 5, Y: 0, Button: tea.MouseLeft})
	m = next.(Model)
	if !m.sel.dragging {
		t.Fatal("precondition: dragging")
	}
	next, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(Model)
	if m.sel.dragging || m.sel.pressed {
		t.Error("a resize must clear the in-flight selection")
	}
}

// stringifyClipboardCmdMsg unwraps tea.SetClipboard's private Msg type.
// setClipboardMsg is `type setClipboardMsg string`, so %v on it
// formats the underlying string directly - the only way to read it
// back without reflect, since the type itself is unexported.
func stringifyClipboardCmdMsg(msg tea.Msg) string {
	return fmt.Sprintf("%v", msg)
}
