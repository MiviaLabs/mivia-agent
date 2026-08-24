package conversation

// statusRow, toolDetail, turnTail, and statusText live in this file,
// grouped by the bottom status bar they collectively draw.

import (
	"fmt"

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
// key hint, generated from the keymap table so it cannot drift from the
// help screen; transient state (turn status, scroll affordances) takes
// the left. The whole row is one line, truncated to the chat column's
// width - inside the split's left pane it must not exceed the pane.
func (s Screen) statusRow() string {
	line := s.statusText()
	var hint string
	if s.embedded {
		hint = render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).Render("esc:close dialog")
	} else if s.quitArmed {
		prefix := s.keys.Hint(keymap.IDHelp, keymap.IDOpenPager, keymap.IDPanelToggle)
		warn := render.Role(s.Theme, s.Tier, theme.RoleWarning).Render("ctrl+c:press again to quit")
		if prefix != "" {
			hint = render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).Render(prefix) + "  " + warn
		} else {
			hint = warn
		}
	} else {
		hint = render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).
			Render(s.keys.Hint(keymap.IDHelp, keymap.IDOpenPager, keymap.IDPanelToggle, keymap.IDQuit))
	}
	if line == "" {
		line = hint
	} else {
		line += "  " + hint
	}
	if s.width > 2 {
		line = ansi.Truncate(line, s.chatWidth(), uikitconfig.ClipMarker)
	}
	return line
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
	if a := render.FormatArgs(args); a != "" {
		detail += " " + a
	}
	return detail
}

// turnTail is the trailing fields wireframes-panes.md section 9 adds
// to an active turn's status line, after the mark/label/elapsed
// statusline.Model already draws: the context share (when the window
// size is known - an unknown window is left out rather than printing
// a fabricated percentage) and the cancel hint, which states real
// behavior (keymap.IDCancel binds esc to "cancel the turn, keep the
// text" in ContextGlobal - ux-rules.md rule 1.4 forbids a hint that
// promises something the current state cannot do).
func (s Screen) turnTail() string {
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	var tail string
	if pct, ok := s.topbar.ContextPercent(); ok {
		tail += subtle.Render(fmt.Sprintf("  %d%% ctx", pct))
	}
	tail += subtle.Render("  esc to cancel")
	return tail
}

// statusText is the transient left side of the status row: the turn's
// status line, or the scroll and truncation affordances.
func (s Screen) statusText() string {
	if v := s.statusline.View(s.now()); v != "" {
		if s.active != nil {
			v += s.turnTail()
		}
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
