package cli

import (
	"fmt"
	"strings"
	"testing"
)

// overlayWindowRows builds n synthetic rows ("item-00" … "item-19") for the
// shared windowed-overlay renderer.
func overlayWindowRows(n int) []string {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = fmt.Sprintf("item-%02d", i)
	}
	return rows
}

// ---------------------------------------------------------------------------
// Wave 5 (RED): renderOverlayWindow — the shared windowed selection popup.
//
// Contract being locked in:
//   - A windowed popup shows at most windowRows items with the selected row
//     prefixed by the '›' marker and a "+N more" footer for the remainder.
//   - The window scrolls so the selection stays visible; when the window ends
//     exactly at the last row there is no "+N more" footer.
//   - A terminal too short for a framed popup falls back to a single row.
//   - Nil rows render nothing.
// ---------------------------------------------------------------------------

// TestOverlayWindowThreeVisible: 20 rows, selection at the top, 3-row window.
func TestOverlayWindowThreeVisible(t *testing.T) {
	panel, _ := RenderOverlayWindow(overlayWindowRows(20), 0, 3, 80, 10, " x (20) ", "")
	if panel == "" {
		t.Fatalf("renderOverlayWindow: expected a non-empty panel, got %q", panel)
	}
	plain := stripANSI(panel)
	if !strings.Contains(plain, "› item-00") {
		t.Fatalf("renderOverlayWindow: selected row must carry the '›' marker, panel:\n%s", plain)
	}
	if !strings.Contains(plain, "+17 more") {
		t.Fatalf("renderOverlayWindow: expected +17 more footer for 20 rows / 3 visible, panel:\n%s", plain)
	}
	if !strings.Contains(plain, " x (20) ") {
		t.Fatalf("renderOverlayWindow: title missing from the frame, panel:\n%s", plain)
	}
}

// TestOverlayWindowScrollsSelectionIntoView: selection at the last row must
// scroll the window to the end, so the selected row is visible and there is
// no "+N more" remainder.
func TestOverlayWindowScrollsSelectionIntoView(t *testing.T) {
	panel, _ := RenderOverlayWindow(overlayWindowRows(20), 19, 3, 80, 10, " x (20) ", "")
	if panel == "" {
		t.Fatalf("renderOverlayWindow: expected a non-empty panel, got %q", panel)
	}
	plain := stripANSI(panel)
	if !strings.Contains(plain, "item-19") {
		t.Fatalf("renderOverlayWindow: last (selected) row must be visible, panel:\n%s", plain)
	}
	if strings.Contains(plain, "+ more") {
		t.Fatalf("renderOverlayWindow: window ending at the last row must not print a +N more footer, panel:\n%s", plain)
	}
}

// TestOverlayWindowShortTerminalFallback: a 1-row terminal cannot hold a
// frame; the renderer must fall back to a single row showing the selection.
func TestOverlayWindowShortTerminalFallback(t *testing.T) {
	panel, r := RenderOverlayWindow(overlayWindowRows(5), 0, 3, 80, 1, " x (5) ", "")
	if panel == "" {
		t.Fatalf("renderOverlayWindow: expected a fallback row even on a 1-row terminal, got %q", panel)
	}
	if strings.Contains(panel, "\n") {
		t.Fatalf("renderOverlayWindow: short-terminal fallback must be a single row, got:\n%s", panel)
	}
	if r.H != 1 {
		t.Fatalf("renderOverlayWindow: expected Rect h == 1 on a 1-row terminal, got %+v", r)
	}
	if !strings.Contains(stripANSI(panel), "item-00") {
		t.Fatalf("renderOverlayWindow: fallback must show the selected row, panel:\n%s", stripANSI(panel))
	}
}

// TestOverlayWindowEmpty: nil rows render nothing.
func TestOverlayWindowEmpty(t *testing.T) {
	panel, r := RenderOverlayWindow(nil, 0, 3, 80, 10, " x (0) ", "")
	if panel != "" {
		t.Fatalf("renderOverlayWindow: expected an empty panel for nil rows, got %q", panel)
	}
	if r != (Rect{}) {
		t.Fatalf("renderOverlayWindow: expected a zero Rect for nil rows, got %+v", r)
	}
}

// ---------------------------------------------------------------------------
// Wave 5 (RED): renderHistoryPanel — the composer message-history picker.
//
// Contract being locked in:
//   - The picker frames the newest-first entries with a " history (N) "
//     title, a '›' marker on the selected entry, a "+N more" footer, and an
//     "enter recall" hint.
//   - Entries are sanitized: raw escape sequences and newlines must not leak
//     into a row (newlines render as '⏎').
//   - The selected entry follows state.selected and stays visible.
//   - Nil entries render nothing.
// ---------------------------------------------------------------------------

