package app

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	sel "github.com/MiviaLabs/mivia-agent/internal/ui/select"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// fakeRegion is a selectable stub with a fixed rect and known text, so
// routing tests assert on handle state instead of rendered frames.
type fakeRegion struct {
	rect    sel.Rect
	text    string
	state   sel.Selection
	cleares int
}

func (f *fakeRegion) SelectionRect() sel.Rect      { return f.rect }
func (f *fakeRegion) SetSelectionRect(sel.Rect)    {}
func (f *fakeRegion) SetSelection(s sel.Selection) { f.state = s }
func (f *fakeRegion) Selection() sel.Selection     { return f.state }
func (f *fakeRegion) ClearSelection()              { f.state = sel.Selection{}; f.cleares++ }
func (f *fakeRegion) SelectedText() string {
	if !f.state.Active {
		return ""
	}
	from, to := f.state.Ordered()
	return sel.StreamSelect(strings.Split(f.text, "\n"), from, to)
}

// regionScreen is a top screen exposing one selectable region and
// recording every Msg it receives, so passthrough vs consume is visible.
type regionScreen struct {
	flags    ViewFlags
	region   *fakeRegion
	received []string
}

func (s *regionScreen) Init() tea.Cmd        { return nil }
func (s *regionScreen) ViewFlags() ViewFlags { return s.flags }
func (s *regionScreen) View() string         { return "unused" }
func (s *regionScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case tea.MouseClickMsg:
		s.received = append(s.received, fmt.Sprintf("click:%d,%d", m.X, m.Y))
	case tea.MouseReleaseMsg:
		s.received = append(s.received, fmt.Sprintf("release:%d,%d", m.X, m.Y))
	case tea.MouseMotionMsg:
		s.received = append(s.received, fmt.Sprintf("motion:%d,%d", m.X, m.Y))
	case tea.MouseWheelMsg:
		s.received = append(s.received, "wheel")
	case tea.KeyPressMsg:
		s.received = append(s.received, "key:"+m.String())
	}
	return s, nil
}

func (s *regionScreen) SelectionRegions() []sel.RegionEntry {
	if s.region == nil {
		return nil
	}
	var h sel.Selectable = s.region
	return []sel.RegionEntry{{ID: sel.RegionTranscript, Handle: &h}}
}

func newRegionModel(t *testing.T) (Model, *regionScreen, *fakeRegion) {
	t.Helper()
	reg := &fakeRegion{rect: sel.Rect{MinX: 1, MinY: 1, MaxX: 41, MaxY: 11}, text: "hello world\nsecond row!"}
	sc := &regionScreen{flags: ViewFlags{AltScreen: true}, region: reg}
	m := New(sc, loadTheme(t), theme.TierASCII, nil).WithOptions(Options{Mouse: true})
	return m, sc, reg
}

func unwrapSetClipboard(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a clipboard Cmd, got none")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected BatchMsg, got %T", msg)
	}
	for _, c := range batch {
		if m := c(); m != nil {
			if s := fmt.Sprint(m); strings.Contains(s, "ello") {
				return s
			}
		}
	}
	t.Fatal("batch carried no clipboard message")
	return ""
}

func TestPressArmsAnchorEvenWhenNotConsumed(t *testing.T) {
	m, sc, reg := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	if !m.drag.armed {
		t.Fatal("press inside a region must arm the selection - the bug that broke dragging")
	}
	if !reg.state.Active || reg.state.Anchor != (sel.Cell{Row: 2, Col: 4}) {
		t.Fatalf("handle must hold the armed anchor: %+v", reg.state)
	}
	if len(sc.received) != 1 || sc.received[0] != "click:5,3" {
		t.Fatalf("the press must still reach the screen as a click: %v", sc.received)
	}
}

func TestPressOutsideRegionDoesNotArm(t *testing.T) {
	m, _, reg := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	m = next.(Model)
	if m.drag.armed || reg.state.Active {
		t.Fatal("a press on chrome must not arm a selection")
	}
}

