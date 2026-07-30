// Table rendering for the markdown writer: box chrome, per-cell measurement
// and padding.
//
// Cells are FORMATTED BEFORE MEASURING. Column widths and padding must use
// the rendered width, not the markdown source: "**bold**" is eight source
// columns but renders as four, so measuring the source padded every styled
// cell short and the box borders never lined up.
package cli

import (
	"strings"
	"unicode/utf8"
)

// flushTable renders buffered table lines and clears the buffer.
//
// A buffer without a GFM separator row is not a table — it is prose that
// happened to contain a pipe (outer pipes are optional in GFM, so the
// separator is the only reliable signal). Those lines are emitted as-is.
//
// A real table gets full box chrome: top border, bold header, header rule,
// body rows, bottom border. Previously the separator was dropped and nothing
// replaced it, so the header was indistinguishable from the data.
func (mw *MarkdownWriter) flushTable() string {
	raw := mw.tableBuf
	mw.tableBuf = nil
	if len(raw) == 0 {
		return ""
	}
	hasSeparator := false
	for _, line := range raw {
		if isSeparatorLine(line) {
			hasSeparator = true
			break
		}
	}
	if !hasSeparator {
		var out []string
		for _, line := range raw {
			if f := mw.formatLine(line); f != "" {
				out = append(out, f)
			}
		}
		return strings.Join(out, "\n")
	}

	rows, aligns, maxCols := parseTableBuffer(raw)
	if len(rows) == 0 || maxCols == 0 {
		return ""
	}
	aligns = normalizeTableAligns(aligns, maxCols)
	normalizeTableRows(rows, maxCols)

	// Cells are formatted BEFORE measuring. Column widths and padding must
	// use the rendered width, not the markdown source: "**bold**" is 8
	// source columns but renders as 4, so measuring the source padded every
	// styled cell short and the box borders never lined up.
	formatted := make([][]string, len(rows))
	for ri, row := range rows {
		formatted[ri] = make([]string, len(row))
		for ci, cell := range row {
			formatted[ri][ci] = mw.formatInline(cell)
		}
	}
	widths := tableColumnWidths(formatted, maxCols)
	shrinkTableWidths(widths, mw.width)

	return renderTableBox(formatted, widths, aligns)
}

// renderTableBox draws the framed table: top border, rows (each wrapped to
// its own height), a rule between every row, and the bottom border.
func renderTableBox(formatted [][]string, widths []int, aligns []tableAlign) string {
	var b strings.Builder
	b.WriteString(tableBorderRow(widths, "┌", "┬", "┐"))
	for ri, row := range formatted {
		for _, visual := range wrapTableRow(row, widths) {
			b.WriteByte('\n')
			b.WriteString(formatAlignedTableRow(visual, widths, aligns, ri == 0))
		}
		// Every row is separated, not just the header: without inner rules a
		// wrapped multi-line row is indistinguishable from two short rows.
		if ri < len(formatted)-1 {
			b.WriteByte('\n')
			b.WriteString(tableBorderRow(widths, "├", "┼", "┤"))
		}
	}
	b.WriteByte('\n')
	b.WriteString(tableBorderRow(widths, "└", "┴", "┘"))
	return b.String()
}

// wrapTableRow wraps each cell to its column width and returns the row's
// visual lines — the row grows to the tallest cell, so long prose is carried
// in full instead of being cut with an ellipsis.
func wrapTableRow(row []string, widths []int) [][]string {
	cellLines := make([][]string, len(widths))
	height := 1
	for ci := range widths {
		cell := ""
		if ci < len(row) {
			cell = row[ci]
		}
		cellLines[ci] = wrapCellANSI(cell, widths[ci])
		if len(cellLines[ci]) > height {
			height = len(cellLines[ci])
		}
	}
	out := make([][]string, height)
	for li := 0; li < height; li++ {
		visual := make([]string, len(widths))
		for ci := range widths {
			if li < len(cellLines[ci]) {
				visual[ci] = cellLines[ci][li]
			}
		}
		out[li] = visual
	}
	return out
}

// wrapCellANSI word-wraps a styled string to a visible width, carrying active
// escape sequences across line breaks so styling neither bleeds past a line
// nor is lost on the next one.
func wrapCellANSI(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	if s == "" {
		return []string{""}
	}
	var lines []string
	var cur strings.Builder
	var active []string // style codes in effect at the current position
	curW := 0

	flush := func() {
		if curW == 0 && cur.Len() == 0 {
			return
		}
		line := cur.String()
		if len(active) > 0 {
			line += ansiReset
		}
		lines = append(lines, line)
		cur.Reset()
		curW = 0
		for _, code := range active {
			cur.WriteString(code)
		}
	}

	for _, word := range splitWordsANSI(s) {
		ww := visibleWidth(word.text)
		if curW > 0 && curW+1+ww > width {
			flush()
		} else if curW > 0 {
			cur.WriteString(" ")
			curW++
		}
		// A single word wider than the column is hard-split rather than
		// pushed past the border.
		for ww > width {
			head := truncateANSIHard(word.text, width-curW)
			cur.WriteString(head)
			active = trackStyles(active, head)
			flush()
			word.text = strings.TrimPrefix(word.text, head)
			ww = visibleWidth(word.text)
		}
		cur.WriteString(word.text)
		active = trackStyles(active, word.text)
		curW += ww
	}
	if cur.Len() > 0 || len(lines) == 0 {
		line := cur.String()
		if len(active) > 0 {
			line += ansiReset
		}
		lines = append(lines, line)
	}
	return lines
}

