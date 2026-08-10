// Live panel - the "Stable Stage" fix.
//
// Everything that changes while the agent works renders HERE, as a paint-only
// overlay over the top of the transcript viewport: agent fleet, active tools,
// thinking, and the streaming answer tail. The viewport below holds immutable
// history only, at its full height.
//
// Why: live state used to be concatenated into the viewport content and
// rebuilt every tick, so the transcript's height changed constantly and the
// scroll anchor chased it - the whole chat visibly jumped up and down, and
// reading an earlier message mid-turn was impossible. The panel used to be a
// reserved band between the transcript and the composer; it is now an overlay
// that holds no layout space at all, so the transcript never moves while the
// agent works and the constant-band invariant is trivially satisfied: during
// a turn the layout never changes.
//
// Height discipline: livePanelHeight is the single source of truth for the
// overlay height. Sections are priority-ordered and shrink bottom-up on short
// terminals so the overlay never exceeds its cap.
package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

const (
	livePanelMaxHeight = 8 // hard ceiling including borders
	liveMaxFleetRows   = 3
	liveMaxToolRows    = 4
	liveMaxStreamRows  = 5
	// liveMaxThinkingRows is the rolling chain-of-thought window: the last few
	// reasoning lines, newest at the bottom. One line was too little to read
	// as thought; the whole buffer would drown the panel.
	liveMaxThinkingRows = 4
)

// livePanelSections computes how many rows each section gets at the current
// state and terminal height. Returns fleet, tools, thinking, stream counts.
func (m *tuiModel) livePanelSections(termH int) (fleet, tools, thinking, stream int) {
	if !m.waiting {
		return 0, 0, 0, 0
	}
	if n := len(m.subagents.ActiveRows()); n > 0 {
		fleet = min(n, liveMaxFleetRows)
	}
	if n := len(m.toolRows); n > 0 {
		tools = min(n, liveMaxToolRows)
	}
	if m.thinkingBuf.Len() > 0 {
		// One label row + the rolling tail beneath it.
		thinking = 1 + min(liveMaxThinkingRows, len(thinkingTailLines(m.thinkingBuf.String(), liveMaxThinkingRows)))
	} else if m.livePlanningLine() != "" {
		// Quiet turn: the planning/thinking affordance takes the thinking slot
		// so a working agent is never a blank panel.
		thinking = 1
	}
	if m.streamBuf.Len() > 0 {
		stream = min(liveMaxStreamRows, len(strings.Split(strings.TrimRight(m.streamBuf.String(), "\n"), "\n")))
	}
	if fleet+tools+thinking+stream == 0 {
		return 0, 0, 0, 0
	}
	// The panel is a fixed band for the whole turn. The content budget inside
	// it is the band minus its two borders. Sections shrink bottom-up until
	// the panel fits that budget and leaves the transcript at least a few
	// rows on short terminals. The budget accounts for the "… n more"
	// indicator rows a truncated section adds.
	budget := m.livePanelBand(max(8, termH)) - 2
	if budget < 2 {
		// The band cannot hold a section with borders. Render it empty
		// rather than let a section spill over it.
		return 0, 0, 0, 0
	}
	indicators := func() int {
		n := 0
		if fleet > 0 && len(m.subagents.ActiveRows()) > fleet {
			n++
		}
		if tools > 0 && len(m.toolRows) > tools {
			n++
		}
		return n
	}
	for fleet+tools+thinking+stream+indicators() > budget {
		switch {
		case stream > 1:
			stream--
		case tools > 1:
			tools--
		case fleet > 1:
			fleet--
		case thinking > 0 && fleet+tools+stream > 0:
			thinking = 0
		case stream > 0 && tools+fleet > 0:
			stream = 0
		case tools > 0 && fleet > 0:
			tools = 0
		default:
			// Only a thinking window can be left, and even it cannot fit the
			// budget. Drop the section rather than render over the band.
			return 0, 0, 0, 0
		}
	}
	return fleet, tools, thinking, stream
}

// livePanelBand is the height the "now" panel paints for the duration of one
// turn: constant while m.waiting, zero when idle. The two border rows are
// included; the content budget inside the band is band - 2. The panel is an
// overlay, so this height is a paint rectangle, not a layout reservation.
func (m *tuiModel) livePanelBand(termH int) int {
	if !m.waiting {
		return 0
	}
	return min(livePanelMaxHeight, max(1, termH/3))
}

// livePanelHeight is the exact painted height (0 when hidden). It is the
// fixed overlay height for the whole turn. renderLivePanel pads to it, so the
// overlay is always a stable rectangle of this many rows.
func (m *tuiModel) livePanelHeight() int {
	return m.livePanelBand(max(8, m.height))
}

// livePanelOverlayRect is the terminal-cell rectangle the live panel paints
// over: the top of the chat pane, one row below the status header, spanning
// the chat pane width. pane.chatX keeps the sessions sidebar clear, so the
// overlay never covers it.
func (m *tuiModel) livePanelOverlayRect() rect {
	pane := newChatPaneLayout(max(1, m.width), m.sessionsSidebar != nil)
	return rect{x: pane.chatX, y: 1, w: pane.chatWidth, h: m.livePanelHeight()}
}

