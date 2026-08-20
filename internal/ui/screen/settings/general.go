package settings

import (
	"context"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/field"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// generalRow pairs one rendered field with the edit its current value
// produces. All seven General settings are KindChoice - even scroll
// lines, as a short preset list - so the section needs no separate
// edit/commit mode: space (or enter) cycles the highlighted row AND
// applies it in the same key press, matching the plan's "space:
// toggle" for the common boolean case and extending it uniformly to
// every row rather than special-casing text input for one field
// (settings-screen.md §7 keeps KindText for a section that actually
// needs free text, e.g. Models' base_url in a later slice).
type generalRow struct {
	f     field.Model
	apply func(value string) ports.GeneralEdit
}

// generalSection is the General settings section.
type generalSection struct {
	store         ports.GeneralSettings
	theme         theme.Theme
	tier          theme.Tier
	width, height int

	rows   []generalRow
	cursor int
	notice string
}

func newGeneralSection(store ports.GeneralSettings) *generalSection {
	return &generalSection{store: store}
}

func (s *generalSection) Title() string { return "General" }

func (s *generalSection) SetSize(w, h int) {
	s.width, s.height = w, h
	for i := range s.rows {
		s.rows[i].f.SetWidth(w)
	}
}

func (s *generalSection) SetTheme(t theme.Theme, tier theme.Tier) {
	s.theme, s.tier = t, tier
	for i := range s.rows {
		s.rows[i].f.SetTheme(t, tier)
	}
	if s.store != nil && len(s.rows) == 0 {
		s.rebuild()
	}
}

// boolChoice projects a bool onto the two-value "on"/"off" set every
// boolean row shares.
func boolChoice(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// scrollChoices is a short preset rather than free text: a scroll-lines
// value outside a sane range is a UX bug, not a setting anyone wants,
// so the field cannot represent one.
var scrollChoices = []string{"1", "2", "3", "5", "8"}

// rebuild (re)creates every row from the store's current values. It
// runs once at construction and again after every successful save, so
// a row that failed to apply still shows the last CONFIRMED value, not
// an optimistic local guess.
func (s *generalSection) rebuild() {
	v := s.store.General()
	mk := func(label string) field.Model { return field.New(s.theme, s.tier, label, field.KindChoice, s.width) }

	themeF := mk("theme")
	themeF.SetChoices([]string{"mivia-dark", "mivia-light", "mivia-high-contrast"}, v.Theme)

	mouseF := mk("mouse capture")
	mouseF.SetChoices([]string{"on", "off"}, boolChoice(v.Mouse))

	reasonF := mk("show reasoning")
	reasonF.SetChoices([]string{"on", "off"}, boolChoice(v.ShowReasoning))

	scrollF := mk("scroll lines")
	scrollF.SetChoices(scrollChoices, strconv.Itoa(v.ScrollLines))

	approvalF := mk("approval default")
	approvalF.SetChoices([]string{"once", "always", "deny", "deny_always"}, v.ApprovalDefault)

	srF := mk("screen reader")
	srF.SetChoices([]string{"on", "off"}, boolChoice(v.ScreenReader))

	rmF := mk("reduced motion")
	rmF.SetChoices([]string{"on", "off"}, boolChoice(v.ReducedMotion))

	s.rows = []generalRow{
		{themeF, func(val string) ports.GeneralEdit { return ports.SetTheme{Name: val} }},
		{mouseF, func(val string) ports.GeneralEdit { return ports.SetMouse{On: val == "on"} }},
		{reasonF, func(val string) ports.GeneralEdit { return ports.SetShowReasoning{On: val == "on"} }},
		{scrollF, func(val string) ports.GeneralEdit {
			n, _ := strconv.Atoi(val) // val is always one of scrollChoices; Atoi cannot fail
			return ports.SetScrollLines{N: n}
		}},
		{approvalF, func(val string) ports.GeneralEdit { return ports.SetApprovalDefault{Mode: val} }},
		{srF, func(val string) ports.GeneralEdit { return ports.SetScreenReader{On: val == "on"} }},
		{rmF, func(val string) ports.GeneralEdit { return ports.SetReducedMotion{On: val == "on"} }},
	}
	if s.cursor >= len(s.rows) {
		s.cursor = len(s.rows) - 1
	}
}

// generalSavedMsg/generalFailedMsg are what awaitSave's Cmd yields once
// a SaveHandle finishes - the section's own small async result, kept
// local rather than routed through the Screen, since only this section
// cares about its own save outcome.
type generalSavedMsg struct{}
type generalFailedMsg struct{ message string }

// awaitSave blocks the returned Cmd on handle's channel until it
// closes, then reports the last event's outcome. A SaveHandle's
// contract guarantees a terminal Saved or Failed event before close
// (settings-screen.md §4), so the loop always has a last state to
// report.
func awaitSave(handle ports.SaveHandle) tea.Cmd {
	return func() tea.Msg {
		var last ports.SaveEvent
		for ev := range handle.Events() {
			last = ev
		}
		if last.State == ports.SaveFailed {
			return generalFailedMsg{message: last.Message}
		}
		return generalSavedMsg{}
	}
}

func (s *generalSection) Update(msg tea.Msg) (section, tea.Cmd) {
	switch msg := msg.(type) {
	case generalSavedMsg:
		s.notice = ""
		s.rebuild()
		return s, nil
	case generalFailedMsg:
		s.notice = msg.message
		return s, nil
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	}
	return s, nil
}

func (s *generalSection) handleKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
	if s.store == nil || len(s.rows) == 0 {
		return s, nil
	}
	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(s.rows)-1 {
			s.cursor++
		}
	case "space", "enter":
		return s.commit()
	}
	return s, nil
}

// commit cycles the highlighted row to its next value and applies it
// immediately - see the type's own doc comment for why there is no
// separate preview step.
func (s *generalSection) commit() (section, tea.Cmd) {
	row := &s.rows[s.cursor]
	row.f.Cycle(1)
	edit := row.apply(row.f.Value())
	handle, err := s.store.Apply(context.Background(), ports.ScopeUser, edit)
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitSave(handle)
}

func (s *generalSection) View() string {
	if s.store == nil {
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("General is unavailable.")
	}
	var b []byte
	for i, row := range s.rows {
		line := row.f.View()
		if i == s.cursor {
			line = "> " + line
		} else {
			line = "  " + line
		}
		b = append(b, line...)
		b = append(b, '\n')
	}
	if s.notice != "" {
		b = append(b, render.Role(s.theme, s.tier, theme.RoleWarning).Render(s.notice)...)
	}
	return string(b)
}

func (s *generalSection) Hints() []keymap.ID {
	return []keymap.ID{keymap.IDSettingsUp, keymap.IDSettingsDown, keymap.IDSettingsToggle}
}