type ansiWord struct{ text string }

// splitWordsANSI splits on spaces while keeping escape sequences attached to
// the word that follows them.
func splitWordsANSI(s string) []ansiWord {
	var words []ansiWord
	var cur strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\033' {
			start := i
			i++
			for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
				i++
			}
			if i < len(s) {
				i++
			}
			cur.WriteString(s[start:i])
			continue
		}
		if s[i] == ' ' {
			if cur.Len() > 0 {
				words = append(words, ansiWord{cur.String()})
				cur.Reset()
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		_ = r
		cur.WriteString(s[i : i+size])
		i += size
	}
	if cur.Len() > 0 {
		words = append(words, ansiWord{cur.String()})
	}
	return words
}

// trackStyles folds the escape sequences in chunk into the active set.
// A reset clears it; anything else accumulates.
func trackStyles(active []string, chunk string) []string {
	for i := 0; i < len(chunk); {
		if chunk[i] != '\033' {
			i++
			continue
		}
		start := i
		i++
		for i < len(chunk) && !((chunk[i] >= 'A' && chunk[i] <= 'Z') || (chunk[i] >= 'a' && chunk[i] <= 'z')) {
			i++
		}
		if i < len(chunk) {
			i++
		}
		code := chunk[start:i]
		if code == ansiReset {
			active = active[:0]
			continue
		}
		active = append(active, code)
	}
	return active
}

// truncateANSIHard cuts to a visible width with no ellipsis (used when a
// single unbreakable word must be split across lines).
func truncateANSIHard(s string, max int) string {
	if max <= 0 {
		return ""
	}
	var b strings.Builder
	w := 0
	for i := 0; i < len(s); {
		if s[i] == '\033' {
			start := i
			i++
			for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
				i++
			}
			if i < len(s) {
				i++
			}
			b.WriteString(s[start:i])
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := 1
		if isWideRune(r) {
			rw = 2
		}
		if w+rw > max {
			break
		}
		b.WriteString(s[i : i+size])
		w += rw
		i += size
	}
	return b.String()
}

// tableBorderRow draws a horizontal rule with the given corner/junction runes.
func tableBorderRow(widths []int, left, mid, right string) string {
	var b strings.Builder
	b.WriteString("  ")
	b.WriteString(ansiDim)
	b.WriteString(left)
	for i, w := range widths {
		if i > 0 {
			b.WriteString(mid)
		}
		b.WriteString(strings.Repeat("─", w+2))
	}
	b.WriteString(right)
	b.WriteString(ansiReset)
	return b.String()
}

// formatAlignedTableRow renders one row of already-formatted cells with dim
// borders and padding. Cells arrive rendered so every measurement here is of
// visible width — the only width the terminal actually shows.
func formatAlignedTableRow(cells []string, widths []int, aligns []tableAlign, header bool) string {
	var b strings.Builder
	b.WriteString("  ")
	b.WriteString(ansiDim)
	b.WriteString("│")
	b.WriteString(ansiReset)
	for i := 0; i < len(widths); i++ {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		if visibleWidth(cell) > widths[i] {
			cell = truncateANSI(cell, widths[i])
		}
		if header && cell != "" {
			cell = ansiBold + cell + ansiReset
		}
		b.WriteString(" ")
		b.WriteString(padVisible(cell, widths[i], aligns[i]))
		b.WriteString(" ")
		b.WriteString(ansiDim)
		b.WriteString("│")
		b.WriteString(ansiReset)
	}
	return b.String()
}

// padVisible pads a possibly-styled string to a visible width.
func padVisible(s string, width int, align tableAlign) string {
	pad := width - visibleWidth(s)
	if pad <= 0 {
		return s
	}
	switch align {
	case alignRight:
		return strings.Repeat(" ", pad) + s
	case alignCenter:
		left := pad / 2
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", pad-left)
	default:
		return s + strings.Repeat(" ", pad)
	}
}

// truncateANSI cuts a styled string to a visible width, preserving escape
// sequences (zero width) and closing with a reset so styling cannot bleed.
func truncateANSI(s string, max int) string {
	if max <= 0 {
		return ""
	}
	var b strings.Builder
	w := 0
	styled := false
	for i := 0; i < len(s); {
		if s[i] == '\033' {
			start := i
			i++
			for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
				i++
			}
			if i < len(s) {
				i++
			}
			b.WriteString(s[start:i])
			styled = true
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := 1
		if isWideRune(r) {
			rw = 2
		}
		if w+rw > max-1 {
			b.WriteString("…")
			if styled {
				b.WriteString(ansiReset)
			}
			return b.String()
		}
		b.WriteString(s[i : i+size])
		w += rw
		i += size
	}
	if styled {
		b.WriteString(ansiReset)
	}
	return b.String()
}

// padCell pads formatted cell content to width using plain-text visible width.
