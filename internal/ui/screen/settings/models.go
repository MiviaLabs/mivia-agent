package settings

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// modelsRow is one flattened line of the Models section: either a
// provider header or one of its models, indented under it. Flattening
// both kinds into one cursor-addressable list (rather than a nested
// provider-cursor/model-cursor pair) is what lets a single up/down pair
// move through the whole tree, matching General's one-cursor shape
// instead of inventing a second navigation model for this section
// alone.
type modelsRow struct {
	isProvider bool
	provider   ports.ProviderView
	model      ports.ModelView // zero value when isProvider
}

// modelsSection is the Models settings section.
//
// Scope for this slice: browse every provider and its models, activate
// a model, remove a model or a provider. Creating a new provider or
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
// state. It runs at first theme-set and again after every successful
// save, so the list never shows an optimistic guess about what the
// store actually holds.
func (s *modelsSection) rebuild() {
	providers := s.store.Providers()
	rows := make([]modelsRow, 0, len(providers)*2)
	for _, p := range providers {
		rows = append(rows, modelsRow{isProvider: true, provider: p})
		for _, m := range p.Models {
			rows = append(rows, modelsRow{provider: p, model: m})
		}
	}
	s.rows = rows
	if s.cursor >= len(s.rows) {
		s.cursor = len(s.rows) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
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
		if s.cursor > 0 {
			s.cursor--
		}
		s.notice = ""
	case "down", "j":
		if s.cursor < len(s.rows)-1 {
			s.cursor++
		}
		s.notice = ""
	case "enter", "space":
		return s.activate()
	case "x":
		return s.remove()
	case "n":
		s.notice = "adding a provider or model is not available in this build yet"
	}
	return s, nil
}

func (s *modelsSection) activate() (section, tea.Cmd) {
	row := s.rows[s.cursor]
	if row.isProvider {
		s.notice = "select a model under " + row.provider.Name + " to activate it"
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

func (s *modelsSection) remove() (section, tea.Cmd) {
	row := s.rows[s.cursor]
	var edit ports.ProviderEdit
	if row.isProvider {
		edit = ports.RemoveProvider{Name: row.provider.Name}
	} else {
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
	var b []byte
	for i, row := range s.rows {
		marker := "  "
		if i == s.cursor {
			marker = "> "
		}
		b = append(b, (marker + s.renderRow(row))...)
		b = append(b, '\n')
	}
	if s.notice != "" {
		b = append(b, render.Role(s.theme, s.tier, theme.RoleWarning).Render(s.notice)...)
	}
	return string(b)
}

func (s *modelsSection) renderRow(row modelsRow) string {
	if row.isProvider {
		return s.renderProviderRow(row.provider)
	}
	return s.renderModelRow(row.provider, row.model)
}

func (s *modelsSection) Hints() []keymap.ID {
	return []keymap.ID{keymap.IDSettingsUp, keymap.IDSettingsDown, keymap.IDSettingsSelect, keymap.IDSettingsDelete}
}
