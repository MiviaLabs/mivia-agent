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
	agentFormScope = iota
	agentFormName
	agentFormDescription
	agentFormProvider
	agentFormModel
	agentFormTools
	agentFormSkills
	agentFormMCPServers
	agentFormSystemPrompt
	agentFormFieldCount
)

type agentsRow struct {
	isHeader bool
	header   string
	agent    ports.AgentView
}

type agentsSection struct {
	store         ports.AgentSettings
	theme         theme.Theme
	tier          theme.Tier
	width, height int

	rows         []agentsRow
	agentIndices []int
	cursor       int
	notice       string

	confirmDeleteName  string
	confirmDeleteScope ports.Scope

	// Editing / Add Form State
	editing           bool
	isNew             bool
	formFields        []field.Model
	formFocus         int
	editOriginalName  string
	editOriginalScope ports.Scope
}

func newAgentsSection(store ports.AgentSettings) *agentsSection {
	return &agentsSection{store: store}
}

func (s *agentsSection) Title() string { return "Agents" }

func (s *agentsSection) CapturingInput() bool {
	return s.editing || s.confirmDeleteName != ""
}

func (s *agentsSection) SetSize(w, h int) {
	s.width, s.height = w, h
	for i := range s.formFields {
		s.formFields[i].SetWidth(w - 16)
	}
}

func (s *agentsSection) SetTheme(t theme.Theme, tier theme.Tier) {
	s.theme, s.tier = t, tier
	for i := range s.formFields {
		s.formFields[i].SetTheme(t, tier)
	}
	if s.store != nil && s.rows == nil {
		s.rebuild()
	}
}

