package settings

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/field"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

const (
	skillFormScope = iota
	skillFormName
	skillFormDescription
	skillFormInvocable
	skillFormTools
	skillFormTriggers
	skillFormInstructions
	skillFormFieldCount
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

	confirmDeleteName   string
	confirmDeleteOrigin string

	// Editing / Add Form State
	editing            bool
	isNew              bool
	formFields         []field.Model
	formFocus          int
	editOriginalName   string
	editOriginalOrigin string
	editOriginalScope  ports.Scope
}

func newSkillsSection(store ports.SkillSettings) *skillsSection {
	return &skillsSection{store: store}
}

func (s *skillsSection) Title() string { return "Skills" }

func (s *skillsSection) CapturingInput() bool {
	return s.editing || s.confirmDeleteName != ""
}

func (s *skillsSection) SetSize(w, h int) {
	s.width, s.height = w, h
	for i := range s.formFields {
		s.formFields[i].SetWidth(w - 16)
	}
}

func (s *skillsSection) SetTheme(t theme.Theme, tier theme.Tier) {
	s.theme, s.tier = t, tier
	for i := range s.formFields {
		s.formFields[i].SetTheme(t, tier)
	}
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
	rows = append(rows, skillsRow{isHeader: true, header: "Global Skills (user home)"})
	if len(globalSkills) == 0 {
		rows = append(rows, skillsRow{isHeader: true, header: "  (no global skills installed)"})
	} else {
		for _, sk := range globalSkills {
			indices = append(indices, len(rows))
			rows = append(rows, skillsRow{skill: sk})
		}
	}

	// Project Group (workspace)
	rows = append(rows, skillsRow{isHeader: true, header: "Project Skills (workspace)"})
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
	if s.editing {
		return s.handleEditorKey(msg)
	}

	if s.store == nil {
		return s, nil
	}
	if len(s.skillIndices) == 0 && !s.editing {
		if msg.String() == "n" {
			s.openEditor(ports.SkillView{Origin: "project", UserInvocable: true}, true)
			return s, nil
		}
		return s, nil
	}

	// If pending delete confirmation, check if confirmed or cancelled
	if s.confirmDeleteName != "" {
		if msg.String() == "x" || msg.String() == "y" {
			targetName := s.confirmDeleteName
			targetOrigin := s.confirmDeleteOrigin
			s.confirmDeleteName = ""
			s.confirmDeleteOrigin = ""
			s.notice = ""
			return s.removeByNameAndOrigin(targetName, targetOrigin)
		}
		s.confirmDeleteName = ""
		s.confirmDeleteOrigin = ""
		s.notice = ""
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
	case "enter", "e":
		if sk, ok := s.selectedSkill(); ok {
			s.openEditor(sk, false)
			return s, nil
		}
	case "space", " ":
		return s.toggleInvocable()
	case "n":
		s.openEditor(ports.SkillView{Origin: "project", UserInvocable: true}, true)
		return s, nil
	case "x":
		return s.confirmRemove()
	}
	return s, nil
}

func (s *skillsSection) confirmRemove() (section, tea.Cmd) {
	sk, ok := s.selectedSkill()
	if !ok {
		return s, nil
	}
	s.confirmDeleteName = sk.Name
	s.confirmDeleteOrigin = sk.Origin
	s.notice = fmt.Sprintf("Delete skill %q? Press 'x' or 'y' to confirm, 'esc' to cancel", sk.Name)
	return s, nil
}

func (s *skillsSection) removeByNameAndOrigin(name, origin string) (section, tea.Cmd) {
	if s.store == nil {
		return s, nil
	}
	scope := ports.ScopeProject
	if origin == "user" {
		scope = ports.ScopeUser
	}
	handle, err := s.store.Apply(context.Background(), scope, ports.RemoveSkill{
		Name:   name,
		Origin: origin,
	})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitSkillsSave(handle)
}

