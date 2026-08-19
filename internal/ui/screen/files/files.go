// Package files is the cockpit's Files tab: a two-pane browser over the
// files this session touched, lazygit-shaped - the left pane lists the
// files, the right pane shows the selected file's diff or source, and
// moving the list selection drives the right pane immediately.
//
// The screen takes a VALUE snapshot of the session's file list (the
// same pattern the transcript pager uses): files touched while the tab
// is open appear after re-entering the tab, not live. Phase 1.
package files

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Kind is how the session touched a file.
type Kind string

const (
	KindEdited  Kind = "edited"
	KindCreated Kind = "created"
	KindDeleted Kind = "deleted"
)

// Entry is one file the session touched.
type Entry struct {
	Path string
	Kind Kind
	Diff uievent.Diff
}

// NewEntry derives an Entry from a tool-end diff. Deleted is stated by
// the diff itself; a diff with no removals is a creation (hunks that
// only add carry no previous content); everything else is an edit.
func NewEntry(d uievent.Diff) Entry {
	k := KindEdited
	switch {
	case d.Deleted:
		k = KindDeleted
	case d.Removed == 0:
		k = KindCreated
	}
	return Entry{Path: d.Path, Kind: k, Diff: d}
}

// Model is the Files tab screen.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	entries []Entry
	list    picker.Model

	// sourceView flips the right pane between the diff (default) and
	// the full post-edit source.
	sourceView bool

	// offset windows the right pane; the left pane is a picker and
	// windows itself.
	offset int

	// modalOpen is the narrow-width fallback: below the wide
	// breakpoint the list is the tab, and selecting a file opens the
	// content as a centered dialog instead of a side pane.
	modalOpen bool

	keys   *keymap.Map
	width  int
	height int
}

// New builds the Files tab over the session's touched files. Entries
// with the same path collapse to the latest: the tab answers "what is
// the state of this file", not "what is the history".
func New(t theme.Theme, tier theme.Tier, entries []Entry) Model {
	latest := make([]Entry, 0, len(entries))
	idx := map[string]int{}
	for _, e := range entries {
		if i, ok := idx[e.Path]; ok {
			latest[i] = e
			continue
		}
		idx[e.Path] = len(latest)
		latest = append(latest, e)
	}
	names := make([]string, len(latest))
	for i, e := range latest {
		names[i] = e.rowLabel()
	}
	return Model{
		Theme: t, Tier: tier,
		entries: latest,
		list:    picker.New(t, tier, names),
		keys:    keymap.New(keymap.Default()),
	}
}

// appendLive folds one more observed diff into the list, rebuilding the
// picker over the collapsed entries while holding the selection on the
// same path: a live update must not move the user's cursor.
func (m *Model) appendLive(d uievent.Diff) {
	sel, _ := m.list.Selected()
	selPath := strings.Split(sel, "  ")[0]
	for i, e := range m.entries {
		if e.Path == d.Path {
			m.entries[i] = NewEntry(d)
			m.refreshList(selPath)
			return
		}
	}
	m.entries = append(m.entries, NewEntry(d))
	m.refreshList(selPath)
}

// refreshList rebuilds the picker over the entries, keeping the cursor
// on the path that was selected (or the first row when it vanished).
func (m *Model) refreshList(keepPath string) {
	names := make([]string, len(m.entries))
	for i, e := range m.entries {
		names[i] = e.rowLabel()
	}
	theme, tier := m.list.Theme, m.list.Tier
	m.list = picker.New(theme, tier, names)
	if keepPath == "" {
		return
	}
	for i, e := range m.entries {
		if e.Path == keepPath {
			m.list.MoveTo(i)
			return
		}
	}
}

// Entries exposes the collapsed file list for tests and callers that
// need the derived data without rendering.
func (m Model) Entries() []Entry { return m.entries }

func (m Model) rowLabelFor(path string) string {
	for _, e := range m.entries {
		if e.Path == path {
			return e.rowLabel()
		}
	}
	return path
}

func (e Entry) rowLabel() string {
	return e.Path + "  " + string(e.Kind)
}

func (m Model) Init() tea.Cmd { return nil }

// ViewFlags holds the alternate screen like every cockpit tab.
func (m Model) ViewFlags() app.ViewFlags { return app.ViewFlags{AltScreen: true} }