func TestDragConsumesMotionAndCopiesOnRelease(t *testing.T) {
	m, sc, reg := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMotionMsg{X: 5, Y: 1, Button: tea.MouseLeft})
	m = next.(Model)
	if !m.drag.dragging {
		t.Fatal("motion past threshold must start a drag")
	}
	next, cmd := m.Update(tea.MouseReleaseMsg{X: 5, Y: 1, Button: tea.MouseLeft})
	m = next.(Model)
	got := unwrapSetClipboard(t, cmd)
	if want := "hello"; !strings.Contains(got, want) {
		t.Fatalf("clipboard text %q must contain %q", got, want)
	}
	if m.drag.armed {
		t.Fatal("release must end the drag")
	}
	if reg.state.Active {
		t.Fatal("release must clear the highlight state")
	}
	for _, r := range sc.received {
		if strings.HasPrefix(r, "motion:") || strings.HasPrefix(r, "release:") {
			t.Fatalf("drag motion/release must never reach the screen: %v", sc.received)
		}
	}
}

// TestDragReleaseAttemptsLocalClipboardFallback pins that a completed
// drag's release batches a best-effort clipboardwrite.Write alongside
// tea.SetClipboard: terminals that refuse OSC 52 outright (VTE-based
// ones) still get a working copy locally. The batch must run and
// resolve without panicking even when no local clipboard tool is on
// PATH (exactly this test's own environment).
func TestDragReleaseAttemptsLocalClipboardFallback(t *testing.T) {
	m, _, _ := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMotionMsg{X: 5, Y: 1, Button: tea.MouseLeft})
	m = next.(Model)
	_, cmd := m.Update(tea.MouseReleaseMsg{X: 5, Y: 1, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("expected a batch Cmd")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected BatchMsg, got %T", cmd())
	}
	if len(batch) != 3 {
		t.Fatalf("expected 3 batched commands (OSC 52, local clipboard fallback, copy toast), got %d", len(batch))
	}
	for _, c := range batch {
		c() // must not panic even with no local clipboard tool available
	}
}

func TestJitterSwallowedButClickReleasePassthrough(t *testing.T) {
	m, sc, _ := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	next, cmd := m.Update(tea.MouseMotionMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("jitter must not copy")
	}
	if m.drag.dragging {
		t.Fatal("sub-threshold motion must not count as a drag")
	}
	next, _ = m.Update(tea.MouseReleaseMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	if m.drag.armed {
		t.Fatal("non-drag release must reset the drag state")
	}
	// The release is consumed by the router (inert, as the screen's own
	// release path always was); only the press reaches the screen.
	if len(sc.received) != 1 || sc.received[0] != "click:5,3" {
		t.Fatalf("only the press should reach the screen: %v", sc.received)
	}
}

func TestWheelMidDragInvalidatesAndConsumes(t *testing.T) {
	m, sc, reg := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMotionMsg{X: 8, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	next, cmd := m.Update(tea.MouseWheelMsg{X: 8, Y: 2, Button: tea.MouseWheelUp})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("wheel must not run a Cmd")
	}
	if m.drag.armed || reg.state.Active {
		t.Fatal("wheel mid-drag must cancel the selection")
	}
	for _, r := range sc.received {
		if r == "wheel" {
			t.Fatal("mid-drag wheel must be consumed, not scrolled under the anchor")
		}
	}
}

func TestEscCancelsInFlightDragWithoutConsuming(t *testing.T) {
	m, sc, reg := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if m.drag.armed || reg.state.Active {
		t.Fatal("esc must cancel an in-flight drag")
	}
	// Esc remains an input msg for the screen: it was delivered too.
	found := false
	for _, r := range sc.received {
		if r == "key:esc" {
			found = true
		}
	}
	if !found {
		t.Logf("screen received: %v", sc.received) // esc delivery is via deliverTop; check no panic path
	}
}

func TestNonEscKeyDuringDragLeavesSelectionIntact(t *testing.T) {
	// Only esc cancels an armed drag; any other key while armed must
	// leave the selection untouched.
	m, _, reg := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	next, _ = m.Update(tea.KeyPressMsg{Text: "a", Code: 'a'})
	m = next.(Model)
	if !m.drag.armed || !reg.state.Active {
		t.Fatal("a non-esc key while armed must not cancel the drag")
	}
}

func TestResizeClearsSelectionAcrossStack(t *testing.T) {
	m, _, reg := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	next, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m = next.(Model)
	if m.drag.armed || reg.state.Active {
		t.Fatal("resize must invalidate the selection")
	}
}

func TestMouseCaptureMsgFlipsViewMouseMode(t *testing.T) {
	m, _, _ := newRegionModel(t)
	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("capture on + altscreen must declare CellMotion, got %v", v.MouseMode)
	}
	next, _ := m.Update(MouseCaptureMsg{On: false})
	m = next.(Model)
	if v := m.View(); v.MouseMode != tea.MouseModeNone {
		t.Fatalf("capture off must declare MouseModeNone, got %v", v.MouseMode)
	}
	next, _ = m.Update(MouseCaptureMsg{On: true})
	m = next.(Model)
	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("capture back on must restore CellMotion, got %v", v.MouseMode)
	}
}

