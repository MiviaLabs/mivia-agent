package render

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// MinSplitDiffWidth is the minimum terminal width required to render a
// side-by-side split diff. Below this width, FormatDiffLines falls back
// to a clean unified diff to preserve legibility.
const MinSplitDiffWidth = 60

// SplitDiff renders the hunks of a diff in side-by-side (split) column format.
func SplitDiff(t theme.Theme, tier theme.Tier, width int, d uievent.Diff) string {
	return strings.Join(SplitDiffLines(t, tier, width, d), "\n")
}

// FormatDiffLines renders diffs using side-by-side columns when width >= MinSplitDiffWidth,
// and falls back to unified DiffLines when narrower.
func FormatDiffLines(t theme.Theme, tier theme.Tier, width int, d uievent.Diff) []string {
	if width < MinSplitDiffWidth {
		return DiffLines(t, tier, d)
	}
	return SplitDiffLines(t, tier, width, d)
}

type splitDiffContext struct {
	divider        string
	leftWidth      int
	rightWidth     int
	leftCodeWidth  int
	rightCodeWidth int
	delStyle       lipgloss.Style
	addStyle       lipgloss.Style
	mutedStyle     lipgloss.Style
}

// SplitDiffLines renders each hunk as aligned side-by-side rows with line numbers.
func SplitDiffLines(t theme.Theme, tier theme.Tier, width int, d uievent.Diff) []string {
	if len(d.Hunks) == 0 {
		return nil
	}
	if width <= 0 {
		width = 80
	}

	availableWidth := max(10, width-1)
	leftWidth := availableWidth / 2
	rightWidth := availableWidth - leftWidth

	const numWidth = 4
	ctx := splitDiffContext{
		divider:        Role(t, tier, theme.RoleBorder).Render("│"),
		leftWidth:      leftWidth,
		rightWidth:     rightWidth,
		leftCodeWidth:  max(1, leftWidth-numWidth-1),
		rightCodeWidth: max(1, rightWidth-numWidth-1),
		delStyle:       WithBg(Role(t, tier, theme.RoleDiffDelFG), t, tier, theme.RoleDiffDelBG),
		addStyle:       WithBg(Role(t, tier, theme.RoleDiffAddFG), t, tier, theme.RoleDiffAddBG),
		mutedStyle:     Role(t, tier, theme.RoleFGMuted),
	}

	hunkHeaderStyle := Role(t, tier, theme.RoleDiffHunk)
	var out []string
	for _, hunk := range d.Hunks {
		out = append(out, hunkHeaderStyle.Render(hunk.Header))
		out = append(out, renderSplitHunk(ctx, hunk)...)
	}
	return out
}

func renderSplitHunk(ctx splitDiffContext, hunk uievent.DiffHunk) []string {
	oldLine, newLine := parseHunkHeader(hunk.Header)
	lines := hunk.Lines
	n := len(lines)
	const numWidth = 4
	var out []string

	for i := 0; i < n; {
		if lines[i].Kind == uievent.DiffLineContext {
			leftNum := formatLineNum(oldLine, numWidth, ctx.mutedStyle)
			rightNum := formatLineNum(newLine, numWidth, ctx.mutedStyle)
			leftCode := clipOrPad(lines[i].Text, ctx.leftCodeWidth)
			rightCode := clipOrPad(lines[i].Text, ctx.rightCodeWidth)

			leftCell := leftNum + " " + ctx.mutedStyle.Render(leftCode)
			rightCell := rightNum + " " + ctx.mutedStyle.Render(rightCode)
			out = append(out, leftCell+ctx.divider+rightCell)
			oldLine++
			newLine++
			i++
			continue
		}

		var dels, adds []string
		for i < n && lines[i].Kind != uievent.DiffLineContext {
			if lines[i].Kind == uievent.DiffLineDel {
				dels = append(dels, lines[i].Text)
			} else if lines[i].Kind == uievent.DiffLineAdd {
				adds = append(adds, lines[i].Text)
			}
			i++
		}

		maxRows := max(len(dels), len(adds))
		for r := 0; r < maxRows; r++ {
			var leftCell, rightCell string
			if r < len(dels) {
				leftNum := formatLineNum(oldLine, numWidth, ctx.mutedStyle)
				leftCode := clipOrPad(dels[r], max(0, ctx.leftCodeWidth-1))
				leftCell = leftNum + " " + ctx.delStyle.Render("-"+leftCode)
				oldLine++
			} else {
				leftCell = strings.Repeat(" ", ctx.leftWidth)
			}

			if r < len(adds) {
				rightNum := formatLineNum(newLine, numWidth, ctx.mutedStyle)
				rightCode := clipOrPad(adds[r], max(0, ctx.rightCodeWidth-1))
				rightCell = rightNum + " " + ctx.addStyle.Render("+"+rightCode)
				newLine++
			} else {
				rightCell = strings.Repeat(" ", ctx.rightWidth)
			}
			out = append(out, leftCell+ctx.divider+rightCell)
		}
	}
	return out
}

func formatLineNum(num, width int, st lipgloss.Style) string {
	s := strconv.Itoa(num)
	if len(s) > width {
		s = s[len(s)-width:]
	}
	for len(s) < width {
		s = " " + s
	}
	return st.Render(s)
}

func clipOrPad(s string, width int) string {
	w := ansi.StringWidth(s)
	if w > width {
		return ansi.Truncate(s, width, "…")
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// parseHunkHeader extracts starting old and new line numbers from @@ -A,B +C,D @@.
func parseHunkHeader(h string) (oldStart, newStart int) {
	oldStart, newStart = 1, 1
	if !strings.HasPrefix(h, "@@") {
		return
	}
	parts := strings.Split(h, "@@")
	if len(parts) < 2 {
		return
	}
	rangeStr := strings.TrimSpace(parts[1])
	fields := strings.Fields(rangeStr)
	for _, f := range fields {
		if strings.HasPrefix(f, "-") {
			sub := strings.TrimPrefix(f, "-")
			sub = strings.Split(sub, ",")[0]
			if val, err := strconv.Atoi(sub); err == nil {
				oldStart = val
			}
		} else if strings.HasPrefix(f, "+") {
			sub := strings.TrimPrefix(f, "+")
			sub = strings.Split(sub, ",")[0]
			if val, err := strconv.Atoi(sub); err == nil {
				newStart = val
			}
		}
	}
	return
}
