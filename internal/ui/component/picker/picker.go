// Package picker is a generic, minimal list picker. It supports two
// modes: a flat list of items, and a list grouped by an optional
// provider-style header per group. The cursor walks items only;
// headers are non-selectable. With a non-empty filter, the picker
// keeps every group whose name or any model name matches the
// (case-insensitive) needle.
//
// The picker is the shared building block for /model (one provider or
// many) and /agents (single flat list). The ProviderModelGroup
// metadata carried by the config catalog (Active, Selectable,
// DisabledReason) is intentionally not rendered here: Selectable
// gates the catalog before it reaches the picker, Active renders in
// the top bar, and DisabledReason is a config-time error.
package picker

import (
	"slices"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// Group is one header-and-items block in the picker. An empty
// Provider renders the items without a header, so a single Group with
// Provider "" is a flat list.
type Group struct {
	Provider string
	Models   []string
}

type itemKind int

const (
	itemKindHeader itemKind = iota
	itemKindModel
)

type item struct {
	kind itemKind
	text string
}

// Model is a single-component picker. Construct it with New for a
// flat list, or with NewGroups for a list of groups. NewGroups with
// one Group whose Provider is "" renders a flat list.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	// items is the currently visible (filter-narrowed) list. The full
	// source list is rebuilt from lastFlat or lastGroups whenever a
	// filter changes.
	items      []item
	filter     string
	cursor     int
	lastGroups []Group
	lastFlat   []string
}

// New returns a picker over a flat list of items with the cursor on
// the first row.
func New(t theme.Theme, tier theme.Tier, items []string) Model {
	cloned := slices.Clone(items)
	its := make([]item, len(cloned))
	for i, s := range cloned {
		its[i] = item{kind: itemKindModel, text: s}
	}
	return Model{Theme: t, Tier: tier, items: its, lastFlat: cloned}
}

// NewGroups returns a picker over grouped items. Empty groups
// (Models: nil) are dropped. A group with an empty Provider renders
// without a header, so a single anonymous group acts as a flat list.
// The cursor starts on the first model item.
func NewGroups(t theme.Theme, tier theme.Tier, groups []Group) Model {
	cloned := slices.Clone(groups)
	its := make([]item, 0)
	for _, g := range cloned {
		if len(g.Models) == 0 {
			continue
		}
		if g.Provider != "" {
			its = append(its, item{kind: itemKindHeader, text: g.Provider})
		}
		for _, m := range g.Models {
			its = append(its, item{kind: itemKindModel, text: m})
		}
	}
	return Model{Theme: t, Tier: tier, items: its, cursor: firstModelIndex(its), lastGroups: cloned}
}

// Rebind swaps the item set, keeping the filter and clamping the
// cursor to a model item. A live-updating list (the files panel)
// refreshes its rows through this, so an in-progress filter survives
// a rebuild that picker.New would reset.
func (m *Model) Rebind(items []string) {
	savedFilter := m.filter
	savedCursor := m.cursor
	*m = New(m.Theme, m.Tier, items)
	m.filter = savedFilter
	m.lastGroups = nil
	if m.filter != "" {
		*m = m.applyFilter(m.filter)
	}
	if savedCursor < len(m.items) {
		m.cursor = savedCursor
	} else {
		m.cursor = firstModelIndex(m.items)
	}
	if m.cursor == 0 && len(m.items) > 0 && m.items[0].kind == itemKindHeader {
		m.cursor = firstModelIndex(m.items)
	}
}

// RebindGroups swaps the grouped item set, keeping the filter and
// clamping the cursor to a model item.
func (m *Model) RebindGroups(groups []Group) {
	savedFilter := m.filter
	savedCursor := m.cursor
	*m = NewGroups(m.Theme, m.Tier, groups)
	m.filter = savedFilter
	if m.filter != "" {
		*m = m.applyFilter(m.filter)
	}
	if savedCursor < len(m.items) && m.items[savedCursor].kind == itemKindModel {
		m.cursor = savedCursor
	} else {
		m.cursor = firstModelIndex(m.items)
	}
}

// Filter exposes the active filter: a caller that rebinds and holds a
// cursor by row index needs to know whether that row is even visible.
func (m Model) Filter() string { return m.filter }

// ClearFilter drops the active filter and resets the cursor to the
// first model item. Owners that dismiss the list (or hand focus away
// from it) use it so a stale filter cannot resurface as an
// unexplained short list.
func (m *Model) ClearFilter() {
	m.filter = ""
	*m = m.applyFilter("")
}

