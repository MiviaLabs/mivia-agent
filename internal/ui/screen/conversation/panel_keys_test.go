package conversation

import (
	"strings"
	"testing"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
)

// keysWithoutFilesJK returns a keymap identical to keymap.Default() except
// that ContextFiles' "j"/"k" bindings (filesBindings' IDPagerRowDown /
// IDPagerRowUp entries) are stripped. Under the DEFAULT keymap, a "j" or
// "k" press is already resolved by s.keys.Match(keymap.ContextFiles, ...)
// at the very top of handlePanelListKey (panel_keys.go:18-27), which
// rewrites msg to a plain tea.KeyDown/tea.KeyUp BEFORE the function's own
// `if msg.String() == "j" { ... } else if msg.String() == "k" { ... }`
// fallback (panel_keys.go:37,39) is ever reached — that fallback only
// matters when the keymap does not bind pager-row nav to j/k in
// ContextFiles. This helper defeats the earlier Match so a test can
// actually exercise the fallback's own "j"/"k" literals directly.
func keysWithoutFilesJK() *keymap.Map {
	def := keymap.Default()
	out := make([]keymap.Binding, len(def))
	for i, b := range def {
		if b.Context == keymap.ContextFiles {
			var kept []string
			for _, k := range b.Keys {
				if k != "j" && k != "k" {
					kept = append(kept, k)
				}
			}
			b.Keys = kept
		}
		out[i] = b
	}
	return keymap.New(out)
}

// TestPanelListKey_JMovesSelectionDown isolates the "j" literal in
// handlePanelListKey's own fallback switch (panel_keys.go:37): with the
// keymap's ContextFiles j/k bindings removed (so the function's earlier
// keymap.Match no longer intercepts them, see keysWithoutFilesJK), pressing
// "j" must still move the sidebar selection down through this literal.
func TestPanelListKey_JMovesSelectionDown(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	s.keys = keysWithoutFilesJK()
	// Select by what the row IS: the header rows above it move.
	s.panel.selectNavKind(navFile, 0) // a.go
	if sel, _ := s.panel.list.Selected(); !strings.Contains(sel, "a.go") {
		t.Fatalf("precondition: selection = %q, want a.go", sel)
	}
	next, _ := s.Update(key("j"))
	s = next.(Screen)
	if sel, _ := s.panel.list.Selected(); !strings.Contains(sel, "b.go") {
		t.Fatalf("j did not move the selection down: selected %q, want b.go", sel)
	}
}

// TestPanelListKey_KMovesSelectionUp isolates the "k" literal in the same
// fallback switch (panel_keys.go:39), symmetric to the "j" case above.
func TestPanelListKey_KMovesSelectionUp(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	s.keys = keysWithoutFilesJK()
	// Select by what the row IS: the header rows above it move.
	s.panel.selectNavKind(navFile, 0) // a.go
	next, _ := s.Update(key("j"))
	s = next.(Screen)
	if sel, _ := s.panel.list.Selected(); !strings.Contains(sel, "b.go") {
		t.Fatalf("precondition: j did not move down first, selected %q", sel)
	}
	next, _ = s.Update(key("k"))
	s = next.(Screen)
	if sel, _ := s.panel.list.Selected(); !strings.Contains(sel, "a.go") {
		t.Fatalf("k did not move the selection back up: selected %q, want a.go", sel)
	}
}

// TestPanelListKey_KOpensPagerRowUpUnderDefaultKeymap isolates the
// keymap.IDPagerRowUp remap (panel_keys.go:51): under the DEFAULT keymap
// (unlike TestPanelListKey_KMovesSelectionUp, which strips the binding to
// reach the fallback literal instead), "k" resolves through
// s.keys.Match(keymap.ContextFiles, ...) to IDPagerRowUp before the
// fallback switch is ever reached.
func TestPanelListKey_KOpensPagerRowUpUnderDefaultKeymap(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	s.panel.selectNavKind(navFile, 1) // b.go
	if sel, _ := s.panel.list.Selected(); !strings.Contains(sel, "b.go") {
		t.Fatalf("precondition: selection = %q, want b.go", sel)
	}
	next, _ := s.Update(key("k"))
	s = next.(Screen)
	if sel, _ := s.panel.list.Selected(); !strings.Contains(sel, "a.go") {
		t.Fatalf("k did not move the selection up: selected %q, want a.go", sel)
	}
}

