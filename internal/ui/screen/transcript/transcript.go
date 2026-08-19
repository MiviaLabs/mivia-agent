// Package transcript is the full-screen pager over the conversation
// (cockpit-research.md rule 6.2). It is pushed onto the app router's
// stack by ctrl+o and is the replacement for terminal find: the
// alternate screen hides the session from Cmd-F and tmux copy-mode, so
// the app must search it itself.
//
// Keys follow less, because less is the muscle memory every terminal
// user already has. The conversation is a VALUE snapshot of the
// conversation screen's transcript model: blocks re-render at any width,
// so the pager re-flows on resize without anyone rebuilding it.
package transcript

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	conv "github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

var _ app.Screen = Screen{}

// mode is which of the two states the pager is in.
type mode int

const (
	// modePager draws the conversation on the alternate screen.
	modePager mode = iota
	// modeHandover holds no alternate screen: rule 6.3's `[` has written
	// the conversation into native scrollback, and the user reads it
	// with the terminal's own scrolling, find and copy-mode until any
	// key returns to the cockpit.
	modeHandover
)

// Screen is the transcript-mode pager.
type Screen struct {
	Theme theme.Theme
	Tier  theme.Tier

	// conv is the conversation snapshot. Block values re-render at any
	// width; the snapshot's own viewport state is unused here.
	conv   conv.Model
	keys   *keymap.Map
	mode   mode
	notice string

	rows       []string // plain-text rows of the expanded conversation
	promptRows []int    // first row of each user prompt block
	dropped    int

	offset        int
	width, height int

	search searchState
}

// NewPager builds the pager over a conversation snapshot.
func NewPager(t theme.Theme, tier theme.Tier, snapshot conv.Model) Screen {
	s := Screen{
		Theme: t, Tier: tier,
		conv: snapshot,
		keys: keymap.New(keymap.Default()),
		mode: modePager,
	}
	s.rebuild()
	return s
}

func (s Screen) Init() tea.Cmd { return nil }

// ViewFlags reports the handover contract: the pager holds the alternate
// screen except while the conversation is written into native
// scrollback, where holding it would hide exactly what was written.
func (s Screen) ViewFlags() app.ViewFlags {
	return app.ViewFlags{AltScreen: s.mode == modePager}
}

// droppedLine matches the head line Dump writes, so the pager view and
// the scrollback dump agree on what was truncated.
func droppedLine(n int) string {
	return "[" + itoa(n) + " earlier blocks dropped from this transcript]"
}

// rebuild derives the pager's plain-text rows from the snapshot at the
// current width. Every block is expanded: a collapse is a cockpit view
// state, and search must reach text a collapse hides.
func (s *Screen) rebuild() {
	parts := make([]string, 0, len(s.rows))
	prompts := make([]int, 0, len(s.promptRows))
	row := 0
	if n := s.conv.Dropped(); n > 0 {
		parts = append(parts, droppedLine(n))
		row = 1
	}
	for _, b := range s.conv.Blocks() {
		if b.Kind == uievent.KindTurnStart {
			prompts = append(prompts, row)
		}
		b.Collapsed = false
		b.Focused = false
		part := ansi.Strip(b.Render(s.Theme, s.Tier, s.width))
		row += strings.Count(part, "\n") + 1
		parts = append(parts, part)
	}
	s.rows = strings.Split(strings.Join(parts, "\n"), "\n")
	s.promptRows = prompts
	s.dropped = s.conv.Dropped()
	s.clamp()
}

// contentHeight is the row count above the status line.
func (s Screen) contentHeight() int {
	h := s.height - 1
	if h < 1 {
		return 1
	}
	return h
}

func (s Screen) maxOffset() int {
	if over := len(s.rows) - s.contentHeight(); over > 0 {
		return over
	}
	return 0
}

func (s *Screen) clamp() {
	if s.offset > s.maxOffset() {
		s.offset = s.maxOffset()
	}
	if s.offset < 0 {
		s.offset = 0
	}
}

// scrollBy moves by whole rows and keeps the offset inside the
// conversation.
func (s *Screen) scrollBy(delta int) {
	s.offset += delta
	s.clamp()
}

// jumpToRow puts the given row at the top of the view, when it is not
// already visible.
func (s *Screen) jumpToRow(row int) {
	if row >= s.offset && row < s.offset+s.contentHeight() {
		return
	}
	s.offset = row - s.contentHeight()/3
	s.clamp()
}

// Update handles keys, the wheel, resizes, and the completion messages
// of the two handover commands.
func (s Screen) Update(msg tea.Msg) (app.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.conv.SetSize(msg.Width, 1)
		s.rebuild()
		return s, nil
	case tea.MouseWheelMsg:
		step := uikitconfig.CockpitScrollLines
		if msg.Button == tea.MouseWheelUp {
			step = -step
		}
		s.scrollBy(step)
		return s, nil
	case editorDoneMsg:
		s.notice = editorNotice(msg)
		return s, nil
	case handedOverMsg:
		if msg.err != nil {
			// The terminal could not be released. A handover that
			// prints nothing is worse than one absent: say why and stay
			// in the pager.
			s.mode = modePager
			s.notice = "could not write the transcript to scrollback: " + msg.err.Error()
		}
		return s, nil
	case uievent.EventMsg:
		return s.handleEvent(msg.Event)
	case conv.FlushMsg:
		var cmd tea.Cmd
		s.conv, cmd = s.conv.Update(msg)
		return s, cmd
	case tea.KeyPressMsg:
		return s.key(msg)
	}
	return s, nil
}

