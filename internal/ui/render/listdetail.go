package render

import "strings"

// SplitListDetail lays out a scrollable row list above an optional
// collapsible detail preview, sharing one height budget, with a pinned
// notice line at the bottom when present. It is the shared geometry the
// settings screen's grouped sections need (docs/design/settings-screen.md
// section 1): a header/row list on top, window-scrolled to keep the
// selection visible, and a preview of the selected row beneath it,
// extracted from the Skills section so the Agents section (and any
// future grouped section) can present the same layout without
// re-deriving the same height math.
//
// listLines is every list row already rendered (including any group
// headers) in display order. detailLines is the selected item's
// pre-rendered preview body, or nil when nothing is selected or the
// section has no preview to show for the current row. selectedLine is
// the listLines index the window must keep visible; pass a negative
// value when the list is empty. height is the section's full available
// row budget. notice, when non-empty, must already be styled by the
// caller (this package has no theme access here) and consumes its own
// trailing row, taking priority over detail rows the same way the
// section's own notice line always has.
func SplitListDetail(listLines, detailLines []string, selectedLine, height int, notice string) string {
	avail := height
	if notice != "" && avail > 1 {
		avail--
	}

	detailHeight := 0
	if len(detailLines) > 0 && avail > 4 {
		needed := len(listLines)
		if needed > avail-4 {
			needed = avail - 4
		}
		if needed < 2 {
			needed = 2
		}
		detailHeight = avail - needed
		if detailHeight > len(detailLines)+1 {
			detailHeight = len(detailLines) + 1
		}
	}
	listHeight := avail - detailHeight
	if listHeight < 1 {
		listHeight = 1
	}

	targetCursorLine := selectedLine
	if targetCursorLine < 0 {
		targetCursorLine = 0
	}
	start, end := WindowSlice(len(listLines), targetCursorLine, listHeight)

	var b []byte
	for _, line := range listLines[start:end] {
		b = append(b, line...)
		b = append(b, '\n')
	}
	if detailHeight > 0 && len(detailLines) > 0 {
		b = append(b, '\n')
		maxDetailLines := detailHeight - 1
		if maxDetailLines > len(detailLines) {
			maxDetailLines = len(detailLines)
		}
		for _, dl := range detailLines[:maxDetailLines] {
			b = append(b, dl...)
			b = append(b, '\n')
		}
	}
	if notice != "" {
		b = append(b, notice...)
	}
	return strings.TrimRight(string(b), "\n")
}
