package settings

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

type skillsRow struct {
	isHeader bool
	header   string
	skill    ports.SkillView
}

type skillsSection struct {
	store         ports.SkillSettings
	theme         theme.Theme
	tier          theme.Tier
	width, height int

	rows         []skillsRow
	skillIndices []int
	cursor       int
	notice       string
}

func newSkillsSection(store ports.SkillSettings) *skillsSection {
	return &skillsSection{store: store}
}

func (s *skillsSection) Title() string { return "Skills" }

func (s *skillsSection) SetSize(w, h int) { s.width, s.height = w, h }

func (s *skillsSection) SetTheme(t theme.Theme, tier theme.Tier) {
	s.theme, s.tier = t, tier
	if s.store != nil && s.rows == nil {
		s.rebuild()
	}
}

func (s *skillsSection) rebuild() {
	if s.store == nil {
		s.rows = nil
		s.skillIndices = nil
		return
	}
	all := s.store.Skills()
	var globalSkills, projectSkills []ports.SkillView
	for _, sk := range all {
		if sk.Origin == "user" {
			globalSkills = append(globalSkills, sk)
		} else {
			projectSkills = append(projectSkills, sk)
		}
	}

	rows := make([]skillsRow, 0, len(all)+4)
	indices := make([]int, 0, len(all))

	// Global Group (user home)
	rows = append(rows, skillsRow{isHeader: true, header: "Global Skills (~/.mivia/skills)"})
	if len(globalSkills) == 0 {
		rows = append(rows, skillsRow{isHeader: true, header: "  (no global skills installed)"})
	} else {
		for _, sk := range globalSkills {
			indices = append(indices, len(rows))
			rows = append(rows, skillsRow{skill: sk})
		}
	}

	// Project Group (workspace)
	rows = append(rows, skillsRow{isHeader: true, header: "Project Skills (.agents/skills)"})
	if len(projectSkills) == 0 {
		rows = append(rows, skillsRow{isHeader: true, header: "  (no project skills installed)"})
	} else {
		for _, sk := range projectSkills {
			indices = append(indices, len(rows))
			rows = append(rows, skillsRow{skill: sk})
		}
	}

	s.rows = rows
	s.skillIndices = indices
	if s.cursor >= len(s.skillIndices) {
		s.cursor = len(s.skillIndices) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

func (s *skillsSection) selectedSkill() (ports.SkillView, bool) {
	if len(s.skillIndices) == 0 || s.cursor < 0 || s.cursor >= len(s.skillIndices) {
		return ports.SkillView{}, false
	}
	rowIdx := s.skillIndices[s.cursor]
	return s.rows[rowIdx].skill, true
}

type skillsSavedMsg struct{}
type skillsFailedMsg struct{ message string }

func awaitSkillsSave(handle ports.SaveHandle) tea.Cmd {
	return func() tea.Msg {
		var last ports.SaveEvent
		for ev := range handle.Events() {
			last = ev
		}
		if last.State == ports.SaveFailed {
			return skillsFailedMsg{message: last.Message}
		}
		return skillsSavedMsg{}
	}
}

func (s *skillsSection) Update(msg tea.Msg) (section, tea.Cmd) {
	switch msg := msg.(type) {
	case skillsSavedMsg:
		s.notice = ""
		s.rebuild()
		return s, nil
	case skillsFailedMsg:
		s.notice = msg.message
		return s, nil
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	}
	return s, nil
}

func (s *skillsSection) handleKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
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
		if s.cursor < len(s.skillIndices)-1 {
			s.cursor++
		}
		s.notice = ""
	case "space", "enter":
		return s.toggleInvocable()
	case "x", "d":
		return s.remove()
	case "e":
		s.notice = "to edit skill instructions, edit its SKILL.md file directly"
	case "n":
		s.notice = "to add a skill, create a directory with SKILL.md under ~/.mivia/skills or .agents/skills"
	}
	return s, nil
}

func (s *skillsSection) toggleInvocable() (section, tea.Cmd) {
	sk, ok := s.selectedSkill()
	if !ok {
		return s, nil
	}
	scope := ports.ScopeProject
	if sk.Origin == "user" {
		scope = ports.ScopeUser
	}
	handle, err := s.store.Apply(context.Background(), scope, ports.SetSkillUserInvocable{
		Name:   sk.Name,
		Origin: sk.Origin,
		On:     !sk.UserInvocable,
	})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitSkillsSave(handle)
}

func (s *skillsSection) remove() (section, tea.Cmd) {
	sk, ok := s.selectedSkill()
	if !ok {
		return s, nil
	}
	scope := ports.ScopeProject
	if sk.Origin == "user" {
		scope = ports.ScopeUser
	}
	handle, err := s.store.Apply(context.Background(), scope, ports.RemoveSkill{
		Name:   sk.Name,
		Origin: sk.Origin,
	})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitSkillsSave(handle)
}

func (s *skillsSection) Hints() []keymap.ID {
	return []keymap.ID{keymap.IDSettingsUp, keymap.IDSettingsDown, keymap.IDSettingsToggle, keymap.IDSettingsDelete}
}