func (m Model) Update(msg tea.Msg) (app.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case uievent.EventMsg:
		// The router broadcasts stream events to every screen on the
		// stack, so the tab observes edits LIVE while it is open - not
		// only the snapshot it was pushed with.
		if end, ok := msg.Event.Body.(uievent.ToolEndBody); ok && end.Diff != nil {
			m.appendLive(*end.Diff)
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.offset = 0
		return m, nil
	case app.ThemeChangedMsg:
		m.Theme, m.Tier = msg.Theme, msg.Tier
		m.list.Theme, m.list.Tier = msg.Theme, msg.Tier
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	if id, ok := m.keys.Match(keymap.ContextGlobal, msg.String()); ok && id == keymap.IDTabNext {
		// ctrl+n cycles tabs: from Files, back to chat.
		return m, func() tea.Msg { return app.PopScreenMsg{} }
	}
	if m.modalOpen {
		// The modal shows one file; any key returns to the list, the
		// same dismissal rule the help overlay uses. The toggle key
		// still works inside it.
		if id, ok := m.keys.Match(keymap.ContextFiles, msg.String()); ok && id == keymap.IDFileToggleView {
			m.sourceView = !m.sourceView
			m.offset = 0
			return m, nil
		}
		m.modalOpen = false
		return m, nil
	}
	if id, ok := m.keys.Match(keymap.ContextFiles, msg.String()); ok {
		switch id {
		case keymap.IDFileToggleView:
			m.sourceView = !m.sourceView
			m.offset = 0
			return m, nil
		case keymap.IDPagerHalfUp:
			m.offset -= m.contentHeight() / 2
			m.clampOffset()
			return m, nil
		case keymap.IDPagerHalfDown:
			m.offset += m.contentHeight() / 2
			m.clampOffset()
			return m, nil
		}
		// up/down/k/j move the LIST selection; the right pane follows
		// because View derives from it. The picker answers the arrows
		// itself; the less spellings it does not know, so translate.
		switch id {
		case keymap.IDPagerRowUp:
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case keymap.IDPagerRowDown:
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		}
	}
	// Everything else feeds the list: arrows move the selection, typing
	// filters, and the right pane follows because View derives from the
	// selection.
	next, cmd := m.list.Update(msg)
	m.list = next
	m.offset = 0
	// Narrow terminals have no side pane: a completed selection (Enter)
	// opens the content modal instead.
	if m.width > 0 && m.width < uikitconfig.BreakpointWide && cmd != nil {
		if _, ok := cmd().(picker.SelectMsg); ok {
			m.modalOpen = true
		}
	}
	return m, nil
}

func (m Model) clampOffset() {
	max := len(m.contentRows()) - m.contentHeight()
	if max < 0 {
		max = 0
	}
	if m.offset > max {
		m.offset = max
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// contentHeight is the rows the panes have: the terminal minus the top
// bar and the status row.
func (m Model) contentHeight() int {
	h := m.height - 2
	if h < 1 {
		h = 1
	}
	return h
}

// selected returns the entry the list highlights.
func (m Model) selected() (Entry, bool) {
	name, ok := m.list.Selected()
	if !ok {
		return Entry{}, false
	}
	for _, e := range m.entries {
		if e.rowLabel() == name {
			return e, true
		}
	}
	return Entry{}, false
}

// contentRows is the right pane's full content: the selected file's
// diff or its post-edit source.
func (m Model) contentRows() []string {
	e, ok := m.selected()
	if !ok {
		if len(m.entries) == 0 {
			return []string{"no files touched yet"}
		}
		return nil
	}
	if m.sourceView {
		if len(e.Diff.After) == 0 {
			return []string{"source not available for this edit"}
		}
		return e.Diff.After
	}
	if lines := render.DiffLines(m.Theme, m.Tier, e.Diff); len(lines) > 0 {
		return lines
	}
	return []string{"no changes recorded"}
}

// View draws the tab: top bar with the tab strip, then the panes, then
// the status row.
func (m Model) View() string {
	// The tab's bar carries the tab strip and no session values: it is
	// a derived view, and the conversation tab's bar holds the real
	// model and usage. Zeros render as just the mark and wordmark.
	bar := topbar.New(m.Theme, m.Tier, ports.ModelInfo{}, ports.Usage{}, m.width)
	bar.SetTabs([]string{"chat", "files"}, 1)

	rows := m.contentRows()
	end := m.offset + m.paneHeight()
	if end > len(rows) {
		end = len(rows)
	}
	if m.offset > end {
		m.offset = end
	}
	window := strings.Join(rows[m.offset:end], "\n")

	var body string
	switch {
	case m.width >= uikitconfig.BreakpointWide:
		body = render.Split(m.Theme, m.Tier, m.width, m.paneHeight(), render.Left,
			m.listView(), window)
	case m.modalOpen:
		// Below the wide breakpoint there is no room for two panes:
		// the list is the tab, and a selected file's content opens as a
		// centered dialog - the same primitive /help and the pickers
		// use. Any key closes it back to the list.
		title := "files"
		if e, ok := m.selected(); ok {
			title = e.Path
		}
		body = render.Dialog(m.Theme, m.Tier, m.width, m.paneHeight(), title, window, "d diff/source  any key closes")
	default:
		body = m.listView()
	}
	out := bar.View() + "\n" + body + "\n" + m.statusRow()
	return out
}

// paneHeight is the inner height of a pane: content height minus the
// border rows the split or dialog draws.
func (m Model) paneHeight() int {
	return m.contentHeight() - 2
}

func (m Model) listView() string {
	if len(m.entries) == 0 {
		return "no files touched yet"
	}
	return m.list.View()
}

func (m Model) statusRow() string {
	hint := m.keys.Hint(keymap.IDFileToggleView, keymap.IDTabNext)
	return render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).
		Render(fmt.Sprintf("row %d of %d  %s", m.offset+1, len(m.contentRows()), hint))
}
