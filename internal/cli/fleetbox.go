// Fleet box: the pinned agent panel between the status header and the
// transcript, shown only while subagents are active this turn. One row per
// agent — phase diamond, name, latest activity, tool counts, elapsed —
// capped at fleetBoxMaxRows with an explicit "… n more" line. ctrl+g opens
// the full fleet detail in the block overlay.
//
// Height discipline: fleetBoxHeight is the single source of truth and is
// consumed by BOTH layout paths (Update's layout() and View's
// chatViewLayout) — the composer-clipping bug came from exactly this kind
// of drift, and TestLayoutAndViewAgreeOnViewportHeight pins it.
package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

const fleetBoxMaxRows = 4

// fleetBoxVisible reports whether the fleet box renders this frame.
func (m *tuiModel) fleetBoxVisible() bool {
	return m.waiting && m.subagents != nil && len(m.subagents.Rows()) > 0
}

// fleetBoxHeight is the exact rendered height (0 when hidden).
func (m *tuiModel) fleetBoxHeight() int {
	if !m.fleetBoxVisible() {
		return 0
	}
	rows := len(m.subagents.Rows())
	if rows > fleetBoxMaxRows {
		rows = fleetBoxMaxRows + 1 // "… n more" line
	}
	return rows + 2 // top + bottom border
}

// renderFleetBox renders the pinned panel. The line count always equals
// fleetBoxHeight — layout math depends on it.
func (m *tuiModel) renderFleetBox(width int, now time.Time) string {
	rows := m.subagents.Rows()
	if len(rows) == 0 {
		return ""
	}
	if width < 30 {
		width = 30
	}
	border := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	running := m.subagents.Active()

	head := fmt.Sprintf(" agents · %d running · ctrl+g detail ", running)
	head = truncateToWidth(head, width-4)
	var b strings.Builder
	b.WriteString(border.Render("┌─" + head + strings.Repeat("─", max(0, width-3-lipgloss.Width(head))) + "┐"))
	b.WriteByte('\n')

	shown := rows
	if len(shown) > fleetBoxMaxRows {
		shown = shown[:fleetBoxMaxRows]
	}
	inner := width - 4
	for _, r := range shown {
		line := fleetRowLine(r, inner, now)
		b.WriteString(border.Render("│ ") + line)
		if fill := inner - lipgloss.Width(line); fill > 0 {
			b.WriteString(strings.Repeat(" ", fill))
		}
		b.WriteString(border.Render(" │") + "\n")
	}
	if len(rows) > fleetBoxMaxRows {
		more := tuiDimStyle.Render(fmt.Sprintf("… %d more · ctrl+g", len(rows)-fleetBoxMaxRows))
		b.WriteString(border.Render("│ ") + more)
		if fill := inner - lipgloss.Width(more); fill > 0 {
			b.WriteString(strings.Repeat(" ", fill))
		}
		b.WriteString(border.Render(" │") + "\n")
	}
	b.WriteString(border.Render("└" + strings.Repeat("─", max(0, width-2)) + "┘"))
	return b.String()
}

// fleetRowLine renders one agent row: diamond, name, activity, counts, time.
func fleetRowLine(r subagentRun, inner int, now time.Time) string {
	diamond := agentBadgeStyle.Render("◆")
	status := ""
	if r.ToolsOpen == 0 && r.ToolsDone > 0 {
		diamond = toolOkStyle.Render("◆")
		status = toolOkStyle.Render(" ✓")
	}
	activity := safeDialogText(r.LastDetail)
	if r.LastTool != "" {
		activity = safeDialogText(r.LastTool)
		if r.LastDetail != "" {
			activity += " · " + safeDialogText(r.LastDetail)
		}
	}
	counts := fmt.Sprintf("%d", r.ToolsDone)
	if r.ToolsOpen > 0 {
		counts = fmt.Sprintf("%d+%d", r.ToolsDone, r.ToolsOpen)
	}
	elapsed := ""
	if !r.Started.IsZero() {
		elapsed = formatDuration(now.Sub(r.Started))
	}
	right := tuiDimStyle.Render(counts+" ⚙ · "+elapsed) + status
	left := diamond + " " + lipgloss.NewStyle().Bold(true).Render(safeDialogText(r.Name)) + " " +
		tuiDimStyle.Render(truncateToWidth(activity, max(8, inner-lipgloss.Width(right)-14)))
	gap := inner - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return lipgloss.NewStyle().MaxWidth(inner).Render(left + " " + right)
	}
	return left + strings.Repeat(" ", gap) + right
}

// openFleetOverlay opens the full fleet detail in the block overlay.
func (m *tuiModel) openFleetOverlay() bool {
	rows := m.subagents.Rows()
	if !m.waiting {
		return len(rows) > 0
	}
	if len(rows) == 0 {
		return false
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "◆ %s  ·  %d tools done, %d open", safeDialogText(r.Name), r.ToolsDone, r.ToolsOpen)
		if r.LastTool != "" {
			fmt.Fprintf(&b, "\n  last tool: %s", redactPreview(SafeChatBlockText(r.LastTool, 0)))
		}
		if r.LastDetail != "" {
			fmt.Fprintf(&b, "\n  %s", redactPreview(SafeChatBlockText(r.LastDetail, 0)))
		}
		if !r.Started.IsZero() {
			fmt.Fprintf(&b, "\n  started %s ago", formatDuration(time.Since(r.Started)))
		}
	}
	m.setOverlay(&blockOverlay{
		title: fmt.Sprintf("◆ agents · %d this turn · captured at open", len(rows)),
		lines: strings.Split(b.String(), "\n"),
		prefs: detailDialogPrefs(),
	})
	return true
}
