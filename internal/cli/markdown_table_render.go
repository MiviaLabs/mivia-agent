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

	var b strings.Builder
	b.WriteString(tableBorderRow(widths, "┌", "┬", "┐"))
	for ri, row := range formatted {
		b.WriteByte('\n')
		b.WriteString(formatAlignedTableRow(row, widths, aligns, ri == 0))
		if ri == 0 {
			b.WriteByte('\n')
			b.WriteString(tableBorderRow(widths, "├", "┼", "┤"))
		}
	}
	b.WriteByte('\n')
	b.WriteString(tableBorderRow(widths, "└", "┴", "┘"))
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