func (s *agentsSection) rebuild() {
	if s.store == nil {
		s.rows = nil
		s.agentIndices = nil
		return
	}
	all := s.store.Agents()
	var globalAgents, projectAgents, builtinAgents []ports.AgentView
	for _, ag := range all {
		switch ag.Scope {
		case ports.ScopeUser:
			globalAgents = append(globalAgents, ag)
		case ports.ScopeBuiltin:
			builtinAgents = append(builtinAgents, ag)
		default:
			projectAgents = append(projectAgents, ag)
		}
	}

	rows := make([]agentsRow, 0, len(all)+4)
	indices := make([]int, 0, len(all))

	// Global Group (user home)
	rows = append(rows, agentsRow{isHeader: true, header: "Global Agents (user home)"})
	if len(globalAgents) == 0 {
		rows = append(rows, agentsRow{isHeader: true, header: "  (no global agents installed)"})
	} else {
		for _, ag := range globalAgents {
			indices = append(indices, len(rows))
			rows = append(rows, agentsRow{agent: ag})
		}
	}

	// Project Group (workspace)
	rows = append(rows, agentsRow{isHeader: true, header: "Project Agents (workspace)"})
	if len(projectAgents) == 0 {
		rows = append(rows, agentsRow{isHeader: true, header: "  (no project agents installed)"})
	} else {
		for _, ag := range projectAgents {
			indices = append(indices, len(rows))
			rows = append(rows, agentsRow{agent: ag})
		}
	}

	// Built-in Group (compiled, read-only)
	rows = append(rows, agentsRow{isHeader: true, header: "Built-in Agents (shipped with mivia)"})
	if len(builtinAgents) == 0 {
		rows = append(rows, agentsRow{isHeader: true, header: "  (no built-in agents)"})
	} else {
		for _, ag := range builtinAgents {
			indices = append(indices, len(rows))
			rows = append(rows, agentsRow{agent: ag})
		}
	}

	s.rows = rows
	s.agentIndices = indices
	if s.cursor >= len(s.agentIndices) {
		s.cursor = len(s.agentIndices) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

func (s *agentsSection) selectedAgent() (ports.AgentView, bool) {
	if len(s.agentIndices) == 0 || s.cursor < 0 || s.cursor >= len(s.agentIndices) {
		return ports.AgentView{}, false
	}
	rowIdx := s.agentIndices[s.cursor]
	return s.rows[rowIdx].agent, true
}

type agentsSavedMsg struct{}
type agentsFailedMsg struct{ message string }

func awaitAgentsSave(handle ports.SaveHandle) tea.Cmd {
	return func() tea.Msg {
		var last ports.SaveEvent
		for ev := range handle.Events() {
			last = ev
		}
		if last.State == ports.SaveFailed {
			return agentsFailedMsg{message: last.Message}
		}
		return agentsSavedMsg{}
	}
}

func (s *agentsSection) Update(msg tea.Msg) (section, tea.Cmd) {
	switch msg := msg.(type) {
	case agentsSavedMsg:
		s.notice = ""
		s.rebuild()
		return s, nil
	case agentsFailedMsg:
		s.notice = msg.message
		return s, nil
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	}
	return s, nil
}

func (s *agentsSection) handleKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
	if s.editing {
		return s.handleEditorKey(msg)
	}

	if s.store == nil {
		return s, nil
	}
	if len(s.agentIndices) == 0 && !s.editing {
		if msg.String() == "n" {
			s.openEditor(ports.AgentView{Scope: ports.ScopeProject}, true)
			return s, nil
		}
		return s, nil
	}

	if s.confirmDeleteName != "" {
		if msg.String() == "x" || msg.String() == "y" {
			targetName := s.confirmDeleteName
			targetScope := s.confirmDeleteScope
			s.confirmDeleteName = ""
			s.confirmDeleteScope = 0
			s.notice = ""
			return s.removeByNameAndScope(targetName, targetScope)
		}
		s.confirmDeleteName = ""
		s.confirmDeleteScope = 0
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
		if s.cursor < len(s.agentIndices)-1 {
			s.cursor++
		}
		s.notice = ""
	case "enter", "e":
		if ag, ok := s.selectedAgent(); ok {
			if ag.Scope == ports.ScopeBuiltin {
				s.notice = fmt.Sprintf("built-in agent %q is read-only; create a same-name definition to override it", ag.Name)
				return s, nil
			}
			s.openEditor(ag, false)
			return s, nil
		}
	case "n":
		s.openEditor(ports.AgentView{Scope: ports.ScopeProject}, true)
		return s, nil
	case "x":
		return s.confirmRemove()
	}
	return s, nil
}

func (s *agentsSection) confirmRemove() (section, tea.Cmd) {
	ag, ok := s.selectedAgent()
	if !ok {
		return s, nil
	}
	if ag.Scope == ports.ScopeBuiltin {
		s.notice = fmt.Sprintf("built-in agent %q cannot be removed", ag.Name)
		return s, nil
	}
	s.confirmDeleteName = ag.Name
	s.confirmDeleteScope = ag.Scope
	s.notice = fmt.Sprintf("Delete agent %q? Press 'x' or 'y' to confirm, 'esc' to cancel", ag.Name)
	return s, nil
}

func (s *agentsSection) removeByNameAndScope(name string, scope ports.Scope) (section, tea.Cmd) {
	if s.store == nil {
		return s, nil
	}
	handle, err := s.store.Apply(context.Background(), scope, ports.RemoveAgent{
		Name: name,
	})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitAgentsSave(handle)
}

func (s *agentsSection) remove() (section, tea.Cmd) {
	ag, ok := s.selectedAgent()
	if !ok {
		return s, nil
	}
	return s.removeByNameAndScope(ag.Name, ag.Scope)
}

func (s *agentsSection) openEditor(ag ports.AgentView, isNew bool) {
	s.editing = true
	s.isNew = isNew
	s.editOriginalName = ag.Name
	s.editOriginalScope = ag.Scope
	s.confirmDeleteName = ""
	s.confirmDeleteScope = 0
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
	if ag.Scope == ports.ScopeUser {
		scopeChoice = "global"
	}

	s.formFields = make([]field.Model, agentFormFieldCount)
	s.formFields[agentFormScope] = mkChoice("Scope:        ", []string{"project", "global"}, scopeChoice)
	s.formFields[agentFormName] = mkText("Name:         ", ag.Name)
	s.formFields[agentFormDescription] = mkText("Description:  ", ag.Description)
	s.formFields[agentFormProvider] = mkText("Provider:     ", ag.Provider)
	s.formFields[agentFormModel] = mkText("Model:        ", ag.Model)
	s.formFields[agentFormTools] = mkText("Tools:        ", strings.Join(ag.Tools, ", "))
	s.formFields[agentFormSkills] = mkText("Skills:       ", strings.Join(ag.Skills, ", "))
	s.formFields[agentFormMCPServers] = mkText("MCP Servers:  ", strings.Join(ag.MCPServers, ", "))
	s.formFields[agentFormSystemPrompt] = mkText("System Prompt:", ag.SystemPrompt)

	if isNew {
		s.formFocus = agentFormName
	} else {
		s.formFocus = agentFormDescription
	}
	s.updateFormFieldFocus()
}

func (s *agentsSection) updateFormFieldFocus() tea.Cmd {
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

func (s *agentsSection) handleEditorKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
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

	if s.formFocus == agentFormScope {
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

	if msg.String() == "enter" {
		if s.formFocus == len(s.formFields)-1 {
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

func splitCommaOrSpace(raw string) []string {
	var out []string
	if raw == "" {
		return out
	}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *agentsSection) validateAndBuildAgent() (ports.AgentView, ports.Scope, error) {
	scopeStr := s.formFields[agentFormScope].Value()
	scope := ports.ScopeProject
	if scopeStr == "global" {
		scope = ports.ScopeUser
	}
	name := strings.TrimSpace(s.formFields[agentFormName].Value())
	if name == "" {
		return ports.AgentView{}, scope, fmt.Errorf("Agent name is required")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return ports.AgentView{}, scope, fmt.Errorf("Invalid agent name %q", name)
	}

	if !s.isNew && s.editOriginalScope == ports.ScopeBuiltin {
		return ports.AgentView{}, scope, fmt.Errorf("built-in agent %q is read-only; create a same-name definition to override it", s.editOriginalName)
	}

	if s.isNew || s.editOriginalName != name || s.editOriginalScope != scope {
		for _, row := range s.rows {
			if !row.isHeader && row.agent.Name == name && row.agent.Scope == scope {
				scopeLabel := "project"
				if scope == ports.ScopeUser {
					scopeLabel = "global"
				}
				return ports.AgentView{}, scope, fmt.Errorf("A %s agent named %q already exists", scopeLabel, name)
			}
		}
	}

	prompt := strings.TrimSpace(s.formFields[agentFormSystemPrompt].Value())
	if prompt == "" {
		prompt = "# " + name + "\n"
	}

	return ports.AgentView{
		Name:              name,
		Description:       strings.TrimSpace(s.formFields[agentFormDescription].Value()),
		Provider:          strings.TrimSpace(s.formFields[agentFormProvider].Value()),
		Model:             strings.TrimSpace(s.formFields[agentFormModel].Value()),
		Tools:             splitCommaOrSpace(s.formFields[agentFormTools].Value()),
		Skills:            splitCommaOrSpace(s.formFields[agentFormSkills].Value()),
		MCPServers:        splitCommaOrSpace(s.formFields[agentFormMCPServers].Value()),
		SystemPrompt:      prompt,
		SystemPromptChars: len(prompt),
		Scope:             scope,
	}, scope, nil
}

func (s *agentsSection) saveEditor() (section, tea.Cmd) {
	if len(s.formFields) != agentFormFieldCount {
		return s, nil
	}
	if s.store == nil {
		s.notice = "Agents store is unavailable"
		return s, nil
	}

	item, scope, err := s.validateAndBuildAgent()
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}

	if !s.isNew && (s.editOriginalName != item.Name || s.editOriginalScope != scope) {
		oldHandle, removeErr := s.store.Apply(context.Background(), s.editOriginalScope, ports.RemoveAgent{
			Name: s.editOriginalName,
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

	handle, err := s.store.Apply(context.Background(), scope, ports.UpsertAgent{Agent: item})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	s.editing = false
	s.notice = ""
	return s, awaitAgentsSave(handle)
}

func (s *agentsSection) Hints() []keymap.ID {
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
		keymap.IDSettingsNew,
		keymap.IDSettingsDelete,
	}
}