// CursorRow is the cursor's row within the visible list, so a caller
// that windows the list into a pane can keep the highlighted row on
// screen.
func (m Model) CursorRow() int { return m.cursor }

// MoveTo places the cursor on the row at index, clamped to the
// visible list. Callers that rebuild a picker (a live-updating list)
// use it to hold the selection steady across the rebuild.
func (m *Model) MoveTo(index int) {
	vLen := len(m.items)
	if vLen == 0 {
		m.cursor = 0
		return
	}
	if index < 0 {
		index = 0
	}
	if index >= vLen {
		index = vLen - 1
	}
	m.cursor = index
}

// Selected returns the currently highlighted item and whether the
// (visible) list has a model under the cursor. Headers are not
// selectable, so the bool is false whenever the cursor sits on a
// header.
func (m Model) Selected() (string, bool) {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return "", false
	}
	it := m.items[m.cursor]
	if it.kind != itemKindModel {
		return "", false
	}
	return it.text, true
}

// SelectMsg is emitted when the user confirms a selection with Enter.
type SelectMsg struct{ Item string }

// CancelMsg is emitted when the user aborts with Esc.
type CancelMsg struct{}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case "enter":
		if item, ok := m.Selected(); ok {
			return m, func() tea.Msg { return SelectMsg{Item: item} }
		}
	case "esc":
		return m, func() tea.Msg { return CancelMsg{} }
	case "backspace":
		if m.filter != "" {
			_, size := utf8.DecodeLastRuneInString(m.filter)
			m.filter = m.filter[:len(m.filter)-size]
			m = m.applyFilter(m.filter)
		}
	default:
		if key.Text != "" {
			m.filter += key.Text
			m = m.applyFilter(m.filter)
		}
	}
	return m, nil
}

func (m Model) View() string {
	var b strings.Builder
	for i, it := range m.items {
		switch it.kind {
		case itemKindHeader:
			b.WriteString(render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render(it.text))
			b.WriteByte('\n')
		case itemKindModel:
			style, prefix := render.Role(m.Theme, m.Tier, theme.RoleFG), "  "
			if i == m.cursor {
				style = render.WithBg(style, m.Theme, m.Tier, theme.RoleBGSelection)
				prefix = "> "
			}
			b.WriteString(style.Render(prefix + it.text))
			b.WriteByte('\n')
		}
	}
	if m.filter != "" {
		b.WriteString(render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render("/" + m.filter))
	}
	return strings.TrimRight(b.String(), "\n")
}

// applyFilter rebuilds items from the source list (lastFlat for a
// flat picker, lastGroups for a grouped picker). The cursor is
// clamped to the first model item in the rebuilt list.
func (m Model) applyFilter(filter string) Model {
	if m.lastGroups != nil {
		return m.applyFilterGroups(filter)
	}
	return m.applyFilterFlat(filter)
}

func (m Model) applyFilterFlat(filter string) Model {
	needle := strings.ToLower(filter)
	out := make([]item, 0, len(m.lastFlat))
	for _, s := range m.lastFlat {
		if needle == "" || strings.Contains(strings.ToLower(s), needle) {
			out = append(out, item{kind: itemKindModel, text: s})
		}
	}
	m.items = out
	m.cursor = firstModelIndex(out)
	return m
}

func (m Model) applyFilterGroups(filter string) Model {
	needle := strings.ToLower(filter)
	out := make([]item, 0)
	for _, g := range m.lastGroups {
		if len(g.Models) == 0 {
			continue
		}
		providerMatches := needle != "" && strings.Contains(strings.ToLower(g.Provider), needle)
		models := make([]string, 0, len(g.Models))
		for _, m := range g.Models {
			if needle == "" || providerMatches || strings.Contains(strings.ToLower(m), needle) {
				models = append(models, m)
			}
		}
		if len(models) == 0 {
			continue
		}
		if g.Provider != "" {
			out = append(out, item{kind: itemKindHeader, text: g.Provider})
		}
		for _, name := range models {
			out = append(out, item{kind: itemKindModel, text: name})
		}
	}
	m.items = out
	m.cursor = firstModelIndex(out)
	return m
}

func firstModelIndex(items []item) int {
	for i, it := range items {
		if it.kind == itemKindModel {
			return i
		}
	}
	return 0
}
