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
	w := s.chatWidth()
	if w <= 0 {
		w = 80
	}

	left := s.statusText()
	leftW := ansi.StringWidth(left)

	avail := w
	if left != "" {
		avail = w - leftW - 2
		if avail < 0 {
			avail = 0
		}
	}

	if s.active != nil && w >= 15 {
		cancelW := ansi.StringWidth("esc:cancel")
		if avail < cancelW {
			maxLeftW := max(0, w-cancelW-2)
			if maxLeftW > 0 {
				left = ansi.Truncate(left, maxLeftW, uikitconfig.ClipMarker)
			} else {
				left = ""
			}
			leftW = ansi.StringWidth(left)
			avail = max(0, w-leftW-2)
		}
	}

	right := s.statusRight(avail)
	rightW := ansi.StringWidth(right)

	var line string
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

func (s Screen) statusRight(avail int) string {
	if avail <= 0 {
		return ""
	}
	if s.embedded {
		txt := "esc:close dialog"
		if ansi.StringWidth(txt) <= avail {
			return render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).Render(txt)
		}
		return ""
	}
	if s.panel.open && s.panel.focused {
		return s.panelFocusedHints(avail)
	}
	if s.quitArmed {
		return s.quitArmedHints(avail)
	}

	candidateList := [][]keymap.ID{
		{keymap.IDHelp, keymap.IDOpenPager, keymap.IDPanelToggle, keymap.IDQuit},
		{keymap.IDHelp, keymap.IDPanelToggle, keymap.IDQuit},
		{keymap.IDHelp, keymap.IDQuit},
		{keymap.IDHelp},
	}

	if s.active != nil {
		return s.activeTurnHints(avail, candidateList)
	}
	return s.idleHints(avail, candidateList)
}

// panelFocusedHints states what the keys do on the row the cursor is
// ACTUALLY on. Enter opens a file's diff or a subagent's thread, but on a
// section header it folds - so a fixed "enter:view" was untrue exactly
// when the header was selected, and ux-rules 1.4 requires a hint to state
// the complete truth. The fold keys were reachable with no hint at all;
// the glyph was their only advertisement.
func (s Screen) panelFocusedHints(avail int) string {
	accent := render.Role(s.Theme, s.Tier, theme.RoleAccent)
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	tab := accent.Render("tab:composer")
	candidates := []string{
		"↑/↓:select  enter:view  esc:back",
		"enter:view  esc:back",
		"esc:back",
		"",
	}
	if s.panel.sectionHeaderSelected() {
		candidates = []string{
			"↑/↓:select  ←/→:fold  enter:toggle  esc:back",
			"←/→:fold  enter:toggle  esc:back",
			"←/→:fold  esc:back",
			"esc:back",
			"",
		}
	}
	for _, rest := range candidates {
		full := tab
		if rest != "" {
			full = tab + "  " + subtle.Render(rest)
		}
		if ansi.StringWidth(full) <= avail {
			return full
		}
	}
	return ""
}

func (s Screen) quitArmedHints(avail int) string {
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	warn := render.Role(s.Theme, s.Tier, theme.RoleWarning).Render("ctrl+c:press again to quit")
	if ansi.StringWidth(warn) > avail {
		return ansi.Truncate(warn, avail, uikitconfig.ClipMarker)
	}
	prefixCandidates := [][]keymap.ID{
		{keymap.IDHelp, keymap.IDOpenPager, keymap.IDPanelToggle},
		{keymap.IDHelp, keymap.IDPanelToggle},
		{keymap.IDHelp},
	}
	for _, ids := range prefixCandidates {
		if prefix := s.keys.Hint(ids...); prefix != "" {
			full := subtle.Render(prefix) + "  " + warn
			if ansi.StringWidth(full) <= avail {
				return full
			}
		}
	}
	return warn
}

func (s Screen) activeTurnHints(avail int, candidateList [][]keymap.ID) string {
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	cancel := subtle.Render("esc:cancel")
	if ansi.StringWidth(cancel) > avail {
		return ""
	}
	for _, ids := range candidateList {
		base := s.keys.Hint(ids...)
		if base != "" {
			full := cancel + "  " + subtle.Render(base)
			if ansi.StringWidth(full) <= avail {
				return full
			}
		}
	}
	return cancel
}

func (s Screen) idleHints(avail int, candidateList [][]keymap.ID) string {
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	for _, ids := range candidateList {
		base := s.keys.Hint(ids...)
		if base != "" {
			rendered := subtle.Render(base)
			if ansi.StringWidth(rendered) <= avail {
				return rendered
			}
		}
	}
	return ""
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
