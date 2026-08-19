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
	Theme    theme.Theme
	Tier     theme.Tier
	sessions []ports.SessionSummary
	filter   string
	cursor   int
	mark     mark.Model
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
		vis := sp.visible()
		switch msg.String() {
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

		// Layout: prefix (2) + mark (1) + space (1) + title (flex) + space (2) + time (len(timeStr))
		fixedWidth := 2 + 1 + 1 + 2 + len(timeStr)
		titleMax := max(10, innerWidth-fixedWidth)
		title := s.Title
		if title == "" {
			title = s.ID
		}
		if ansi.StringWidth(title) > titleMax {
			title = ansi.Truncate(title, titleMax, "…")
		}

		gap := max(1, innerWidth-2-1-1-ansi.StringWidth(title)-len(timeStr))
		spaces := strings.Repeat(" ", gap)

		row := prefix + markGlyph + " " + title + spaces + timeStr
		if selected {
			style := render.Role(t, tier, theme.RoleFG)
			style = render.WithBg(style, t, tier, theme.RoleBGSelection)
			b.WriteString(style.Render(row))
		} else {
			fg := render.Role(t, tier, theme.RoleFG).Render(prefix + markGlyph + " " + title)
			subtle := render.Role(t, tier, theme.RoleFGSubtle).Render(spaces + timeStr)
			b.WriteString(fg + subtle)
		}
	}

	if sp.filter != "" {
		b.WriteByte('\n')
		b.WriteString(render.Role(t, tier, theme.RoleFGSubtle).Render("/" + sp.filter))
	}

	return b.String()
}

func renderSessionPickerDialog(t theme.Theme, tier theme.Tier, width, height int, sp sessionPicker, now time.Time) string {
	innerW := render.DialogBodyWidth(width)
	return render.Dialog(t, tier, width, height, "resume session", sp.View(t, tier, innerW, now),
		"[enter] resume  [esc] cancel  type to filter")
}