// TestHistoryOverlayRenderPanelThreeRows: 10 entries, newest selected.
func TestHistoryOverlayRenderPanelThreeRows(t *testing.T) {
	entries := overlayWindowRows(10) // item-00 … item-09, newest first
	panel, _ := renderHistoryPanel(HistoryState{Open: true, Selected: 0}, entries, 80, 10)
	if panel == "" {
		t.Fatalf("renderHistoryPanel: expected a non-empty panel, got %q", panel)
	}
	plain := stripANSI(panel)
	if !strings.Contains(plain, " history (10) ") {
		t.Fatalf("renderHistoryPanel: title missing from the frame, panel:\n%s", plain)
	}
	if !strings.Contains(plain, "› item-00") {
		t.Fatalf("renderHistoryPanel: newest (selected) entry must carry the '›' marker, panel:\n%s", plain)
	}
	if !strings.Contains(plain, "+7 more") {
		t.Fatalf("renderHistoryPanel: expected +7 more footer for 10 entries / 3 visible, panel:\n%s", plain)
	}
}

// TestHistoryOverlayRenderSanitizes: raw escape sequences and newlines inside
// entries must not leak into the rendered rows. The escape assertion checks
// the UN-normalized panel for the injected CSI sequence: the frame's own SGR
// resets (\\x1b[0m) are styling, but the user-content sequence must be gone.
// stripANSI would strip CSI itself and make the check vacuous, so the raw
// panel is what a non-sanitizing renderer must fail on.
func TestHistoryOverlayRenderSanitizes(t *testing.T) {
	entries := []string{"hi\x1b[2Jclear", "a\nb\nc"}
	panel, _ := renderHistoryPanel(HistoryState{Open: true, Selected: 0}, entries, 80, 10)
	if panel == "" {
		t.Fatalf("renderHistoryPanel: expected a non-empty panel, got %q", panel)
	}
	if strings.Contains(panel, "\x1b[2J") {
		t.Fatalf("renderHistoryPanel: raw CSI escape must be stripped from entries, panel:\n%q", panel)
	}
	if !strings.Contains(stripANSI(panel), "hiclear") {
		t.Fatalf("renderHistoryPanel: sanitized content must survive, panel:\n%q", panel)
	}
	plain := stripANSI(panel)
	if strings.Contains(plain, "a\nb") {
		t.Fatalf("renderHistoryPanel: raw newline inside a row must be replaced, panel:\n%q", plain)
	}
	if !strings.Contains(plain, "⏎") {
		t.Fatalf("renderHistoryPanel: multi-line entry must render as one row with '⏎', panel:\n%q", plain)
	}
}

// TestHistoryOverlayRenderFooterHint: the picker advertises the recall action.
func TestHistoryOverlayRenderFooterHint(t *testing.T) {
	panel, _ := renderHistoryPanel(HistoryState{Open: true, Selected: 0}, []string{"one", "two"}, 80, 10)
	if panel == "" {
		t.Fatalf("renderHistoryPanel: expected a non-empty panel, got %q", panel)
	}
	if !strings.Contains(stripANSI(panel), "enter recall") {
		t.Fatalf("renderHistoryPanel: 'enter recall' hint missing from the footer, panel:\n%s", stripANSI(panel))
	}
}

// TestHistoryOverlayRenderEmpty: nil entries render nothing.
func TestHistoryOverlayRenderEmpty(t *testing.T) {
	panel, r := renderHistoryPanel(HistoryState{Open: true, Selected: 0}, nil, 80, 10)
	if panel != "" {
		t.Fatalf("renderHistoryPanel: expected an empty panel for nil entries, got %q", panel)
	}
	if r != (Rect{}) {
		t.Fatalf("renderHistoryPanel: expected a zero Rect for nil entries, got %+v", r)
	}
}

// TestHistoryOverlayRenderSelectedFollowsState: the picker honors
// state.selected and keeps the selected (here: last) entry visible.
func TestHistoryOverlayRenderSelectedFollowsState(t *testing.T) {
	entries := overlayWindowRows(10)
	panel, _ := renderHistoryPanel(HistoryState{Open: true, Selected: 9}, entries, 80, 10)
	if panel == "" {
		t.Fatalf("renderHistoryPanel: expected a non-empty panel, got %q", panel)
	}
	if !strings.Contains(stripANSI(panel), "item-09") {
		t.Fatalf("renderHistoryPanel: selected (last) entry must be visible, panel:\n%s", stripANSI(panel))
	}
}
