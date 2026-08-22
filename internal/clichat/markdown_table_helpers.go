package clichat

func parseTableBuffer(raw []string) ([][]string, []tableAlign, int) {
	var rows [][]string
	var aligns []tableAlign
	maxCols := 0
	for _, line := range raw {
		cells := splitTableRow(line)
		if isGFMSeparator(cells) {
			if aligns == nil {
				aligns = make([]tableAlign, len(cells))
				for i, cell := range cells {
					aligns[i] = parseTableAlign(cell)
				}
			}
			continue
		}
		if len(cells) > maxCols {
			maxCols = len(cells)
		}
		rows = append(rows, cells)
	}
	return rows, aligns, maxCols
}

func normalizeTableAligns(aligns []tableAlign, maxCols int) []tableAlign {
	if aligns == nil {
		return make([]tableAlign, maxCols)
	}
	if len(aligns) >= maxCols {
		return aligns
	}
	extended := make([]tableAlign, maxCols)
	copy(extended, aligns)
	return extended
}

func normalizeTableRows(rows [][]string, maxCols int) {
	for i := range rows {
		if len(rows[i]) < maxCols {
			padded := make([]string, maxCols)
			copy(padded, rows[i])
			rows[i] = padded
		} else if len(rows[i]) > maxCols {
			rows[i] = rows[i][:maxCols]
		}
	}
}

func tableColumnWidths(rows [][]string, maxCols int) []int {
	widths := make([]int, maxCols)
	for _, row := range rows {
		for i, cell := range row {
			if width := VisibleWidth(cell); width > widths[i] {
				widths[i] = width
			}
		}
	}
	for i := range widths {
		if widths[i] < 1 {
			widths[i] = 1
		}
	}
	return widths
}

func shrinkTableWidths(widths []int, maxWidth int) {
	for tableWidth(widths) > maxWidth {
		widest := -1
		widestWidth := 1
		for i, width := range widths {
			if width > widestWidth {
				widestWidth = width
				widest = i
			}
		}
		if widest < 0 {
			return
		}
		widths[widest]--
	}
}

func tableWidth(widths []int) int {
	width := 3
	for _, columnWidth := range widths {
		width += columnWidth + 3
	}
	return width
}