// handleEvent applies one event to the conversation model and updates the view.
func (s Screen) handleEvent(ev uievent.Event) (app.Screen, tea.Cmd) {
	oldBlocks := s.conv.Blocks()
	oldDropped := s.dropped
	nextConv, cmd := s.conv.HandleEvent(ev)
	s.conv = nextConv
	newDropped := s.conv.Dropped()
	if dropped := newDropped - oldDropped; dropped > 0 {
		droppedRows := 0
		for i := 0; i < dropped && i < len(oldBlocks); i++ {
			b := oldBlocks[i]
			b.Collapsed = false
			b.Focused = false
			part := ansi.Strip(b.Render(s.Theme, s.Tier, s.width))
			droppedRows += strings.Count(part, "\n") + 1
		}
		shift := droppedRows
		if oldDropped == 0 && newDropped > 0 {
			shift--
		}
		s.offset -= shift
		if s.offset < 0 {
			s.offset = 0
		}
		if s.search.restore > 0 {
			s.search.restore -= shift
			if s.search.restore < 0 {
				s.search.restore = 0
			}
		}
	}
	s.rebuild()
	if s.search.query != "" {
		s.search.find(s.rows)
	}
	return s, cmd
}

// key routes one key press: handover mode takes any key back to the
// pager; an open search bar takes typing; otherwise the pager keymap
// dispatches.
func (s Screen) key(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	if s.mode == modeHandover {
		s.mode = modePager
		s.notice = ""
		return s, nil
	}
	if s.search.active {
		return s.searchKey(msg)
	}
	id, ok := s.keys.Match(keymap.ContextPager, msg.String())
	if !ok {
		s.notice = ""
		return s, nil
	}
	return s.action(id)
}

func (s Screen) action(id keymap.ID) (app.Screen, tea.Cmd) {
	s.notice = ""
	switch id {
	case keymap.IDLeavePager:
		return s, func() tea.Msg { return app.PopScreenMsg{} }
	case keymap.IDSearchStart:
		s.search.begin(s.offset)
		return s, nil
	case keymap.IDSearchNext:
		s.search.step(1)
		if cur, ok := s.search.currentMatch(); ok {
			s.jumpToRow(cur.row)
		}
		return s, nil
	case keymap.IDSearchPrev:
		s.search.step(-1)
		if cur, ok := s.search.currentMatch(); ok {
			s.jumpToRow(cur.row)
		}
		return s, nil
	case keymap.IDPagerRowUp:
		s.scrollBy(-1)
	case keymap.IDPagerRowDown:
		s.scrollBy(1)
	case keymap.IDPagerTop:
		s.offset = 0
	case keymap.IDPagerBottom:
		s.offset = s.maxOffset()
	case keymap.IDPagerPromptUp:
		if row, ok := prevPrompt(s.promptRows, s.offset); ok {
			s.jumpToRow(row)
		}
	case keymap.IDPagerPromptDn:
		if row, ok := nextPrompt(s.promptRows, s.offset+s.contentHeight()); ok {
			s.jumpToRow(row)
		}
	case keymap.IDPagerHalfUp:
		s.scrollBy(-s.contentHeight() / 2)
	case keymap.IDPagerHalfDown:
		s.scrollBy(s.contentHeight() / 2)
	case keymap.IDPagerFullUp:
		s.scrollBy(-s.contentHeight())
	case keymap.IDPagerFullDown:
		s.scrollBy(s.contentHeight())
	case keymap.IDDumpScrollback:
		return s.dumpScrollback()
	case keymap.IDEditTranscript:
		return s.openEditor()
	}
	return s, nil
}

// prevPrompt is the newest prompt row at or above the top visible row.
func prevPrompt(prompts []int, top int) (int, bool) {
	for i := len(prompts) - 1; i >= 0; i-- {
		if prompts[i] < top {
			return prompts[i], true
		}
	}
	return 0, false
}

// nextPrompt is the oldest prompt row at or below the bottom visible row.
func nextPrompt(prompts []int, bottom int) (int, bool) {
	for _, p := range prompts {
		if p >= bottom {
			return p, true
		}
	}
	return 0, false
}

// View draws the pager: a window of conversation rows with every search
// match highlighted, and one status line. The status line is search bar
// and match count while a search is open, and the key hint otherwise.
func (s Screen) View() string {
	if s.mode == modeHandover {
		return render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).
			Render("transcript written to the terminal scrollback - press any key to return")
	}
	content := s.contentHeight()
	out := make([]string, 0, content+1)
	for i := 0; i < content; i++ {
		row := s.offset + i
		if row >= len(s.rows) {
			out = append(out, "")
			continue
		}
		out = append(out, s.renderRow(row))
	}
	out = append(out, s.statusLine())
	return strings.Join(out, "\n")
}

// statusLine is the bottom row: the search bar while typing, a notice
// after the editor returns, or the standing key hint.
func (s Screen) statusLine() string {
	if s.search.active {
		return render.Role(s.Theme, s.Tier, theme.RoleAccent).
			Render("/"+s.search.query) + "  " + s.search.statusText()
	}
	if s.notice != "" {
		return render.Role(s.Theme, s.Tier, theme.RoleWarning).Render(s.notice)
	}
	hint := "row " + itoa(min(s.offset+1, len(s.rows))) + " of " + itoa(len(s.rows)) +
		"  " + s.keys.Hint(keymap.IDSearchStart, keymap.IDDumpScrollback, keymap.IDEditTranscript, keymap.IDLeavePager)
	if n := s.search.count(); n > 0 {
		hint = itoa(n) + " matches  n/N:next  " + hint
	}
	return render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).Render(hint)
}

// itoa is the same tiny integer formatter the composer uses: no fmt on a
// per-row render path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
