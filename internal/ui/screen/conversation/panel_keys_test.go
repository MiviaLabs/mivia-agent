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