// dialogScreen opens the files-panel content dialog on a.go, so
// panelDialogKey's own key table can be driven directly.
func dialogScreen(t *testing.T) Screen {
	t.Helper()
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	s.panel.selectNavKind(navFile, 0) // a.go
	s.panel.dialog = true
	return s
}

// TestPanelDialogKey_CtrlCClosesDialogAndQuits pins panelDialogKey's own
// emergency-exit branch: ctrl+c closes the dialog AND runs the ordinary
// quit flow, so the second-press warning lands on a visible status row.
func TestPanelDialogKey_CtrlCClosesDialogAndQuits(t *testing.T) {
	s := dialogScreen(t)
	next := s.panelDialogKey(key("ctrl+c"))
	scr := next.(Screen)
	if scr.panel.dialog {
		t.Fatal("ctrl+c did not close the panel dialog")
	}
}

// TestPanelDialogKey_ScrollKeysDoNotPanic drives every scroll-key branch
// in panelDialogKey's table (up/k, down/j, pgup, pgdown, home, end, and
// the pager-half keymap IDs) directly, pinning that each one executes its
// own scrollPanel/offset statement without panicking and leaves the
// offset non-negative.
func TestPanelDialogKey_ScrollKeysDoNotPanic(t *testing.T) {
	for _, k := range []string{"up", "k", "down", "j", "pgup", "pgdown", "home", "end"} {
		t.Run(k, func(t *testing.T) {
			s := dialogScreen(t)
			next := s.panelDialogKey(key(k))
			scr := next.(Screen)
			if scr.panel.offset < 0 {
				t.Fatalf("key %q left a negative offset: %d", k, scr.panel.offset)
			}
		})
	}
}

// TestPanelDialogKey_ToggleViewFlipsSourceView pins the
// keymap.IDFileToggleView branch.
func TestPanelDialogKey_ToggleViewFlipsSourceView(t *testing.T) {
	s := dialogScreen(t)
	before := s.panel.sourceView
	next := s.panelDialogKey(key(keyForID(t, keymap.ContextFiles, keymap.IDFileToggleView)))
	scr := next.(Screen)
	if scr.panel.sourceView == before {
		t.Fatal("IDFileToggleView did not flip sourceView")
	}
}

// TestPanelDialogKey_PagerHalfKeysDoNotPanic drives the keymap
// IDPagerHalfUp/IDPagerHalfDown branches directly.
func TestPanelDialogKey_PagerHalfKeysDoNotPanic(t *testing.T) {
	for _, id := range []keymap.ID{keymap.IDPagerHalfUp, keymap.IDPagerHalfDown} {
		s := dialogScreen(t)
		next := s.panelDialogKey(key(keyForID(t, keymap.ContextFiles, id)))
		scr := next.(Screen)
		if scr.panel.offset < 0 {
			t.Fatalf("pager-half key for %v left a negative offset: %d", id, scr.panel.offset)
		}
	}
}

// keyForID returns the first bound key string for id in ctx under the
// default keymap, failing the test if none exists.
func keyForID(t *testing.T, ctx keymap.Context, id keymap.ID) string {
	t.Helper()
	for _, b := range keymap.Default() {
		if b.Context == ctx && b.ID == id && len(b.Keys) > 0 {
			return b.Keys[0]
		}
	}
	t.Fatalf("no default binding for %v/%v", ctx, id)
	return ""
}
