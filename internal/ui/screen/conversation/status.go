package conversation

// statusRow, toolDetail, turnTail, and statusText live in this file,
// grouped by the bottom status bar they collectively draw.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
)

// statusRow is the permanent bottom status line.
//
// It is always drawn, even when there is nothing to say, because a row
// that appears and disappears reflows every wrapped line above it
// (docs/design/ux-rules.md rule 2.7). Its right side carries the compact
// key hints / tooltips, generated from the keymap table so it cannot drift
// from the help screen; transient state (turn status, elapsed timer,
// scroll affordances) takes the left. The whole row is one line,
// truncated to the chat column's width.
func (s Screen) statusRow() string {
	left := s.statusText()
	right := s.statusRight()
	w := s.chatWidth()
	if w <= 0 {
		w = 80
	}

	var line string
	leftW := ansi.StringWidth(left)
	rightW := ansi.StringWidth(right)

	if left != "" && right != "" {
		gap := w - leftW - rightW
		if gap >= 1 {
			line = left + strings.Repeat(" ", gap) + right
		} else {
			line = ansi.Truncate(left+" "+right, w, uikitconfig.ClipMarker)
		}
	} else if left != "" {
		line = left
		if leftW > w {
			line = ansi.Truncate(line, w, uikitconfig.ClipMarker)
		}
	} else if right != "" {
		gap := w - rightW
		if gap > 0 {
			line = strings.Repeat(" ", gap) + right
		} else {
			line = ansi.Truncate(right, w, uikitconfig.ClipMarker)
		}
	}

	if s.width > 2 && ansi.StringWidth(line) > s.chatWidth() {
		line = ansi.Truncate(line, s.chatWidth(), uikitconfig.ClipMarker)
	}
	return line
}

func (s Screen) statusRight() string {
	if s.embedded {
		return render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).Render("esc:close dialog")
	}
	if s.panel.open && s.panel.focused {
		return render.Role(s.Theme, s.Tier, theme.RoleAccent).Render("tab:composer") + "  " +
			render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).Render("↑/↓:select  enter:view  esc:back")
	}
	if s.quitArmed {
		prefix := s.keys.Hint(keymap.IDHelp, keymap.IDOpenPager, keymap.IDPanelToggle)
		warn := render.Role(s.Theme, s.Tier, theme.RoleWarning).Render("ctrl+c:press again to quit")
		if prefix != "" {
			return render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).Render(prefix) + "  " + warn
		}
		return warn
	}
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	base := s.keys.Hint(keymap.IDHelp, keymap.IDOpenPager, keymap.IDPanelToggle, keymap.IDQuit)
	if s.active != nil {
		return subtle.Render("esc:cancel") + "  " + subtle.Render(base)
	}
	return subtle.Render(base)
}

// toolDetail is the status line's "<detail>" field for a pending or
// running tool call - the wireframe's "go test
// ./internal/storage/..." - built the same way the transcript block's
// own header already does (component/transcript/transcript.go's
// handleToolPending/handleToolStart: "Label: b.Name, Detail:
// render.FormatArgs(b.Args)"), just flattened into one string since
// the status line has no separate label/detail columns.
func toolDetail(name string, args map[string]any) string {
	detail := name
	if a := render.FormatToolDetail(name, args); a != "" {
		detail += " " + a
	}
	return detail
}

// statusText is the transient left side of the status row: the turn's
// status line, or the scroll and truncation affordances.
func (s Screen) statusText() string {
	if v := s.statusline.View(s.now()); v != "" {
		return v
	}
	// Narrow panel open: the transcript is hidden behind the list, so
	// its scroll affordances would narrate something the user cannot
	// see.
	if s.panel.open && !s.panelIsSplit() {
		return ""
	}
	if !s.transcript.Following() {
		if n := s.transcript.NewWhilePaused(); n > 0 {
			return render.Role(s.Theme, s.Tier, theme.RoleWarning).
				Render(fmt.Sprintf("%d new blocks while you read - ctrl+end to follow again", n))
		}
		return render.Role(s.Theme, s.Tier, theme.RoleWarning).
			Render("scrolled up - ctrl+end to follow again")
	}
	if n := s.transcript.Dropped(); n > 0 {
		return render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).
			Render(fmt.Sprintf("%d earlier blocks dropped from this transcript", n))
	}
	return ""
}