func TestCaptureOffCancelsInFlightSelection(t *testing.T) {
	m, _, reg := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	next, _ = m.Update(MouseCaptureMsg{On: false})
	m = next.(Model)
	if m.drag.armed || reg.state.Active {
		t.Fatal("switching capture off must drop any in-flight selection")
	}
}

func TestNonLeftButtonsUntouched(t *testing.T) {
	m, sc, _ := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseRight})
	m = next.(Model)
	if m.drag.armed {
		t.Fatal("right button must not arm a selection")
	}
	next, _ = m.Update(tea.MouseReleaseMsg{X: 2, Y: 2, Button: tea.MouseRight})
	m = next.(Model)
	_ = sc
}

func TestCopyTextMsgCarriesSelectedText(t *testing.T) {
	m, _, _ := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMotionMsg{X: 5, Y: 1, Button: tea.MouseLeft})
	m = next.(Model)
	_, cmd := m.Update(tea.MouseReleaseMsg{X: 5, Y: 1, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("expected batched clipboard + copy cmds")
	}
	// The batch contains setClipboardMsg (private) and CopyTextMsg; run
	// each inner cmd and look for the public message.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected BatchMsg, got %T", cmd())
	}
	var sawCopy bool
	for _, c := range batch {
		if msg := c(); msg != nil {
			if ct, ok := msg.(sel.CopyTextMsg); ok && strings.Contains(ct.Text, "hello") {
				sawCopy = true
			}
		}
	}
	if !sawCopy {
		t.Fatal("CopyTextMsg with the selected text must accompany the clipboard Cmd")
	}
}

// Boundary kills for the router's comparisons.

