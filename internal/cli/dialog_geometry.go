package cli

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// rect is a terminal-cell rectangle. Coordinates are always relative to the
// raw terminal canvas; logical minimums never enlarge the canvas.
type rect struct {
	x, y int
	w, h int
}

type dialogPrefs struct {
	preferredW, preferredH       int
	preferredWPct, preferredHPct int
	minW, minH                   int
	maxWPct, maxHPct             int
	frameCols, frameRows         int
	borderless                   bool
	pager                        bool
}

type dialogLayout struct {
	rect                 rect
	innerW, pageH        int
	frameCols, frameRows int
	borderless           bool
}

func dialogRect(termW, termH int, p dialogPrefs, contentW, contentH int) rect {
	if termW <= 0 || termH <= 0 {
		return rect{}
	}
	w := preferredDimension(termW, p.preferredW, p.preferredWPct, contentW)
	h := preferredDimension(termH, p.preferredH, p.preferredHPct, contentH)
	if p.maxWPct > 0 {
		w = min(w, percentOf(termW, p.maxWPct))
	}
	if p.maxHPct > 0 {
		h = min(h, percentOf(termH, p.maxHPct))
	}
	w = max(w, p.minW)
	h = max(h, p.minH)
	w = min(termW, max(0, w))
	h = min(termH, max(0, h))
	return rect{x: (termW - w) / 2, y: (termH - h) / 2, w: w, h: h}
}

func makeDialogLayout(termW, termH int, p dialogPrefs, measure func(innerW int) (contentW, contentH int)) dialogLayout {
	if termW <= 0 || termH <= 0 {
		return dialogLayout{}
	}
	if p.frameCols == 0 && p.frameRows == 0 && !p.borderless {
		p.frameCols, p.frameRows = 4, 2
	}
	if measure == nil {
		measure = func(int) (int, int) { return 0, 0 }
	}
	measureW := max(1, termW-p.frameCols)
	if p.preferredW > 0 || p.preferredWPct > 0 || p.minW > 0 {
		probe := dialogRect(termW, termH, p, 0, 1)
		measureW = max(1, probe.w-p.frameCols)
	}
	contentW, contentH := measure(measureW)
	r := dialogRect(termW, termH, p, contentW+p.frameCols, contentH+p.frameRows)
	if r.w < p.frameCols+8 || r.h < p.frameRows+2 || p.borderless {
		p.borderless = true
		r = rect{w: termW, h: termH}
	}
	frameCols, frameRows := p.frameCols, p.frameRows
	if p.borderless {
		frameCols, frameRows = 0, 0
	}
	return dialogLayout{
		rect: r, innerW: max(0, r.w-frameCols), pageH: max(0, r.h-frameRows),
		frameCols: frameCols, frameRows: frameRows, borderless: p.borderless,
	}
}

func preferredDimension(term, preferred, pct, content int) int {
	switch {
	case preferred > 0:
		return preferred
	case pct > 0:
		return percentOf(term, pct)
	default:
		return max(0, content)
	}
}

func percentOf(n, pct int) int {
	if n <= 0 || pct <= 0 {
		return 0
	}
	return n * pct / 100
}

// wrapDisplayRows converts semantic lines into the exact rows a pager renders.
// ansi.Cut is grapheme-aware and keeps ANSI sequences intact.
func wrapDisplayRows(lines []string, innerW int) []string {
	rows, _ := wrapDisplayRowsWithSources(lines, innerW)
	return rows
}

func wrapDisplayRowsWithSources(lines []string, innerW int) ([]string, []int) {
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
