package conversation

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// overlayComposerPopup draws the composer's completion popup over the rows
// directly above the bar. It is an overlay, not a block in the flow: no
// row is reserved for it, so opening the menu never reflows the transcript
// or moves the bar (ux-rules.md rules 2.7, 2.8). The popup's item rows come
// first and its footer sits against the bar; when the rows above the bar
// are fewer than the popup's, the popup is clipped from the top.
//
// lines is the frame before the gutter is applied: the composer block is
// the last rows above the status row, so its first row is found from the
// end. In the split layout the popup spans only the bar's padded width,
// which is the chat column, so it never reaches the sidebar.
func (s Screen) overlayComposerPopup(lines []string) []string {
	if s.hideComposer {
		return lines
	}
	pop := s.composer.Popup()
	if len(pop) == 0 {
		return lines
	}
	top := len(lines) - 1 - s.composer.Height() // the bar's first row
	x := s.composer.PopupOffset()
	for i := len(pop) - 1; i >= 0; i-- {
		y := top - (len(pop) - i)
		if y < 0 {
			break
		}
		lines[y] = spliceRow(lines[y], x, pop[i])
	}
	return lines
}

// spliceRow lays overlay over base starting at column x, keeping base's
// cells on either side. The seams carry a reset so a style open in base
// never bleeds into the overlay or back out of it.
func spliceRow(base string, x int, overlay string) string {
	w := ansi.StringWidth(overlay)
	bw := ansi.StringWidth(base)
	left := ansi.Cut(base, 0, x)
	if lw := ansi.StringWidth(left); lw < x {
		left += strings.Repeat(" ", x-lw)
	}
	right := ""
	if bw > x+w {
		right = ansi.Cut(base, x+w, bw)
	}
	return left + "\x1b[m" + overlay + "\x1b[m" + right
}