func (s *skillsSection) toggleInvocable() (section, tea.Cmd) {
	if s.store == nil {
		return s, nil
	}
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
	return s.removeByNameAndOrigin(sk.Name, sk.Origin)
}

func (s *skillsSection) openEditor(sk ports.SkillView, isNew bool) {
	s.editing = true
	s.isNew = isNew
	s.editOriginalName = sk.Name
	s.editOriginalOrigin = sk.Origin
	scope := ports.ScopeProject
	if sk.Origin == "user" {
		scope = ports.ScopeUser
	}
	s.editOriginalScope = scope
	s.confirmDeleteName = ""
	s.confirmDeleteOrigin = ""
	s.notice = ""

	fieldWidth := s.width - 16
	if fieldWidth < 20 {
		fieldWidth = 40
	}

	mkChoice := func(label string, choices []string, active string) field.Model {
		f := field.New(s.theme, s.tier, label, field.KindChoice, fieldWidth)
		f.SetChoices(choices, active)
		return f
	}
	mkText := func(label string, val string) field.Model {
		f := field.New(s.theme, s.tier, label, field.KindText, fieldWidth)
		f.SetValue(val)
		return f
	}

	scopeChoice := "project"
	if sk.Origin == "user" {
		scopeChoice = "global"
	}
	invocableChoice := "on"
	if !isNew && !sk.UserInvocable {
		invocableChoice = "off"
	}

	toolsVal := strings.Join(sk.Tools, ", ")
	triggersVal := strings.Join(sk.Triggers, ", ")

	s.formFields = make([]field.Model, skillFormFieldCount)
	s.formFields[skillFormScope] = mkChoice("Scope:        ", []string{"project", "global"}, scopeChoice)
	s.formFields[skillFormName] = mkText("Name:         ", sk.Name)
	s.formFields[skillFormDescription] = mkText("Description:  ", sk.Description)
	s.formFields[skillFormInvocable] = mkChoice("Invocable:    ", []string{"on", "off"}, invocableChoice)
	s.formFields[skillFormTools] = mkText("Tools:        ", toolsVal)
	s.formFields[skillFormTriggers] = mkText("Triggers:     ", triggersVal)
	s.formFields[skillFormInstructions] = mkText("Instructions: ", sk.Instructions)

	if isNew {
		s.formFocus = skillFormName
	} else {
		s.formFocus = skillFormDescription
	}
	s.updateFormFieldFocus()
}

func (s *skillsSection) updateFormFieldFocus() tea.Cmd {
	var cmd tea.Cmd
	for i := range s.formFields {
		if i == s.formFocus {
			cmd = s.formFields[i].Focus()
		} else {
			s.formFields[i].Blur()
		}
	}
	return cmd
}

func (s *skillsSection) handleEditorKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.editing = false
		s.notice = ""
		return s, nil
	case "ctrl+s":
		return s.saveEditor()
	case "tab", "down":
		s.formFocus = (s.formFocus + 1) % len(s.formFields)
		cmd := s.updateFormFieldFocus()
		return s, cmd
	case "shift+tab", "up":
		s.formFocus = (s.formFocus - 1 + len(s.formFields)) % len(s.formFields)
		cmd := s.updateFormFieldFocus()
		return s, cmd
	}

	if s.formFocus < 0 || s.formFocus >= len(s.formFields) {
		return s, nil
	}

	// Choice fields: Scope, Invocable
	isChoice := (s.formFocus == skillFormScope || s.formFocus == skillFormInvocable)
	if isChoice {
		switch msg.String() {
		case " ", "space", "enter", "right", "l":
			s.formFields[s.formFocus].Cycle(1)
			return s, nil
		case "left", "h":
			s.formFields[s.formFocus].Cycle(-1)
			return s, nil
		}
		return s, nil
	}

	// Text fields: Name, Description, Tools, Triggers, Instructions
	if msg.String() == "enter" {
		if s.formFocus == len(s.formFields)-1 { // last field (Instructions)
			s.formFocus = 0
			cmd := s.updateFormFieldFocus()
			return s, cmd
		}
		s.formFocus = (s.formFocus + 1) % len(s.formFields)
		cmd := s.updateFormFieldFocus()
		return s, cmd
	}

	var cmd tea.Cmd
	s.formFields[s.formFocus], cmd = s.formFields[s.formFocus].Update(msg)
	return s, cmd
}

