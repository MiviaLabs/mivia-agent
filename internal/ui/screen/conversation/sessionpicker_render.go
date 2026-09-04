package conversation

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/mark"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// Rendering half of the /resume session picker: row assembly, badges,
// preview pane, and dialog chrome. State, key handling, filtering, and
// worktree grouping live in sessionpicker.go.

// formatRelativeTime returns human-readable elapsed time for session listing.
func formatRelativeTime(updatedAt, now time.Time) string {
	if updatedAt.IsZero() {
		return ""
	}
	diff := now.Sub(updatedAt)
	if diff < 0 {
		diff = 0
	}
	switch {
	case diff < time.Minute:
		return "now"
	case diff < time.Hour:
		return fmt.Sprintf("%dm ago", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(diff.Hours()))
	case diff < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(diff.Hours()/24))
	default:
		return updatedAt.Format("Jan 02")
	}
}

// sessionMeta is the turn-count/context-size segment of a session row
// (wireframes-panes.md section 12.2: "14 turns   2h ago   41k ctx"),
// e.g. "14 turns  41k ctx". Either half is omitted when its value is
// zero, and the whole segment is "" when both are - a session an
// adapter cannot report either for renders with no fabricated "0
// turns" or "0k ctx" (SessionSummary's own doc comment).
func sessionMeta(s ports.SessionSummary) string {
	var parts []string
	if s.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d turns", s.Turns))
	}
	if s.ContextTokens > 0 {
		parts = append(parts, fmt.Sprintf("%dk ctx", s.ContextTokens/1000))
	}
	if s.IsCurrent {
		parts = append(parts, "current")
	}
	return strings.Join(parts, "  ")
}

// metaSuffix prefixes meta with its own leading two-space gap, or
// returns "" untouched when there is no meta segment to show - the gap
// belongs to the segment, not to whatever comes before it, so an empty
// meta contributes no stray spaces to the row.
func metaSuffix(meta string) string {
	if meta == "" {
		return ""
	}
	return "  " + meta
}

func (sp sessionPicker) statusBadge(s ports.SessionSummary) string {
	border := render.Role(sp.Theme, sp.Tier, theme.RoleBorder)
	if s.Active {
		stateStyle := render.Role(sp.Theme, sp.Tier, theme.RoleSuccess)
		label := "● LIVE"
		if sp.Tier == theme.TierASCII || sp.Tier == theme.TierNoTTY {
			label = "* LIVE"
		}
		return border.Render("[") + stateStyle.Render(label) + border.Render("]")
	}
	if s.State == "failed" {
		stateStyle := render.Role(sp.Theme, sp.Tier, theme.RoleDanger)
		label := "✖ ERR"
		if sp.Tier == theme.TierASCII || sp.Tier == theme.TierNoTTY {
			label = "x ERR"
		}
		return border.Render("[") + stateStyle.Render(label) + border.Render("]")
	}
	stateStyle := render.Role(sp.Theme, sp.Tier, theme.RoleFGSubtle)
	label := "○ IDLE"
	if sp.Tier == theme.TierASCII || sp.Tier == theme.TierNoTTY {
		label = "- IDLE"
	}
	return border.Render("[") + stateStyle.Render(label) + border.Render("]")
}

func (sp sessionPicker) sessionMark(s ports.SessionSummary) string {
	if s.Active {
		state := mark.Thinking
		if s.State == "running" {
			state = mark.Running
		} else if s.State == "streaming" {
			state = mark.Streaming
		}
		m := mark.New(sp.Theme, sp.Tier, state)
		return render.Role(sp.Theme, sp.Tier, theme.RoleInfo).Render(string(m.Glyph()))
	}
	if s.State == "failed" {
		return mark.New(sp.Theme, sp.Tier, mark.Failed).View()
	}
	return mark.New(sp.Theme, sp.Tier, mark.Idle).View()
}

func (sp sessionPicker) View(t theme.Theme, tier theme.Tier, innerWidth int, now time.Time) string {
	return sp.ViewWindow(t, tier, innerWidth, 0, now)
}

