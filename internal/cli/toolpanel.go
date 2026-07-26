package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Tool panel UX constants.
const (
	// toolMaxVisibleRows is the max collapsed tool rows shown at once
	// (plus 1 header line). Active + most recent stay in view via scroll window.
	toolMaxVisibleRows = 6
)

// toolPanelState is the windowed view over toolRows for keyboard/mouse nav.
type toolPanelState struct {
	// Scroll is the first index into ordered indices (0 = top of list).
	Scroll int
	// Selected is index into toolRows (-1 = none).
	Selected int
	// Focused when user is navigating tools (arrows apply here, not transcript).
	Focused bool
	// Screen Y range of the last rendered panel (absolute, for mouse hit).
	Y0, Y1 int
	// ordered maps display row → toolRows index (running first, then recent).
	ordered []int
	// visible is ordered[scroll:scroll+max] toolRows indices currently painted.
	visible []int
	// rowY maps toolRows index → absolute screen Y of that collapsed row.
	rowY map[int]int
}

// orderToolIndices puts active (not done) first, then remaining by recency (newest last index first among done).
// Within active: original order. Within done: reverse chronological (higher index first).
func orderToolIndices(rows []toolRow) []int {
	var running, done []int
	for i, r := range rows {
		if !r.Done {
			running = append(running, i)
		} else {
			done = append(done, i)
		}
	}
	// Done: most recent first (scan from end).
	for i, j := 0, len(done)-1; i < j; i, j = i+1, j-1 {
		done[i], done[j] = done[j], done[i]
	}
	return append(running, done...)
}

// clampToolScroll keeps scroll so selected (if any) is visible and window valid.
func clampToolScroll(scroll, selected int, ordered []int, maxVis int) int {
	n := len(ordered)
	if n == 0 {
		return 0
	}
	if maxVis < 1 {
		maxVis = toolMaxVisibleRows
	}
	maxScroll := max(0, n-maxVis)
	if scroll < 0 {
		scroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	// Ensure selected index is inside window when selected is in ordered list.
	if selected >= 0 {
		pos := -1
		for i, idx := range ordered {
			if idx == selected {
				pos = i
				break
			}
		}
		if pos >= 0 {
			if pos < scroll {
				scroll = pos
			}
			if pos >= scroll+maxVis {
				scroll = pos - maxVis + 1
			}
			if scroll < 0 {
				scroll = 0
			}
			if scroll > maxScroll {
				scroll = maxScroll
			}
		}
	}
	return scroll
}

// renderToolPanelWindow draws at most maxVis collapsed rows (plus header),
// with expand under the selected row if Expanded.
// yBase is absolute screen Y of the first line of the returned block (for mouse).
// Returns rendered text, line count, and updated panel state (scroll/visible/rowY).
func renderToolPanelWindow(
	rows []toolRow,
	width int,
	now time.Time,
	st toolPanelState,
	logoFrame int,
	phase brandPhase,
	maxVis int,
	yBase int,
) (string, int, toolPanelState) {
	if len(rows) == 0 {
		st.ordered = nil
		st.visible = nil
		st.rowY = nil
		return "", 0, st
	}
	if width < 20 {
		width = 20
	}
	if maxVis <= 0 {
		maxVis = toolMaxVisibleRows
	}

	ordered := orderToolIndices(rows)
	st.ordered = ordered
	st.Scroll = clampToolScroll(st.Scroll, st.Selected, ordered, maxVis)
	end := st.Scroll + maxVis
	if end > len(ordered) {
		end = len(ordered)
	}
	window := ordered[st.Scroll:end]
	st.visible = append([]int(nil), window...)
	st.rowY = make(map[int]int, len(window))

	var b strings.Builder
	totalLines := writeToolPanelHeader(&b, rows, ordered, st, logoFrame, phase, maxVis, end)

	rowScreenY := yBase + totalLines
	for _, ti := range window {
		st.rowY[ti] = rowScreenY
		n := writeToolPanelRow(&b, rows[ti], ti, st.Selected == ti, width, now, logoFrame)
		totalLines += n
		rowScreenY += n
	}

	st.Y0 = yBase
	st.Y1 = yBase + totalLines - 1
	return strings.TrimRight(b.String(), "\n"), totalLines, st
}

func writeToolPanelHeader(
	b *strings.Builder,
	rows []toolRow,
	ordered []int,
	st toolPanelState,
	logoFrame int,
	phase brandPhase,
	maxVis, end int,
) int {
	open, done, total := countTools(rows)
	hdrColor := brandColor(phase)
	if phase != phaseTools && phase != phaseMulti {
		hdrColor = brandColorTools
	}
	hdrMark := brandGlyph(logoFrame, hdrColor)
	more := ""
	if len(ordered) > maxVis {
		more = fmt.Sprintf(" · %d–%d/%d", st.Scroll+1, end, len(ordered))
	}
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(hdrColor)).Render(
		fmt.Sprintf("  %s tools  %d/%d done · %d active%s", hdrMark, done, total, open, more),
	))
	b.WriteByte('\n')
	n := 1
	if st.Focused || len(ordered) > maxVis {
		b.WriteString(toolDimStyle.Render("  tab/↑↓ select · enter/space expand · wheel when hover"))
		b.WriteByte('\n')
		n++
	}
	return n
}

