package settings

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// modelsRow is one flattened line of the Models section: a group
// header ("Global" / "Project"), a provider header within a group, or
// one of that provider's models indented under it. Flattening every
// kind into one cursor-addressable list (rather than a nested
// group/provider/model cursor triple) is what lets a single up/down
// pair move through the whole tree, matching every other settings
// section's one-cursor shape instead of inventing a nested navigation
// model for this section alone.
type modelsRow struct {
	kind     modelsRowKind
	header   string // set when kind == modelsRowGroup
	provider ports.ProviderView
	model    ports.ModelView // zero value unless kind == modelsRowModel
}

type modelsRowKind int

const (
	modelsRowGroup modelsRowKind = iota
	modelsRowProvider
	modelsRowModel
)

// modelsSection is the Models settings section.
//
// Scope for this slice: browse every provider and its models, grouped
// by which config layer each row's own fields came from (see
// ports.ProviderView's doc comment - a provider with a project-scope
// default_model override appears once under each group), activate a
// model, set a model as default at either scope, clear a project
// override, remove a model or a provider. Creating a new provider or
// model needs a multi-field entry flow (name, base URL, API key env
// var name) that this slice deliberately does not build - it is real,
// separate work, not a corner cut silently; "n" is bound but reports a
// clear notice instead of doing nothing or crashing.
type modelsSection struct {
	store         ports.ProviderSettings
	theme         theme.Theme
	tier          theme.Tier
	width, height int

	rows   []modelsRow
	cursor int
	notice string
}

func newModelsSection(store ports.ProviderSettings) *modelsSection {
	return &modelsSection{store: store}
}

func (s *modelsSection) Title() string { return "Models" }

func (s *modelsSection) SetSize(w, h int) { s.width, s.height = w, h }

func (s *modelsSection) SetTheme(t theme.Theme, tier theme.Tier) {
	s.theme, s.tier = t, tier
	if s.store != nil && s.rows == nil {
		s.rebuild()
	}
}

// rebuild re-flattens the provider/model tree from the store's current
// state, grouped into a Global section (every ScopeUser row) and a
// Project section (every ScopeProject row - only providers with their
// own project default_model override produce one; see
// ports.ProviderView). It runs at first theme-set and again after
// every successful save, so the list never shows an optimistic guess
// about what the store actually holds.
func (s *modelsSection) rebuild() {
	providers := s.store.Providers()
	rows := make([]modelsRow, 0, len(providers)*2+2)

	rows = append(rows, s.groupRows("Global (user home)", providers, ports.ScopeUser)...)
	rows = append(rows, s.groupRows("Project (this workspace)", providers, ports.ScopeProject)...)

	s.rows = rows
	if s.cursor >= len(s.rows) {
		s.cursor = len(s.rows) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
	// A rebuild can leave the cursor sitting on a group header (e.g. the
	// provider it used to point at was just removed and the header slid
	// into its slot). up/down already refuse to STOP on a header (see
	// handleKey), so nudge forward once here the same way, then fall
	// back to the closest real row if that ran off the end.
	if len(s.rows) > 0 && s.rows[s.cursor].kind == modelsRowGroup {
		if nc, ok := s.nextSelectable(s.cursor, 1); ok {
			s.cursor = nc
		} else if nc, ok := s.nextSelectable(s.cursor, -1); ok {
			s.cursor = nc
		}
	}
}

// nextSelectable walks from start in direction dir (+1/-1) and returns
// the first row index that is not a modelsRowGroup header, or ok=false
// if the walk runs off the list without finding one (every row is a
// header - no providers configured at all).
func (s *modelsSection) nextSelectable(start, dir int) (int, bool) {
	for i := start; i >= 0 && i < len(s.rows); i += dir {
		if s.rows[i].kind != modelsRowGroup {
			return i, true
		}
	}
	return 0, false
}

// groupRows builds one scope group's rows: a header followed by every
// provider row at that scope and its models, or a "(none)" placeholder
// when no provider has a row at that scope - a Project group is empty
// whenever no provider in this config has its own project override,
// and an empty group must still say so rather than silently vanishing
// (a vanished group reads as "there is no Project scope", not "nothing
// is overridden here").
func (s *modelsSection) groupRows(header string, providers []ports.ProviderView, scope ports.Scope) []modelsRow {
	rows := []modelsRow{{kind: modelsRowGroup, header: header}}
	found := false
	for _, p := range providers {
		if p.Scope != scope {
			continue
		}
		found = true
		rows = append(rows, modelsRow{kind: modelsRowProvider, provider: p})
		for _, m := range p.Models {
			rows = append(rows, modelsRow{kind: modelsRowModel, provider: p, model: m})
		}
	}
	if !found {
		placeholder := "  (no project overrides set)"
		if scope == ports.ScopeUser {
			placeholder = "  (no providers configured)"
		}
		rows = append(rows, modelsRow{kind: modelsRowGroup, header: placeholder})
	}
	return rows
}

type modelsSavedMsg struct{}
type modelsFailedMsg struct{ message string }

func awaitModelsSave(handle ports.SaveHandle) tea.Cmd {
	return func() tea.Msg {
		var last ports.SaveEvent
		for ev := range handle.Events() {
			last = ev
		}
		if last.State == ports.SaveFailed {
			return modelsFailedMsg{message: last.Message}
		}
		return modelsSavedMsg{}
	}
}

func (s *modelsSection) Update(msg tea.Msg) (section, tea.Cmd) {
	switch msg := msg.(type) {
	case modelsSavedMsg:
		s.notice = ""
		s.rebuild()
		return s, nil
	case modelsFailedMsg:
		s.notice = msg.message
		return s, nil
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	}
	return s, nil
}

func (s *modelsSection) handleKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
	if s.store == nil || len(s.rows) == 0 {
		return s, nil
	}
	switch msg.String() {
	case "up", "k":
		s.moveCursor(-1)
		s.notice = ""
	case "down", "j":
		s.moveCursor(1)
		s.notice = ""
	case "enter", "space":
		return s.activate()
	case "d":
		return s.setDefault()
	case "p":
		return s.setProjectDefault()
	case "c":
		return s.clearProjectOverride()
	case "x":
		return s.remove()
	case "n":
		s.notice = "adding a provider or model is not available in this build yet"
	}
	return s, nil
}