func (sp sessionPicker) ViewWindow(t theme.Theme, tier theme.Tier, innerWidth, maxRows int, now time.Time) string {
	vis := sp.visible()
	if len(vis) == 0 {
		if sp.filter != "" {
			return render.Role(t, tier, theme.RoleFGSubtle).Render("no sessions match /" + sp.filter)
		}
		return render.Role(t, tier, theme.RoleFGSubtle).Render("no saved sessions found")
	}

	// Reserve display lines beyond the rows themselves - but only for
	// chrome that will actually render, and never below one visible row:
	// render.Dialog clips surplus bottom lines, so overflowing the budget
	// hides the selection behind its own chrome. avail == 0 is the
	// unbounded View() mode where no reservation applies.
	avail := maxRows
	showFooter := sp.filter != ""
	showSeparator := true
	if avail > 0 {
		if showFooter {
			if avail >= 2 {
				avail--
			} else {
				showFooter = false // one line: the row wins over the footer
			}
		}
		// Two-pass separator reservation (groupWorktreeRoutes guarantees a
		// contiguous route tail, so at most one label exists): compute the
		// window without a reservation first; reserve only when the label
		// would actually render inside THAT window. A blanket reservation
		// wasted one row whenever the boundary was scrolled off-window.
		start, end := render.WindowSlice(len(vis), sp.cursor, avail)
		if separatorVisible(vis, start, end) && avail >= 2 {
			avail--
		} else if avail < 2 {
			showSeparator = false // one line: the row wins over the label
		}
	}
	start, end := render.WindowSlice(len(vis), sp.cursor, avail)
	if !separatorVisible(vis, start, end) {
		showSeparator = false
	}
	visSlice := vis[start:end]

	var b strings.Builder
	// A window opening inside the route block still shows the label once,
	// as its first line, so the block never loses its marker.
	if showSeparator && start > 0 && vis[start].WorktreeRoute {
		b.WriteString(sp.worktreeSeparatorLabel(t, tier))
		b.WriteString("\n")
	}
	prevRoute := false
	for i, s := range visSlice {
		actualIdx := start + i
		if i > 0 {
			b.WriteByte('\n')
		}
		if showSeparator && s.WorktreeRoute && !prevRoute && !(i == 0 && start > 0) {
			b.WriteString(sp.worktreeSeparatorLabel(t, tier))
			b.WriteString("\n")
		}
		prevRoute = s.WorktreeRoute
		b.WriteString(sp.renderSessionRow(s, actualIdx, tier, innerWidth, now))
	}

	if showFooter {
		b.WriteByte('\n')
		b.WriteString(render.Role(t, tier, theme.RoleFGSubtle).Render("/" + sp.filter))
	}

	return b.String()
}

// separatorVisible reports whether the "-- in worktree --" label would
// render for the window [start,end): either the window opens on a route
// row (label re-shown as the first line) or the plain-to-route boundary
// falls inside it.
func separatorVisible(vis []ports.SessionSummary, start, end int) bool {
	if start >= end {
		return false
	}
	if vis[start].WorktreeRoute {
		return true
	}
	for i := start + 1; i < end; i++ {
		if vis[i].WorktreeRoute && !vis[i-1].WorktreeRoute {
			return true
		}
	}
	return false
}

// worktreeSeparatorLabel renders the "-- in worktree --" label that closes
// the real-session block ahead of the route pseudo-row tail.
func (sp sessionPicker) worktreeSeparatorLabel(t theme.Theme, tier theme.Tier) string {
	return render.Role(t, tier, theme.RoleFGSubtle).Render("-- in worktree --")
}

// renderSessionRow assembles one picker row: selection cursor, session
// mark, optional branch glyph, title, status badge, metadata column, and
// right-aligned relative time.
func (sp sessionPicker) renderSessionRow(s ports.SessionSummary, idx int, tier theme.Tier, innerWidth int, now time.Time) string {
	selected := idx == sp.cursor
	prefix := "  "
	if selected {
		prefix = "> "
	}

	timeStr := formatRelativeTime(s.UpdatedAt, now)
	markGlyph := sp.sessionMark(s)
	badge := sp.statusBadge(s)
	meta := sessionMeta(s)
	metaW := 0
	if meta != "" {
		metaW = ansi.StringWidth(meta) + 2 // its own leading two-space gap
	}

	fixedWidth := 2 + 1 + ansi.StringWidth(markGlyph) + 1 + ansi.StringWidth(badge) + metaW + 2 + len(timeStr)
	glyph := worktreeGlyph(s, tier)
	// The glyph rides inside the title column; reserving its width here
	// keeps saturated titles from pushing the row past innerWidth.
	fixedWidth += ansi.StringWidth(glyph)
	titleMax := max(10, innerWidth-fixedWidth)
	title := glyph + s.Title
	if s.Title == "" {
		title = glyph + s.ID
	}
	if ansi.StringWidth(title) > titleMax {
		title = ansi.Truncate(title, titleMax, uikitconfig.ClipMarker)
	}

	gap := max(1, innerWidth-2-1-ansi.StringWidth(markGlyph)-1-ansi.StringWidth(title)-1-ansi.StringWidth(badge)-metaW-len(timeStr))
	spaces := strings.Repeat(" ", gap)

	t := sp.Theme
	row := prefix + markGlyph + " " + title + " " + badge + metaSuffix(meta) + spaces + timeStr
	if selected {
		style := render.Role(t, tier, theme.RoleFG)
		style = render.WithBg(style, t, tier, theme.RoleBGSelection)
		return style.Render(row)
	}
	fg := render.Role(t, tier, theme.RoleFG).Render(prefix + markGlyph + " " + title + " ")
	subtle := render.Role(t, tier, theme.RoleFGSubtle).Render(metaSuffix(meta) + spaces + timeStr)
	return fg + badge + subtle
}

