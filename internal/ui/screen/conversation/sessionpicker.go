package conversation

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/mark"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// sessionPicker is the state of an open /resume session selection modal.
type sessionPicker struct {
	Theme         theme.Theme
	Tier          theme.Tier
	sessions      []ports.SessionSummary
	filter        string
	cursor        int
	preview       bool
	previewOffset int
	mark          mark.Model
}

func newSessionPicker(t theme.Theme, tier theme.Tier, sessions []ports.SessionSummary) sessionPicker {
	return sessionPicker{
		Theme:    t,
		Tier:     tier,
		sessions: sessions,
		mark:     mark.New(t, tier, mark.Thinking),
	}
}

func (sp sessionPicker) visible() []ports.SessionSummary {
	if sp.filter == "" {
		return sp.sessions
	}
	needle := strings.ToLower(sp.filter)
	out := make([]ports.SessionSummary, 0, len(sp.sessions))
	for _, s := range sp.sessions {
		if strings.Contains(strings.ToLower(s.Title), needle) || strings.Contains(strings.ToLower(s.ID), needle) {
			out = append(out, s)
		}
	}
	return out
}

func (sp sessionPicker) Selected() (ports.SessionSummary, bool) {
	vis := sp.visible()
	if sp.cursor < 0 || sp.cursor >= len(vis) {
		return ports.SessionSummary{}, false
	}
	return vis[sp.cursor], true
}

func (sp sessionPicker) Update(msg tea.Msg) (sessionPicker, tea.Cmd) {
	switch msg := msg.(type) {
	case mark.TickMsg:
		m, cmd := sp.mark.Update(msg)
		sp.mark = m
		return sp, cmd
	case tea.KeyPressMsg:
		if sp.preview {
			return sp.updatePreviewKey(msg)
		}
		return sp.updateListKey(msg)
	}
	return sp, nil
}

func (sp sessionPicker) updatePreviewKey(msg tea.KeyPressMsg) (sessionPicker, tea.Cmd) {
	switch msg.String() {
	case "left", "right":
		sp.preview = false
		return sp, nil
	case "up", "k":
		if sp.previewOffset > 0 {
			sp.previewOffset--
		}
		return sp, nil
	case "down", "j":
		sp.previewOffset++
		return sp, nil
	case "pgup":
		sp.previewOffset = max(0, sp.previewOffset-5)
		return sp, nil
	case "pgdown":
		sp.previewOffset += 5
		return sp, nil
	case "enter":
		if sel, ok := sp.Selected(); ok {
			return sp, func() tea.Msg { return picker.SelectMsg{Item: sel.ID} }
		}
		return sp, nil
	case "esc":
		return sp, func() tea.Msg { return picker.CancelMsg{} }
	}
	return sp, nil
}

func (sp sessionPicker) updateListKey(msg tea.KeyPressMsg) (sessionPicker, tea.Cmd) {
	vis := sp.visible()
	switch msg.String() {
	case "left", "right":
		if _, ok := sp.Selected(); ok {
			sp.preview = true
			sp.previewOffset = 0
		}
		return sp, nil
	case "up":
		if sp.cursor > 0 {
			sp.cursor--
		}
		return sp, nil
	case "down":
		if sp.cursor < len(vis)-1 {
			sp.cursor++
		}
		return sp, nil
	case "enter":
		if sel, ok := sp.Selected(); ok {
			return sp, func() tea.Msg { return picker.SelectMsg{Item: sel.ID} }
		}
		return sp, nil
	case "esc":
		return sp, func() tea.Msg { return picker.CancelMsg{} }
	case "backspace":
		if len(sp.filter) > 0 {
			_, size := utf8.DecodeLastRuneInString(sp.filter)
			sp.filter = sp.filter[:len(sp.filter)-size]
			sp.clampCursor()
		}
		return sp, nil
	default:
		if text := msg.Text; text != "" && !strings.ContainsRune(text, '\n') {
			sp.filter += text
			sp.clampCursor()
			return sp, nil
		}
	}
	return sp, nil
}

