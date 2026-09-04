package conversation

import (
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/mark"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// State and interaction half of the /resume session picker: the picker
// model, filtering (which keeps worktree route rows matchable like any
// other), key handling, and live refresh. Row assembly, badges, and the
// preview pane render in sessionpicker_render.go.

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
	vis := sp.sessions
	if sp.filter != "" {
		needle := strings.ToLower(sp.filter)
		filtered := make([]ports.SessionSummary, 0, len(vis))
		for _, s := range vis {
			if strings.Contains(strings.ToLower(s.Title), needle) || strings.Contains(strings.ToLower(s.ID), needle) {
				filtered = append(filtered, s)
			}
		}
		vis = filtered
	}
	return groupWorktreeRoutes(vis)
}

// groupWorktreeRoutes sinks synthesized route pseudo-rows ("start a new
// session here") below every real session so the view can draw them under
// a single "-- in worktree --" separator without splitting cursor ranges.
// Within each partition the adapter's ordering stands.
func groupWorktreeRoutes(in []ports.SessionSummary) []ports.SessionSummary {
	routes := 0
	for _, s := range in {
		if s.WorktreeRoute {
			routes++
		}
	}
	if routes == 0 || routes == len(in) {
		return in
	}
	out := make([]ports.SessionSummary, len(in))
	main := out[:len(in)-routes]
	tail := out[len(in)-routes:]
	mi, ti := 0, 0
	for _, s := range in {
		if s.WorktreeRoute {
			tail[ti] = s
			ti++
		} else {
			main[mi] = s
			mi++
		}
	}
	return out
}

// worktreeGlyph prefixes a row that is bound to - or launches a fresh
// session in - a worktree, mirroring git's branch notation.
func worktreeGlyph(s ports.SessionSummary, tier theme.Tier) string {
	if s.Worktree == "" && !s.WorktreeRoute {
		return ""
	}
	if tier == theme.TierASCII || tier == theme.TierNoTTY {
		return "+ "
	}
	return "⎇ "
}

func (sp sessionPicker) Selected() (ports.SessionSummary, bool) {
	vis := sp.visible()
	if sp.cursor < 0 || sp.cursor >= len(vis) {
		return ports.SessionSummary{}, false
	}
	return vis[sp.cursor], true
}

// sessionPickerTickMsg drives the /resume picker's live-refresh loop:
// while the dialog stays open, each tick re-derives Active/State from
// the session pool so a background session's status dot moves without
// the user closing and reopening the picker.
type sessionPickerTickMsg struct{}

// sessionPickerTickCmd returns the one-shot Cmd for the next refresh
// tick. The screen re-arms it after every tick it handles (see
// conversation.go's sessionPickerTickMsg case) and lets it lapse once
// the picker closes, the same self-terminating shape as
// statusline.tickCmd.
func sessionPickerTickCmd() tea.Cmd {
	return tea.Tick(uikitconfig.SessionPickerRefreshInterval, func(time.Time) tea.Msg { return sessionPickerTickMsg{} })
}

// resumePickMsg is emitted on enter over a worktree row. Route rows must
// start a NEW session rather than resume by ID, and bound rows must carry
// their worktree directory into the pool - neither fits the plain
// picker.SelectMsg{Item: id} payload.
type resumePickMsg struct {
	summary ports.SessionSummary
}

// refresh re-derives Active/State for every row from active, a cheap
// per-session liveness check (SessionPool.IsActive via
// CommandRunner.SessionActive - a map lookup and an atomic load, no
// I/O). It never re-queries the session store: Title, Turns,
// ContextTokens, and IsCurrent are a snapshot from when the picker
// opened and stay as they were. A nil active (a runner that predates
// SessionActive) is a no-op rather than a panic.
func (sp sessionPicker) refresh(active func(id string) bool) sessionPicker {
	if active == nil {
		return sp
	}
	sessions := make([]ports.SessionSummary, len(sp.sessions))
	for i, s := range sp.sessions {
		s.Active = active(s.ID)
		s.State = "done"
		if s.Active {
			s.State = "running"
		}
		sessions[i] = s
	}
	sp.sessions = sessions
	return sp
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
			return sp, pickSelectionCmd(sel)
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
			return sp, pickSelectionCmd(sel)
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

// pickSelectionCmd emits the enter-key payload for a selected row: a
// resumePickMsg for route rows and instance-bound rows, the plain
// SelectMsg otherwise. The bound-row half of the split is
// SessionSummary.WorktreeBound - the single predicate shared with the
// typed-/resume router - so the two surfaces cannot disagree on which
// rows take the instance-scoped path.
func pickSelectionCmd(sel ports.SessionSummary) tea.Cmd {
	if sel.WorktreeRoute || sel.WorktreeBound() {
		return func() tea.Msg { return resumePickMsg{summary: sel} }
	}
	return func() tea.Msg { return picker.SelectMsg{Item: sel.ID} }
}

func (sp *sessionPicker) clampCursor() {
	vis := sp.visible()
	if sp.cursor >= len(vis) {
		sp.cursor = max(0, len(vis)-1)
	}
}