func (s *skillsSection) validateAndBuildSkill() (ports.SaveSkill, ports.Scope, error) {
	scopeStr := s.formFields[skillFormScope].Value()
	scope := ports.ScopeProject
	origin := "project"
	if scopeStr == "global" {
		scope = ports.ScopeUser
		origin = "user"
	}
	name := strings.TrimSpace(s.formFields[skillFormName].Value())
	if name == "" {
		return ports.SaveSkill{}, scope, fmt.Errorf("Skill name is required")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return ports.SaveSkill{}, scope, fmt.Errorf("Invalid skill name %q", name)
	}

	if s.isNew || s.editOriginalName != name || s.editOriginalOrigin != origin {
		for _, row := range s.rows {
			if !row.isHeader && row.skill.Name == name && row.skill.Origin == origin {
				return ports.SaveSkill{}, scope, fmt.Errorf("A %s skill named %q already exists", origin, name)
			}
		}
	}

	var tools []string
	if toolsStr := strings.TrimSpace(s.formFields[skillFormTools].Value()); toolsStr != "" {
		for _, part := range strings.FieldsFunc(toolsStr, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
			if part = strings.TrimSpace(part); part != "" {
				tools = append(tools, part)
			}
		}
	}

	var triggers []string
	if triggersStr := strings.TrimSpace(s.formFields[skillFormTriggers].Value()); triggersStr != "" {
		for _, part := range strings.Split(triggersStr, ",") {
			if part = strings.TrimSpace(part); part != "" {
				triggers = append(triggers, part)
			}
		}
	}

	instructions := strings.TrimSpace(s.formFields[skillFormInstructions].Value())
	if instructions == "" {
		instructions = "# " + name + "\n"
	}

	return ports.SaveSkill{
		Name:          name,
		Origin:        origin,
		Description:   strings.TrimSpace(s.formFields[skillFormDescription].Value()),
		UserInvocable: s.formFields[skillFormInvocable].Value() == "on",
		Tools:         tools,
		Triggers:      triggers,
		Instructions:  instructions,
	}, scope, nil
}

func (s *skillsSection) saveEditor() (section, tea.Cmd) {
	if len(s.formFields) != skillFormFieldCount {
		return s, nil
	}
	if s.store == nil {
		s.notice = "Skills store is unavailable"
		return s, nil
	}

	item, scope, err := s.validateAndBuildSkill()
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}

	// If editing existing skill and changed Name or Scope/Origin, remove old entry first
	if !s.isNew && (s.editOriginalName != item.Name || s.editOriginalOrigin != item.Origin) {
		oldHandle, removeErr := s.store.Apply(context.Background(), s.editOriginalScope, ports.RemoveSkill{
			Name:   s.editOriginalName,
			Origin: s.editOriginalOrigin,
		})
		if removeErr != nil {
			s.notice = removeErr.Error()
			return s, nil
		}
		if oldHandle != nil {
			for range oldHandle.Events() {
			}
		}
	}

	handle, err := s.store.Apply(context.Background(), scope, item)
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	s.editing = false
	s.notice = ""
	return s, awaitSkillsSave(handle)
}

func (s *skillsSection) Hints() []keymap.ID {
	if s.editing {
		return []keymap.ID{
			keymap.IDSettingsUp,
			keymap.IDSettingsDown,
			keymap.IDSettingsToggle,
			keymap.IDSettingsBack,
		}
	}
	return []keymap.ID{
		keymap.IDSettingsUp,
		keymap.IDSettingsDown,
		keymap.IDSettingsSelect,
		keymap.IDSettingsToggle,
		keymap.IDSettingsNew,
		keymap.IDSettingsDelete,
	}
}