func (sp sessionPicker) PreviewView(t theme.Theme, tier theme.Tier, innerWidth, bodyRows int, now time.Time) string {
	sel, ok := sp.Selected()
	if !ok {
		return render.Role(t, tier, theme.RoleFGSubtle).Render("no session selected")
	}

	var b strings.Builder
	timeStr := formatRelativeTime(sel.UpdatedAt, now)
	markGlyph := sp.sessionMark(sel)
	state := sel.State
	if state == "" {
		state = "idle"
	}

	hdr := fmt.Sprintf("%s %s%s  (%s • %s)", markGlyph, worktreeGlyph(sel, tier), sel.Title, state, timeStr)
	b.WriteString(render.Role(t, tier, theme.RoleFG).Render(ansi.Truncate(hdr, innerWidth, uikitconfig.ClipMarker)))
	b.WriteByte('\n')
	detail := worktreeDetail(sel)
	if detail != "" {
		b.WriteString(render.Role(t, tier, theme.RoleFGSubtle).Render(ansi.Truncate(detail, innerWidth, uikitconfig.ClipMarker)))
		b.WriteByte('\n')
	}
	b.WriteString(render.Role(t, tier, theme.RoleFGSubtle).Render(strings.Repeat("─", min(innerWidth, 36))))
	b.WriteByte('\n')

	if len(sel.Lines) == 0 {
		b.WriteString(render.Role(t, tier, theme.RoleFGSubtle).Render("no recent activity in this session"))
		return b.String()
	}

	fixed := 3
	if detail != "" {
		fixed++
	}
	availRows := max(1, bodyRows-fixed)
	maxOffset := max(0, len(sel.Lines)-availRows)
	offset := min(sp.previewOffset, maxOffset)

	end := min(len(sel.Lines), offset+availRows)
	for i := offset; i < end; i++ {
		line := sel.Lines[i]
		if i > offset {
			b.WriteByte('\n')
		}
		if ansi.StringWidth(line) > innerWidth {
			line = ansi.Truncate(line, innerWidth, uikitconfig.ClipMarker)
		}
		switch {
		case strings.HasPrefix(line, "> "):
			b.WriteString(render.Role(t, tier, theme.RoleAccent).Render(line))
		case strings.HasPrefix(line, "◈") || strings.HasPrefix(line, "✓"):
			b.WriteString(render.Role(t, tier, theme.RoleSuccess).Render(line))
		default:
			b.WriteString(render.Role(t, tier, theme.RoleFGSubtle).Render(line))
		}
	}

	if len(sel.Lines) > availRows {
		b.WriteByte('\n')
		scrollNote := fmt.Sprintf("[%d-%d of %d lines]", offset+1, end, len(sel.Lines))
		b.WriteString(render.Role(t, tier, theme.RoleFGSubtle).Render(scrollNote))
	}

	return b.String()
}

// worktreeDetail names a selected row's worktree binding for the preview
// pane's header block; empty for plain sessions. A directory-only summary
// falls back to the directory base name so the pane never renders an
// empty "Worktree:" value.
func worktreeDetail(s ports.SessionSummary) string {
	if s.Worktree == "" && s.WorktreeDir == "" {
		return ""
	}
	name := s.Worktree
	if name == "" {
		name = filepath.Base(filepath.ToSlash(s.WorktreeDir))
	}
	return fmt.Sprintf("Worktree: %s (%s)", name, filepath.ToSlash(s.WorktreeDir))
}

func renderSessionPickerDialog(t theme.Theme, tier theme.Tier, width, height int, sp sessionPicker, now time.Time) string {
	innerW := render.DialogBodyWidth(width)
	bodyRows := render.DialogBodyRows(height)
	if sp.preview {
		title := "session preview"
		if sel, ok := sp.Selected(); ok && sel.Title != "" {
			title = "preview: " + sel.Title
		}
		return render.Dialog(t, tier, width, height, title, sp.PreviewView(t, tier, innerW, bodyRows, now),
			"[enter] resume  [←/→] list  [↑/↓] scroll  [esc] cancel")
	}
	var body string
	if height > 0 {
		body = sp.ViewWindow(t, tier, innerW, bodyRows, now)
	} else {
		body = sp.View(t, tier, innerW, now)
	}
	return render.Dialog(t, tier, width, height, "resume session", body,
		"[enter] resume  [←/→] preview  [esc] cancel  type to filter")
}