func (sp *sessionPicker) clampCursor() {
	vis := sp.visible()
	if sp.cursor >= len(vis) {
		sp.cursor = max(0, len(vis)-1)
	}
}

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
		return m.View()
	}
	if s.State == "failed" {
		return mark.New(sp.Theme, sp.Tier, mark.Failed).View()
	}
	return mark.New(sp.Theme, sp.Tier, mark.Idle).View()
}

func (sp sessionPicker) View(t theme.Theme, tier theme.Tier, innerWidth int, now time.Time) string {
	vis := sp.visible()
	if len(vis) == 0 {
		if sp.filter != "" {
			return render.Role(t, tier, theme.RoleFGSubtle).Render("no sessions match /" + sp.filter)
		}
		return render.Role(t, tier, theme.RoleFGSubtle).Render("no saved sessions found")
	}

	var b strings.Builder
	for i, s := range vis {
		if i > 0 {
			b.WriteByte('\n')
		}
		selected := i == sp.cursor
		prefix := "  "
		if selected {
			prefix = "> "
		}

		timeStr := formatRelativeTime(s.UpdatedAt, now)
		markGlyph := sp.sessionMark(s)
		badge := sp.statusBadge(s)

		fixedWidth := 2 + 1 + ansi.StringWidth(markGlyph) + 1 + ansi.StringWidth(badge) + 2 + len(timeStr)
		titleMax := max(10, innerWidth-fixedWidth)
		title := s.Title
		if title == "" {
			title = s.ID
		}
		if ansi.StringWidth(title) > titleMax {
			title = ansi.Truncate(title, titleMax, "…")
		}

		gap := max(1, innerWidth-2-1-ansi.StringWidth(markGlyph)-1-ansi.StringWidth(title)-1-ansi.StringWidth(badge)-len(timeStr))
		spaces := strings.Repeat(" ", gap)

		row := prefix + markGlyph + " " + title + " " + badge + spaces + timeStr
		if selected {
			style := render.Role(t, tier, theme.RoleFG)
			style = render.WithBg(style, t, tier, theme.RoleBGSelection)
			b.WriteString(style.Render(row))
		} else {
			fg := render.Role(t, tier, theme.RoleFG).Render(prefix + markGlyph + " " + title + " ")
			subtle := render.Role(t, tier, theme.RoleFGSubtle).Render(spaces + timeStr)
			b.WriteString(fg + badge + subtle)
		}
	}

	if sp.filter != "" {
		b.WriteByte('\n')
		b.WriteString(render.Role(t, tier, theme.RoleFGSubtle).Render("/" + sp.filter))
	}

	return b.String()
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

	hdr := fmt.Sprintf("%s %s  (%s • %s)", markGlyph, sel.Title, state, timeStr)
	b.WriteString(render.Role(t, tier, theme.RoleFG).Render(ansi.Truncate(hdr, innerWidth, "…")))
	b.WriteByte('\n')
	b.WriteString(render.Role(t, tier, theme.RoleFGSubtle).Render(strings.Repeat("─", min(innerWidth, 36))))
	b.WriteByte('\n')

	if len(sel.Lines) == 0 {
		b.WriteString(render.Role(t, tier, theme.RoleFGSubtle).Render("no recent activity in this session"))
		return b.String()
	}

	availRows := max(1, bodyRows-3)
	maxOffset := max(0, len(sel.Lines)-availRows)
	offset := min(sp.previewOffset, maxOffset)

	end := min(len(sel.Lines), offset+availRows)
	for i := offset; i < end; i++ {
		line := sel.Lines[i]
		if i > offset {
			b.WriteByte('\n')
		}
		if ansi.StringWidth(line) > innerWidth {
			line = ansi.Truncate(line, innerWidth, "…")
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
	return render.Dialog(t, tier, width, height, "resume session", sp.View(t, tier, innerW, now),
		"[enter] resume  [←/→] preview  [esc] cancel  type to filter")
}