func writeToolPanelRow(
	b *strings.Builder,
	r toolRow,
	ti int,
	selected bool,
	width int,
	now time.Time,
	logoFrame int,
) int {
	var iconStyled string
	switch {
	case !r.Done:
		iconStyled = brandGlyph(logoFrame+ti, brandColorTools)
	case r.Failed:
		iconStyled = toolErrStyle.Render("✗")
	default:
		iconStyled = toolOkStyle.Render("✓")
	}
	path := parseToolPath(r.Detail, r.Result)
	pathPart := ""
	if path != "" {
		chip := path
		if len(chip) > max(12, width/3) {
			chip = "…" + chip[len(chip)-max(11, width/3-1):]
		}
		pathPart = " " + toolPathStyle.Render(" "+chip+" ")
	}
	summary := summarizeToolDetail(r.Detail, "")
	if r.Done && r.Result != "" {
		summary = summarizeToolDetail(r.Detail, r.Result)
	}
	if path != "" && summary == path {
		summary = ""
	}
	summary = truncateStr(summary, max(16, width-40-len(path)))
	marker := "  "
	if selected {
		marker = "▸ "
	}
	line := fmt.Sprintf("%s%s %s %s%s %s %s",
		marker, iconStyled, toolIconForName(r.Name), toolNameStyle.Render(r.Name),
		pathPart, toolDimStyle.Render(summary), toolTimeStyle.Render(formatDuration(r.elapsed(now))),
	)
	if selected {
		line = toolSelStyle.Render(line)
	}
	b.WriteString(line)
	b.WriteByte('\n')
	n := 1
	if r.Expanded && selected {
		n += writeToolPanelExpand(b, r, width)
	}
	return n
}

func writeToolPanelExpand(b *strings.Builder, r toolRow, width int) int {
	maxPreviewLines := 6
	if isEditTool(r.Name) {
		maxPreviewLines = 10
	}
	n := 0
	if r.Detail != "" {
		n += writePreviewSection(b, "    ╭─ input", r.Detail, width, maxPreviewLines, false)
	}
	if r.Result != "" {
		colorDiff := isEditTool(r.Name) || resultLooksLikeDiff(r.Result)
		n += writePreviewSection(b, "    ╰─ output", r.Result, width, maxPreviewLines, colorDiff)
	}
	return n
}

func writePreviewSection(b *strings.Builder, header, body string, width, maxLines int, colorDiff bool) int {
	b.WriteString(toolSection.Render(header))
	b.WriteByte('\n')
	n := 1
	all := strings.Split(body, "\n")
	lines := all
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
		b.WriteString(toolDimStyle.Render(fmt.Sprintf("    │ … (%d more)", len(all)-maxLines)))
		b.WriteByte('\n')
		n++
	}
	for _, l := range lines {
		l = clipPreviewLine(l, width)
		if colorDiff {
			b.WriteString("  " + colorDiffLine(l))
		} else {
			b.WriteString(fmt.Sprintf("    │ %s", l))
		}
		b.WriteByte('\n')
		n++
	}
	return n
}

// toolIndexAtY returns toolRows index under absolute mouse Y, or -1.
func (st *toolPanelState) toolIndexAtY(y int) int {
	if st.rowY == nil {
		return -1
	}
	for idx, ry := range st.rowY {
		if y == ry {
			return idx
		}
	}
	// Expand region: still "on" selected if between header and bottom.
	if st.Selected >= 0 && y >= st.Y0 && y <= st.Y1 {
		if _, ok := st.rowY[st.Selected]; ok {
			// clicks on expand body keep selection
			if y > st.rowY[st.Selected] {
				return st.Selected
			}
		}
	}
	return -1
}

// inPanel reports whether absolute Y is over the tool strip.
func (st *toolPanelState) inPanel(y int) bool {
	if st.Y1 < st.Y0 {
		return false
	}
	return y >= st.Y0 && y <= st.Y1
}

// selectNext moves selection along ordered list; delta -1 or +1.
func (st *toolPanelState) selectNext(delta int, maxVis int) {
	if len(st.ordered) == 0 {
		return
	}
	pos := 0
	if st.Selected >= 0 {
		for i, idx := range st.ordered {
			if idx == st.Selected {
				pos = i
				break
			}
		}
		pos += delta
	} else if delta < 0 {
		pos = len(st.ordered) - 1
	}
	if pos < 0 {
		pos = 0
	}
	if pos >= len(st.ordered) {
		pos = len(st.ordered) - 1
	}
	st.Selected = st.ordered[pos]
	st.Focused = true
	st.Scroll = clampToolScroll(st.Scroll, st.Selected, st.ordered, maxVis)
}

// scrollWindow moves the visible window without changing selection if possible.
func (st *toolPanelState) scrollWindow(delta, maxVis int) {
	if len(st.ordered) == 0 {
		return
	}
	st.Scroll = clampToolScroll(st.Scroll+delta, st.Selected, st.ordered, maxVis)
}