// moveCursor steps s.cursor by dir (+1/-1), one row at a time, and
// skips any modelsRowGroup header it lands on by taking one more step
// in the same direction - group headers are visible but never a cursor
// stop. A step that would run off either end of the list is simply
// dropped, matching the plain-bounds-check behavior every other
// settings section's up/down already has.
func (s *modelsSection) moveCursor(dir int) {
	next := s.cursor + dir
	for next >= 0 && next < len(s.rows) && s.rows[next].kind == modelsRowGroup {
		next += dir
	}
	if next >= 0 && next < len(s.rows) {
		s.cursor = next
	}
}

// currentModelRow returns the model row under the cursor, or a notice
// (and no row) when the cursor is on a group header or provider row -
// every model-only action shares this same "select a model" guard.
func (s *modelsSection) currentModelRow(actionDesc string) (modelsRow, bool) {
	row := s.rows[s.cursor]
	switch row.kind {
	case modelsRowGroup:
		s.notice = "select a model to " + actionDesc
		return modelsRow{}, false
	case modelsRowProvider:
		s.notice = "select a model under " + row.provider.Name + " to " + actionDesc
		return modelsRow{}, false
	default:
		return row, true
	}
}

func (s *modelsSection) activate() (section, tea.Cmd) {
	row, ok := s.currentModelRow("activate it")
	if !ok {
		return s, nil
	}
	handle, err := s.store.Apply(context.Background(), ports.ScopeUser,
		ports.ActivateModel{Provider: row.provider.Name, Model: row.model.Name})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitModelsSave(handle)
}

// setDefault sets the focused model as the default for its OWN row's
// scope (see ports.SetDefaultModel): pressing "d" on a Global row's
// model writes the user config; on a Project row's model it writes the
// project config for a provider that already has its own project
// section (every Project-group row does, by construction of
// groupRows). Use "p" to create a NEW project override from a Global
// row instead.
func (s *modelsSection) setDefault() (section, tea.Cmd) {
	row, ok := s.currentModelRow("set it as default")
	if !ok {
		return s, nil
	}
	handle, err := s.store.Apply(context.Background(), row.provider.Scope,
		ports.SetDefaultModel{Provider: row.provider.Name, Model: row.model.Name})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitModelsSave(handle)
}

// setProjectDefault makes the focused model this project's override
// for its provider, regardless of which group the cursor is currently
// in (ports.SetProjectDefaultModel always targets the project file) -
// the action that turns a Global-only provider into one with its own
// Project row.
func (s *modelsSection) setProjectDefault() (section, tea.Cmd) {
	row, ok := s.currentModelRow("make it the project default")
	if !ok {
		return s, nil
	}
	handle, err := s.store.Apply(context.Background(), ports.ScopeProject,
		ports.SetProjectDefaultModel{Provider: row.provider.Name, Model: row.model.Name})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitModelsSave(handle)
}

