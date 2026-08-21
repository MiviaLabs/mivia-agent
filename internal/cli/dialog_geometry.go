package cli

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Rect is a terminal-cell rectangle. Coordinates are always relative to the
// raw terminal canvas; logical minimums never enlarge the canvas. Shared
// with the classic-mode overlay/panel renderers in internal/clichat.
type Rect struct {
	X, Y int
	W, H int
}

// DialogPrefs configures dialog sizing (preferred/min/max dimensions, frame
// padding, pager footer). Shared with dialog construction in
// internal/clichat.
type DialogPrefs struct {
	// PreferredW is the preferred width in cells (0 = unset).
	PreferredW, preferredH int
	// PreferredWPct is the preferred width as a percentage of the terminal.
	// PreferredHPct is the preferred height as a percentage of the terminal.
	PreferredWPct, PreferredHPct int
	// MinW / MinH are the minimum width/height in cells.
	MinW, MinH       int
	maxWPct, maxHPct int
	// FrameCols / FrameRows are the frame's column/row padding.
	FrameCols, FrameRows int
	borderless           bool
	// Pager shows a page-position footer when true.
	Pager bool
}

// DialogLayout is the resolved geometry a dialog renders into: outer Rect,
// inner content dimensions, and frame padding. Shared with dialog
// construction and rendering in internal/clichat.
type DialogLayout struct {
	Rect Rect
	// InnerW / PageH are the inner content width/height, after frame padding.
	InnerW, PageH int
	// FrameCols / FrameRows are the frame's column/row padding.
	FrameCols, FrameRows int
	borderless           bool
}

func dialogRect(termW, termH int, p DialogPrefs, contentW, contentH int) Rect {
	if termW <= 0 || termH <= 0 {
		return Rect{}
	}
	w := preferredDimension(termW, p.PreferredW, p.PreferredWPct, contentW)
	h := preferredDimension(termH, p.preferredH, p.PreferredHPct, contentH)
	if p.maxWPct > 0 {
		w = Min(w, percentOf(termW, p.maxWPct))
	}
	if p.maxHPct > 0 {
		h = Min(h, percentOf(termH, p.maxHPct))
	}
	w = Max(w, p.MinW)
	h = Max(h, p.MinH)
	w = Min(termW, Max(0, w))
	h = Min(termH, Max(0, h))
	return Rect{X: (termW - w) / 2, Y: (termH - h) / 2, W: w, H: h}
}

// MakeDialogLayout resolves a dialog's geometry from terminal size and
// preferences, measuring content via measure. Shared with dialog
// construction in internal/clichat.
func MakeDialogLayout(termW, termH int, p DialogPrefs, measure func(innerW int) (contentW, contentH int)) DialogLayout {
	if termW <= 0 || termH <= 0 {
		return DialogLayout{}
	}
	if p.FrameCols == 0 && p.FrameRows == 0 && !p.borderless {
		p.FrameCols, p.FrameRows = 4, 2
	}
	if measure == nil {
		measure = func(int) (int, int) { return 0, 0 }
	}
	measureW := Max(1, termW-p.FrameCols)
	if p.PreferredW > 0 || p.PreferredWPct > 0 || p.MinW > 0 {
		probe := dialogRect(termW, termH, p, 0, 1)
		measureW = Max(1, probe.W-p.FrameCols)
	}
	contentW, contentH := measure(measureW)
	r := dialogRect(termW, termH, p, contentW+p.FrameCols, contentH+p.FrameRows)
	if r.W < p.FrameCols+8 || r.H < p.FrameRows+2 || p.borderless {
		p.borderless = true
		r = Rect{W: termW, H: termH}
	}
	frameCols, frameRows := p.FrameCols, p.FrameRows
	if p.borderless {
		frameCols, frameRows = 0, 0
	}
	return DialogLayout{
		Rect: r, InnerW: Max(0, r.W-frameCols), PageH: Max(0, r.H-frameRows),
		FrameCols: frameCols, FrameRows: frameRows, borderless: p.borderless,
	}
}

func preferredDimension(term, preferred, pct, content int) int {
	switch {
	case preferred > 0:
		return preferred
	case pct > 0:
		return percentOf(term, pct)
	default:
		return Max(0, content)
	}
}

func percentOf(n, pct int) int {
	if n <= 0 || pct <= 0 {
		return 0
	}
	return n * pct / 100
}

// WrapDisplayRows converts semantic lines into the exact rows a pager renders.
// ansi.Cut is grapheme-aware and keeps ANSI sequences intact. Shared with
// dialog rendering in internal/clichat.
func WrapDisplayRows(lines []string, innerW int) []string {
	rows, _ := WrapDisplayRowsWithSources(lines, innerW)
	return rows
}

// WrapDisplayRowsWithSources is WrapDisplayRows, also returning each output
// row's source line index. WrapDisplayRows is the sole in-package caller;
// exported (rather than relocated to its one external caller) so both stay
// colocated with the wrapping logic they share.
func WrapDisplayRowsWithSources(lines []string, innerW int) ([]string, []int) {
	if innerW <= 0 {
		return nil, nil
	}
	rows := make([]string, 0, len(lines))
	sources := make([]int, 0, len(lines))
	for source, line := range lines {
		if ansi.StringWidth(line) <= innerW {
			rows = append(rows, line)
			sources = append(sources, source)
			continue
		}
		pos := 0
		width := ansi.StringWidth(line)
		for pos < width {
			part := sliceANSI(line, pos, pos+innerW)
			partWidth := ansi.StringWidth(part)
			if partWidth == 0 {
				part = sliceANSI(line, pos, pos+1)
				partWidth = ansi.StringWidth(part)
			}
			if partWidth == 0 {
				part = sliceANSI(line, pos, pos+2)
				partWidth = ansi.StringWidth(part)
				if partWidth == 0 {
					break
				}
			}
			rows = append(rows, part)
			sources = append(sources, source)
			pos += partWidth
		}
		if len(rows) == 0 || (len(rows) > 0 && ansi.StringWidth(rows[len(rows)-1]) == 0) {
			rows = append(rows, "")
			sources = append(sources, source)
		}
	}
	if len(rows) == 0 && len(lines) > 0 {
		return []string{""}, []int{0}
	}
	return rows, sources
}

func joinDisplayRows(rows []string) string { return strings.Join(rows, "\n") }