// renderLivePanel draws the paint-only live overlay. Line count always equals
// livePanelHeight - the overlay is a stable rectangle of that many rows.
func (m *tuiModel) renderLivePanel(width int, now time.Time) string {
	band := m.livePanelBand(max(8, m.height))
	if band == 0 {
		return ""
	}
	if width < 30 {
		width = 30
	}
	inner := width - 4
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorDim))
	contentRows := band - 2
	var rows []string

	fleetN, toolN, thinkingN, streamN := m.livePanelSections(max(8, m.height))

	rows = append(rows, m.liveFleetRows(fleetN, inner, now)...)
	rows = append(rows, m.liveToolRows(toolN, inner, now)...)

	if thinkingN > 0 {
		tail := thinkingTailLines(m.thinkingBuf.String(), thinkingN-1)
		if len(tail) > 0 {
			// Labelled rolling window: the label says what these lines are,
			// then the last few reasoning lines scroll beneath it - older dim,
			// newest lit, so the eye tracks where the model actually is.
			rows = append(rows, thinkingLiveStyle.Render("▾ thinking"))
			for i, line := range tail {
				style := thinkingDimStyle
				if i == len(tail)-1 {
					style = thinkingLiveStyle
				}
				rows = append(rows, style.Render(truncateToWidth("    "+line, inner)))
			}
		} else if plan := m.livePlanningLine(); plan != "" {
			rows = append(rows, tuiDimStyle.Render(truncateToWidth(plan, inner)))
		}
	}
	if streamN > 0 {
		lines := strings.Split(strings.TrimRight(m.streamBuf.String(), "\n"), "\n")
		tail := lines[max(0, len(lines)-streamN):]
		for i, ln := range tail {
			prefix := "  "
			if i == len(tail)-1 {
				prefix = tuiDimStyle.Render("▌ ")
			}
			rows = append(rows, truncateToWidth(prefix+ln, inner))
		}
	}

	// Pad short content with blank rows so the band keeps its exact height.
	// No truncation clamp here: an over-budget section must fail the
	// rendered==declared tests loudly, not be silently cut.
	for len(rows) < contentRows {
		rows = append(rows, "")
	}

	head := " now "
	if m.waiting {
		head = fmt.Sprintf(" now · %s ", formatDuration(time.Since(m.turnStart)))
	}
	var b strings.Builder
	b.WriteString(border.Render("┌─" + head + strings.Repeat("─", max(0, width-3-lipgloss.Width(head))) + "┐"))
	for _, r := range rows {
		b.WriteByte('\n')
		b.WriteString(border.Render("│ ") + r)
		if fill := inner - lipgloss.Width(r); fill > 0 {
			b.WriteString(strings.Repeat(" ", fill))
		}
		b.WriteString(border.Render(" │"))
	}
	b.WriteByte('\n')
	b.WriteString(border.Render("└" + strings.Repeat("─", max(0, width-2)) + "┘"))
	return b.String()
}

// liveFleetRows renders the agent section of the live panel. Running agents
// only - a finished run leaves the panel the same way a finished tool row
// does, and lives on in the transcript and the ctrl+g fleet detail.
func (m *tuiModel) liveFleetRows(n, inner int, now time.Time) []string {
	fleetRows := m.subagents.ActiveRows()
	var rows []string
	for i := 0; i < n && i < len(fleetRows); i++ {
		rows = append(rows, fleetRowLine(fleetRows[i], inner, now))
	}
	if n > 0 && len(fleetRows) > n {
		rows = append(rows, tuiDimStyle.Render(fmt.Sprintf("  … %d more agents · ctrl+g", len(fleetRows)-n)))
	}
	return rows
}

// liveToolRows renders the active-tool section of the live panel.
func (m *tuiModel) liveToolRows(n, inner int, now time.Time) []string {
	ordered := orderToolIndices(m.toolRows)
	var rows []string
	for i := 0; i < n && i < len(ordered); i++ {
		r := m.toolRows[ordered[i]]
		icon := toolRunStyle.Render(r.icon(now))
		if r.Done {
			icon = toolOkStyle.Render(glyphCheck)
			if r.Failed {
				icon = toolErrStyle.Render(glyphCross)
			}
		}
		item := newToolRenderItem(r.Name, r.Detail, r.Result, r.Done, r.Failed)
		line := icon + " " + toolIconForName(r.Name) + " " + toolNameStyle.Render(r.Name) + " " +
			tuiDimStyle.Render(item.summary(max(10, inner-28))) + " " +
			toolTimeStyle.Render(formatDuration(r.elapsed(now)))
		rows = append(rows, truncateToWidth(line, inner))
	}
	if n > 0 && len(m.toolRows) > n {
		rows = append(rows, tuiDimStyle.Render(fmt.Sprintf("  … %d more tools", len(m.toolRows)-n)))
	}
	return rows
}

// livePlanningLine is the affordance for a turn with no output yet: the
// agent is working but has produced nothing to show, and a blank panel reads
// as a hang. Empty when there is real content or the turn just started.
func (m *tuiModel) livePlanningLine() string {
	if !m.waiting || m.streamBuf.Len() > 0 || m.thinkingBuf.Len() > 0 || len(m.toolRows) > 0 {
		return ""
	}
	elapsed := time.Since(m.turnStart)
	glyph := brandGlyph(m.logoFrame, brandColorThinking)
	if m.awaitingFirstActivity {
		if elapsed <= 300*time.Millisecond {
			return ""
		}
		return fmt.Sprintf("%s … planning · %s", glyph, formatDuration(elapsed))
	}
	if elapsed <= 2*time.Second {
		return ""
	}
	return fmt.Sprintf("%s thinking · %s", glyph, formatDuration(elapsed))
}

// lastNonEmptyLine returns the final non-blank line of s (for one-line
// thinking/status summaries).
func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

// thinkingTailLines returns the last n non-blank reasoning lines.
func thinkingTailLines(buf string, n int) []string {
	if n < 1 || strings.TrimSpace(buf) == "" {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(buf, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