// clearProjectOverride removes the focused provider's project-scope
// default_model override, reverting its effective default to the
// Global value. Valid from either group's row for that provider - a
// Global row with HasProjectOverride can clear the shadowing override
// without navigating to the Project group first.
func (s *modelsSection) clearProjectOverride() (section, tea.Cmd) {
	row := s.rows[s.cursor]
	var providerName string
	switch row.kind {
	case modelsRowGroup:
		s.notice = "select a provider or model to clear its project override"
		return s, nil
	case modelsRowProvider:
		providerName = row.provider.Name
	case modelsRowModel:
		providerName = row.provider.Name
	}
	if !row.provider.HasProjectOverride {
		s.notice = row.provider.Name + " has no project override to clear"
		return s, nil
	}
	handle, err := s.store.Apply(context.Background(), ports.ScopeProject,
		ports.ClearProjectDefaultModel{Provider: providerName})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitModelsSave(handle)
}

func (s *modelsSection) remove() (section, tea.Cmd) {
	row := s.rows[s.cursor]
	var edit ports.ProviderEdit
	switch row.kind {
	case modelsRowGroup:
		return s, nil
	case modelsRowProvider:
		edit = ports.RemoveProvider{Name: row.provider.Name}
	case modelsRowModel:
		edit = ports.RemoveModel{Provider: row.provider.Name, Model: row.model.Name}
	}
	handle, err := s.store.Apply(context.Background(), ports.ScopeUser, edit)
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitModelsSave(handle)
}

func (s *modelsSection) View() string {
	if s.store == nil {
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("Models is unavailable.")
	}
	lines := s.alignedRows()
	avail := s.height
	if s.notice != "" && avail > 1 {
		avail--
	}
	start, end := render.WindowSlice(len(lines), s.cursor, avail)

	var b []byte
	for i, line := range lines[start:end] {
		actualIdx := start + i
		marker := "  "
		if actualIdx == s.cursor && s.rows[actualIdx].kind != modelsRowGroup {
			marker = "> "
		}
		b = append(b, (marker + line)...)
		b = append(b, '\n')
	}
	if s.notice != "" {
		b = append(b, render.Role(s.theme, s.tier, theme.RoleWarning).Render(s.notice)...)
	}
	return string(b)
}

// alignedRows renders every row's cells and column-aligns providers and
// models SEPARATELY, each in its own original order: the two kinds
// carry different columns (a provider's key status vs a model's
// context window), so aligning them as one table would line up
// unrelated fields under one column. Group headers are not cells at
// all - they render as a single accent-styled line, like a skills
// section header.
func (s *modelsSection) alignedRows() []string {
	providerCells := make([][]string, 0, len(s.rows))
	modelCells := make([][]string, 0, len(s.rows))
	for _, row := range s.rows {
		switch row.kind {
		case modelsRowProvider:
			providerCells = append(providerCells, s.renderProviderCells(row.provider))
		case modelsRowModel:
			modelCells = append(modelCells, s.renderModelCells(row.provider, row.model))
		}
	}
	alignedProviders := render.Columns(rowGap, providerCells)
	alignedModels := render.Columns(rowGap, modelCells)

	lines := make([]string, len(s.rows))
	pi, mi := 0, 0
	for i, row := range s.rows {
		switch row.kind {
		case modelsRowGroup:
			lines[i] = s.renderGroupHeader(row.header)
		case modelsRowProvider:
			lines[i] = alignedProviders[pi]
			pi++
		case modelsRowModel:
			lines[i] = alignedModels[mi]
			mi++
		}
	}
	return lines
}

// renderGroupHeader draws a "Global"/"Project" section header in accent
// bold, matching the Skills section's own group-header treatment
// (skills_detail.go's headerText branch), except a "(none)" placeholder
// line (leading two spaces, same convention as skills.go's own empty-
// group rows) renders subtle instead.
func (s *modelsSection) renderGroupHeader(header string) string {
	if len(header) >= 2 && header[:2] == "  " {
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(header)
	}
	return render.Role(s.theme, s.tier, theme.RoleAccent).Bold(true).Render(header)
}

func (s *modelsSection) Hints() []keymap.ID {
	return []keymap.ID{
		keymap.IDSettingsUp, keymap.IDSettingsDown, keymap.IDSettingsSelect,
		keymap.IDSettingsDefault, keymap.IDSettingsProjectDefault, keymap.IDSettingsClearOverride,
		keymap.IDSettingsDelete,
	}
}