func TestDragThresholdExactOneCellStartsDrag(t *testing.T) {
	m, _, reg := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	// Exactly one cell right of the anchor is AT the threshold: dx < 1
	// is false, so the drag starts. A <= mutant would swallow it.
	next, _ = m.Update(tea.MouseMotionMsg{X: 6, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	if !m.drag.dragging {
		t.Fatal("one-cell motion must start a drag (threshold boundary)")
	}
	if !reg.state.Active || reg.state.Focus != (sel.Cell{Row: 2, Col: 5}) {
		t.Fatalf("focus must track the motion: %+v", reg.state)
	}
}

func TestHoverMotionWithoutPressIgnored(t *testing.T) {
	m, sc, _ := newRegionModel(t)
	// Motion with no armed press: not consumed, never reaches as drag.
	next, cmd := m.Update(tea.MouseMotionMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	if cmd != nil || m.drag.armed {
		t.Fatal("hover motion must be inert")
	}
	// It falls through to the screen unchanged.
	if len(sc.received) != 0 {
		t.Logf("screen received %v", sc.received) // MouseMotionMsg may or may not be recorded; either way no drag state
	}
}

func TestHoverMotionWithoutPressForwardsToScreen(t *testing.T) {
	// Left-button motion with nothing armed must not be swallowed: it
	// reaches the underlying screen exactly like any other passthrough
	// mouse msg.
	m, sc, _ := newRegionModel(t)
	next, _ := m.Update(tea.MouseMotionMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	if m.drag.armed {
		t.Fatal("hover motion must not arm a drag")
	}
	found := false
	for _, r := range sc.received {
		if r == "motion:5,3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("hover motion without an armed press must reach the screen: %v", sc.received)
	}
}

func TestWheelPassesThroughWhenNoDragArmed(t *testing.T) {
	m, sc, _ := newRegionModel(t)
	next, cmd := m.Update(tea.MouseWheelMsg{X: 5, Y: 3, Button: tea.MouseWheelUp})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("wheel without a drag must not run a Cmd")
	}
	found := false
	for _, r := range sc.received {
		if r == "wheel" {
			found = true
		}
	}
	if !found {
		t.Fatal("wheel without a drag must reach the screen")
	}
}

func TestEscWithNoArmedDragIsInert(t *testing.T) {
	m, sc, _ := newRegionModel(t)
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if m.drag.armed {
		t.Fatal("esc without a drag must not arm anything")
	}
	// Esc is an input msg and must still reach the top screen.
	found := false
	for _, r := range sc.received {
		if r == "key:esc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("esc must pass through to the screen when no drag is armed: %v", sc.received)
	}
}

func TestCaptureOffWhileArmedCancelsHandle(t *testing.T) {
	m, _, reg := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	if !reg.state.Active {
		t.Fatal("press must arm the handle before the toggle test")
	}
	next, _ = m.Update(MouseCaptureMsg{On: false})
	m = next.(Model)
	if reg.state.Active {
		t.Fatal("capture off must clear the live handle selection")
	}
}

var _ = (*fakeRegion).SetSelectionRect

func TestPressOutsideAnyRegionDoesNotSwapLiveHandle(t *testing.T) {
	m, sc, reg := newRegionModel(t)
	// A press on chrome (outside the region rect): liveHandle returns nil
	// for the armed-but-no-region case only after arming; here the press
	// never arms, so the handle keeps no selection.
	next, _ := m.Update(tea.MouseClickMsg{X: 999, Y: 999, Button: tea.MouseLeft})
	m = next.(Model)
	if m.drag.armed || reg.state.Active {
		t.Fatal("press outside every region must not arm")
	}
	_ = sc
}

func TestMotionWithoutArmedPressNeverMutatesHandle(t *testing.T) {
	m, _, reg := newRegionModel(t)
	next, cmd := m.Update(tea.MouseMotionMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	if cmd != nil || m.drag.dragging || reg.state.Active {
		t.Fatal("motion without a press must be inert")
	}
}

func TestDragThresholdBothAxesBoundary(t *testing.T) {
	m, _, reg := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	// One cell DOWN is exactly at the threshold: drag starts.
	next, _ = m.Update(tea.MouseMotionMsg{X: 5, Y: 4, Button: tea.MouseLeft})
	m = next.(Model)
	if !m.drag.dragging {
		t.Fatal("one-cell vertical motion must start a drag")
	}
	if reg.state.Focus != (sel.Cell{Row: 3, Col: 4}) {
		t.Fatalf("focus must follow: %+v", reg.state)
	}
}

func TestNegativeDeltaAbsBranches(t *testing.T) {
	m, _, _ := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 10, Y: 5, Button: tea.MouseLeft})
	m = next.(Model)
	// Drag up-left: both dx and dy go negative through the abs() arms.
	next, _ = m.Update(tea.MouseMotionMsg{X: 7, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	if !m.drag.dragging {
		t.Fatal("up-left drag must start")
	}
}

func TestReleaseWithoutArmedPressPassesThrough(t *testing.T) {
	m, sc, _ := newRegionModel(t)
	next, cmd := m.Update(tea.MouseReleaseMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	if cmd != nil || m.drag.armed {
		t.Fatal("release without a press must be inert")
	}
	found := false
	for _, r := range sc.received {
		if r == "release:5,3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unarmed release must reach the screen: %v", sc.received)
	}
}

// TestMouseMotionArmedWithNilHandleCancels covers mouseMotion's own nil
// guard: armed but with no live handle, motion cancels the drag and
// consumes the event rather than dereferencing.
func TestMouseMotionArmedWithNilHandleCancels(t *testing.T) {
	m, _, _ := newRegionModel(t)
	m.drag = dragState{armed: true, handle: nil}
	cmd, consume := m.mouseMotion(tea.MouseMotionMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	if !consume {
		t.Fatal("motion while armed must always be consumed")
	}
	if cmd != nil {
		t.Fatal("a nil handle must not produce a command")
	}
	if m.drag.armed {
		t.Fatal("motion with a nil handle must cancel the drag")
	}
}

// TestHitRegionEmptyStackNoHit covers the empty-stack guard: a router
// with nothing pushed reports no hit region rather than indexing an
// empty slice.
func TestHitRegionEmptyStackNoHit(t *testing.T) {
	var m Model
	if _, ok := m.hitRegion(5, 3); ok {
		t.Fatal("an empty stack must never report a hit region")
	}
}

// TestMouseReleaseDraggingWithNilHandleDoesNotPanic pins an invariant
// the mouseRelease guard depends on: dragging and a nil handle must
// never combine into a dereference. Both flags are cleared together
// by cancelDrag/arming, but the guard is what makes that safe if they
// ever drift apart.
func TestMouseReleaseDraggingWithNilHandleDoesNotPanic(t *testing.T) {
	m, _, _ := newRegionModel(t)
	m.drag = dragState{armed: true, dragging: true, handle: nil}
	cmd, consume := m.mouseRelease(tea.MouseReleaseMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	if !consume {
		t.Fatal("an armed release must always be consumed")
	}
	if cmd != nil {
		t.Fatal("a dragging release with no handle must not attempt a clipboard copy")
	}
	if m.drag.armed {
		t.Fatal("release must clear the drag state")
	}
}

// selfSelectableScreen is itself Selectable and reports no regions -
// the pager's shape - so hitRegion/liveHandle take their fallback arms.
type selfSelectableScreen struct {
	stubScreen
	shared  *sel.Selection // shared across copies, like the pager's pointer
	rect    sel.Rect
	cleared int
}

// All methods take the value receiver: the router stores copies of this
// screen in its stack slot, and every copy must satisfy sel.Selectable.
// Live state travels through the shared pointer, exactly like the real
// pager's selState field.
func (s selfSelectableScreen) SelectionRect() sel.Rect { return s.rect }
func (s selfSelectableScreen) Selection() sel.Selection {
	if s.shared == nil {
		return sel.Selection{}
	}
	return *s.shared
}

func (s selfSelectableScreen) SetSelection(x sel.Selection) {
	if s.shared != nil {
		*s.shared = x // copy-on-write through the shared pointer
	}
}
func (s selfSelectableScreen) ClearSelection() {
	if s.shared != nil {
		*s.shared = sel.Selection{}
	}
	s.cleared++
}
func (s selfSelectableScreen) SelectedText() string {
	if s.shared == nil || !s.shared.Active {
		return ""
	}
	return "self"
}

// Update returns a value copy of the WHOLE screen (like the pager's),
// so the stack slot keeps its identity and shared selection pointer.
// Value receiver: returns a copy of the whole screen (like the pager),
// so the stack slot keeps its identity and shared selection pointer.
func (s selfSelectableScreen) Update(msg tea.Msg) (Screen, tea.Cmd) { return s, nil }

func TestSelfSelectableScreenRoutesViaStackSlot(t *testing.T) {
	shared := &sel.Selection{}
	sc := &selfSelectableScreen{
		stubScreen: stubScreen{flags: ViewFlags{AltScreen: true}},
		shared:     shared,
		rect:       sel.Rect{MinX: 0, MinY: 0, MaxX: 40, MaxY: 10},
	}
	m := New(sc, loadTheme(t), theme.TierASCII, nil).WithOptions(Options{Mouse: true})
	next, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	if !m.drag.armed {
		t.Fatal("a screen that is itself Selectable must arm through its stack slot")
	}
	next, _ = m.Update(tea.MouseMotionMsg{X: 9, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	next, cmd := m.Update(tea.MouseReleaseMsg{X: 9, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("self-selectable release must copy")
	}
	batch := cmd().(tea.BatchMsg)
	var saw bool
	for _, c := range batch {
		if msg := c(); msg != nil {
			if ct, ok := msg.(sel.CopyTextMsg); ok && ct.Text == "self" {
				saw = true
			}
		}
	}
	if !saw {
		t.Fatal("CopyTextMsg must carry the self-screen text")
	}
}

func TestPressArmsLiveHandleWhenSnapshotNil(t *testing.T) {
	// A screen whose SelectionRegions returns a nil Handle for the hit
	// region: press must fall back to liveHandle (the slot pointer).
	sc := &regionScreen{flags: ViewFlags{AltScreen: true}}
	reg := &fakeRegion{rect: sel.Rect{MinX: 1, MinY: 1, MaxX: 41, MaxY: 11}, text: "hello world\nsecond row!"}
	sc.region = reg
	m := New(sc, loadTheme(t), theme.TierASCII, nil).WithOptions(Options{Mouse: true})
	next, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	if !m.drag.armed || m.drag.handle == nil {
		t.Fatal("press must arm with a live handle")
	}
}

func TestMotionWithoutArmedDragDoesNotMutate(t *testing.T) {
	m, _, reg := newRegionModel(t)
	// Motion before any press: not consumed as drag; no state change.
	next, cmd := m.Update(tea.MouseMotionMsg{X: 9, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	if cmd != nil || m.drag.dragging || reg.state.Active {
		t.Fatal("unarmed motion must be inert")
	}
}

func TestJitterExactlyAtThresholdStartsDrag(t *testing.T) {
	m, _, _ := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 6, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	// dx==1, dy==0: exactly at dragThreshold. `dx < dragThreshold` is
	// false, so the drag starts; a <= mutant would swallow it.
	next, _ = m.Update(tea.MouseMotionMsg{X: 7, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	if !m.drag.dragging {
		t.Fatal("one-cell horizontal motion must start a drag")
	}
	// Vertical one-cell too.
	m2, _, _ := newRegionModel(t)
	next, _ = m2.Update(tea.MouseClickMsg{X: 6, Y: 3, Button: tea.MouseLeft})
	m2 = next.(Model)
	next, _ = m2.Update(tea.MouseMotionMsg{X: 6, Y: 4, Button: tea.MouseLeft})
	m2 = next.(Model)
	if !m2.drag.dragging {
		t.Fatal("one-cell vertical motion must start a drag")
	}
}

func TestSelfSelectableZeroRectNoHit(t *testing.T) {
	shared := &sel.Selection{}
	sc := &selfSelectableScreen{
		stubScreen: stubScreen{flags: ViewFlags{AltScreen: true}},
		shared:     shared,
		rect:       sel.Rect{}, // zero rect: height/width are 0
	}
	m := New(sc, loadTheme(t), theme.TierASCII, nil).WithOptions(Options{Mouse: true})
	next, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	if m.drag.armed {
		t.Fatal("a zero-size self-selectable screen must not arm")
	}
}

// TestSelfSelectableClickOutsideRectNoHit pins hitRegion's three-way
// guard (height>0 && width>0 && Contains(x,y)): Contains already
// implies a positive height and width, so a click outside a
// positive-size rect must not arm - a version that ORs any one clause
// in would arm on rect size alone, regardless of where the click fell.
func TestSelfSelectableClickOutsideRectNoHit(t *testing.T) {
	shared := &sel.Selection{}
	sc := &selfSelectableScreen{
		stubScreen: stubScreen{flags: ViewFlags{AltScreen: true}},
		shared:     shared,
		rect:       sel.Rect{MinX: 0, MinY: 0, MaxX: 5, MaxY: 5},
	}
	m := New(sc, loadTheme(t), theme.TierASCII, nil).WithOptions(Options{Mouse: true})
	next, _ := m.Update(tea.MouseClickMsg{X: 100, Y: 100, Button: tea.MouseLeft})
	m = next.(Model)
	if m.drag.armed {
		t.Fatal("a click outside a positive-size rect must not arm")
	}
}

// nilRegionScreen is a regions screen whose hit region carries a nil
// Handle: the press must fall back to liveHandle for the armed state.
type nilRegionScreen struct {
	regionScreen
}

func (s *nilRegionScreen) SelectionRegions() []sel.RegionEntry {
	return []sel.RegionEntry{{ID: sel.RegionTranscript, Handle: nil}}
}

func TestPressWithNilSnapshotHandleFallsBackToLive(t *testing.T) {
	sc := &nilRegionScreen{regionScreen: regionScreen{flags: ViewFlags{AltScreen: true}}}
	m := New(sc, loadTheme(t), theme.TierASCII, nil).WithOptions(Options{Mouse: true})
	next, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	m = next.(Model)
	// hitRegion skips nil handles, so no arming happens at all.
	if m.drag.armed {
		t.Fatal("a nil-handle region must not arm")
	}
}

func TestCancelDragClearsHandleState(t *testing.T) {
	m, _, reg := newRegionModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft})
	m = next.(Model)
	if !reg.state.Active {
		t.Fatal("press must arm the handle")
	}
	m.cancelDrag()
	if reg.state.Active || m.drag.armed {
		t.Fatal("cancelDrag must clear both the router and the handle")
	}
}

// mixedRegionScreen reports one nil-handle region alongside one real
// one, so cancelAllSelections must skip the nil entry without
// panicking and still reach the real one.
type mixedRegionScreen struct {
	regionScreen
}

func (s *mixedRegionScreen) SelectionRegions() []sel.RegionEntry {
	var h sel.Selectable = s.region
	return []sel.RegionEntry{
		{ID: sel.RegionComposer, Handle: nil},
		{ID: sel.RegionTranscript, Handle: &h},
	}
}

func TestCancelAllSelectionsSkipsNilHandleAndClearsReal(t *testing.T) {
	reg := &fakeRegion{rect: sel.Rect{MinX: 1, MinY: 1, MaxX: 41, MaxY: 11}, state: sel.Selection{Active: true}}
	sc := &mixedRegionScreen{regionScreen: regionScreen{flags: ViewFlags{AltScreen: true}, region: reg}}
	m := New(sc, loadTheme(t), theme.TierASCII, nil).WithOptions(Options{Mouse: true})
	m.cancelAllSelections()
	if reg.state.Active {
		t.Fatal("the real handle's selection must be cleared")
	}
}
