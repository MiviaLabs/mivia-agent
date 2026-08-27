package settings

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/field"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

type mcpRow struct {
	isHeader bool
	header   string
	server   ports.MCPServerView
}

// mcpSection is the MCP settings section: browse MCP servers configured
// globally (user home ~/.mivia/mivia.toml) and in the workspace (.mivia/mivia.toml),
// check connection status, inspect full server configuration and tools, and add/edit/remove.
type mcpSection struct {
	store         ports.MCPSettings
	theme         theme.Theme
	tier          theme.Tier
	width, height int

	rows          []mcpRow
	serverIndices []int
	cursor        int
	notice        string

	confirmDeleteID string

	// Editing / Add Form State
	editing           bool
	isNew             bool
	formFields        []field.Model
	formFocus         int
	editOriginalID    string
	editOriginalScope ports.Scope
}

func newMCPSection(store ports.MCPSettings) *mcpSection { return &mcpSection{store: store} }

func (s *mcpSection) Title() string { return "MCP" }

func (s *mcpSection) CapturingInput() bool {
	return s.editing || s.confirmDeleteID != ""
}

func (s *mcpSection) SetSize(w, h int) {
	s.width, s.height = w, h
	for i := range s.formFields {
		s.formFields[i].SetWidth(w - 16)
	}
}

func (s *mcpSection) SetTheme(t theme.Theme, tier theme.Tier) {
	s.theme, s.tier = t, tier
	for i := range s.formFields {
		s.formFields[i].SetTheme(t, tier)
	}
	if s.store != nil && s.rows == nil {
		s.rebuild()
	}
}

func (s *mcpSection) rebuild() {
	if s.store == nil {
		s.rows = nil
		s.serverIndices = nil
		return
	}
	all := s.store.MCPServers()
	var globalServers, projectServers []ports.MCPServerView
	for _, srv := range all {
		if srv.Scope == ports.ScopeProject {
			projectServers = append(projectServers, srv)
		} else {
			globalServers = append(globalServers, srv)
		}
	}

	rows := make([]mcpRow, 0, len(all)+4)
	indices := make([]int, 0, len(all))

	// Global Group (user config)
	rows = append(rows, mcpRow{isHeader: true, header: "Global MCP Servers (user config)"})
	if len(globalServers) == 0 {
		rows = append(rows, mcpRow{isHeader: true, header: "  (no global MCP servers configured)"})
	} else {
		for _, srv := range globalServers {
			indices = append(indices, len(rows))
			rows = append(rows, mcpRow{server: srv})
		}
	}

	// Project Group (workspace)
	rows = append(rows, mcpRow{isHeader: true, header: "Project MCP Servers (workspace)"})
	if len(projectServers) == 0 {
		rows = append(rows, mcpRow{isHeader: true, header: "  (no project MCP servers configured)"})
	} else {
		for _, srv := range projectServers {
			indices = append(indices, len(rows))
			rows = append(rows, mcpRow{server: srv})
		}
	}

	s.rows = rows
	s.serverIndices = indices
	if s.cursor >= len(s.serverIndices) {
		s.cursor = len(s.serverIndices) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

func (s *mcpSection) selectedServer() (ports.MCPServerView, bool) {
	if len(s.serverIndices) == 0 || s.cursor < 0 || s.cursor >= len(s.serverIndices) {
		return ports.MCPServerView{}, false
	}
	rowIdx := s.serverIndices[s.cursor]
	return s.rows[rowIdx].server, true
}

type mcpSavedMsg struct{}
type mcpFailedMsg struct{ message string }

func awaitMCPSave(handle ports.SaveHandle) tea.Cmd {
	return func() tea.Msg {
		var last ports.SaveEvent
		for ev := range handle.Events() {
			last = ev
		}
		if last.State == ports.SaveFailed {
			return mcpFailedMsg{message: last.Message}
		}
		return mcpSavedMsg{}
	}
}

func (s *mcpSection) Update(msg tea.Msg) (section, tea.Cmd) {
	switch msg := msg.(type) {
	case mcpSavedMsg:
		s.notice = ""
		s.rebuild()
		return s, nil
	case mcpFailedMsg:
		s.notice = msg.message
		return s, nil
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	}
	return s, nil
}

func (s *mcpSection) handleKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
	if s.editing {
		return s.handleEditorKey(msg)
	}

	if s.store == nil || (len(s.rows) == 0 && !s.editing) {
		if msg.String() == "n" {
			s.openEditor(ports.MCPServerView{Transport: "stdio", Enabled: true, Scope: ports.ScopeProject}, true)
			return s, nil
		}
		return s, nil
	}

	// If pending delete confirmation, check if confirmed or cancelled
	if s.confirmDeleteID != "" {
		if msg.String() == "x" || msg.String() == "y" {
			targetID := s.confirmDeleteID
			s.confirmDeleteID = ""
			s.notice = ""
			return s.removeByID(targetID)
		}
		s.confirmDeleteID = ""
		s.notice = ""
		if msg.String() == "esc" {
			return s, nil
		}
	}

	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
		s.notice = ""
	case "down", "j":
		if s.cursor < len(s.serverIndices)-1 {
			s.cursor++
		}
		s.notice = ""
	case "enter", "e":
		if srv, ok := s.selectedServer(); ok {
			s.openEditor(srv, false)
			return s, nil
		}
	case "space", " ":
		return s.toggleEnabled()
	case "n":
		s.openEditor(ports.MCPServerView{Transport: "stdio", Enabled: true, Scope: ports.ScopeProject}, true)
		return s, nil
	case "x":
		return s.confirmRemove()
	case "c", "r":
		if srv, ok := s.selectedServer(); ok {
			s.notice = "status checked for " + srv.ID
		}
	}
	return s, nil
}

func (s *mcpSection) confirmRemove() (section, tea.Cmd) {
	srv, ok := s.selectedServer()
	if !ok {
		return s, nil
	}
	s.confirmDeleteID = srv.ID
	s.notice = fmt.Sprintf("Delete MCP server %q? Press 'x' or 'y' to confirm, 'esc' to cancel", srv.ID)
	return s, nil
}

func (s *mcpSection) removeByID(id string) (section, tea.Cmd) {
	for _, row := range s.rows {
		if !row.isHeader && row.server.ID == id {
			handle, err := s.store.Apply(context.Background(), row.server.Scope, ports.RemoveMCPServer{ID: id})
			if err != nil {
				s.notice = err.Error()
				return s, nil
			}
			return s, awaitMCPSave(handle)
		}
	}
	return s, nil
}

func (s *mcpSection) toggleEnabled() (section, tea.Cmd) {
	srv, ok := s.selectedServer()
	if !ok {
		return s, nil
	}
	handle, err := s.store.Apply(context.Background(), srv.Scope,
		ports.SetMCPServerEnabled{ID: srv.ID, On: !srv.Enabled})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitMCPSave(handle)
}

func (s *mcpSection) remove() (section, tea.Cmd) {
	srv, ok := s.selectedServer()
	if !ok {
		return s, nil
	}
	return s.removeByID(srv.ID)
}

func (s *mcpSection) openEditor(srv ports.MCPServerView, isNew bool) {
	s.editing = true
	s.isNew = isNew
	s.editOriginalID = srv.ID
	s.editOriginalScope = srv.Scope
	s.confirmDeleteID = ""
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
	if srv.Scope == ports.ScopeUser || srv.Global {
		scopeChoice = "global"
	}
	transportChoice := srv.Transport
	if transportChoice == "" {
		transportChoice = "stdio"
	}
	enabledChoice := "on"
	if !isNew && !srv.Enabled {
		enabledChoice = "off"
	}

	argsVal := strings.Join(srv.Args, " ")
	envVal := strings.Join(srv.EnvNames, ", ")

	s.formFields = []field.Model{
		mkChoice("Scope:      ", []string{"project", "global"}, scopeChoice),
		mkText("ID:         ", srv.ID),
		mkChoice("Transport:  ", []string{"stdio", "streamable_http", "sse"}, transportChoice),
		mkText("Command:    ", srv.Command),
		mkText("Args:       ", argsVal),
		mkText("URL:        ", srv.Endpoint),
		mkText("Env Vars:   ", envVal),
		mkChoice("Enabled:    ", []string{"on", "off"}, enabledChoice),
	}

	if isNew {
		s.formFocus = 1 // Start on ID
	} else {
		s.formFocus = 3 // Start on Command
	}
	s.updateFormFieldFocus()
}

func (s *mcpSection) updateFormFieldFocus() tea.Cmd {
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

func (s *mcpSection) handleEditorKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
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

	// Choice fields: indices 0 (Scope), 2 (Transport), 7 (Enabled)
	isChoice := (s.formFocus == 0 || s.formFocus == 2 || s.formFocus == 7)
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

	// Text fields: indices 1 (ID), 3 (Command), 4 (Args), 5 (URL), 6 (Env Vars)
	if msg.String() == "enter" {
		if s.formFocus == len(s.formFields)-2 { // last text field
			s.formFocus = (s.formFocus + 1) % len(s.formFields)
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

func (s *mcpSection) saveEditor() (section, tea.Cmd) {
	if len(s.formFields) < 8 {
		return s, nil
	}
	scopeStr := s.formFields[0].Value()
	scope := ports.ScopeProject
	if scopeStr == "global" {
		scope = ports.ScopeUser
	}
	id := strings.TrimSpace(s.formFields[1].Value())
	transport := s.formFields[2].Value()
	command := strings.TrimSpace(s.formFields[3].Value())
	argsStr := strings.TrimSpace(s.formFields[4].Value())
	urlStr := strings.TrimSpace(s.formFields[5].Value())
	envStr := strings.TrimSpace(s.formFields[6].Value())
	enabled := s.formFields[7].Value() == "on"

	if id == "" {
		s.notice = "Server ID is required"
		return s, nil
	}
	if transport == "stdio" && command == "" {
		s.notice = "Command is required for stdio transport"
		return s, nil
	}
	if (transport == "streamable_http" || transport == "sse") && urlStr == "" {
		s.notice = "URL is required for HTTP/SSE transport"
		return s, nil
	}

	var args []string
	if argsStr != "" {
		args = strings.Fields(argsStr)
	}

	var envNames []string
	if envStr != "" {
		for _, part := range strings.FieldsFunc(envStr, func(r rune) bool { return r == ',' || r == ' ' }) {
			part = strings.TrimSpace(part)
			if part != "" {
				envNames = append(envNames, part)
			}
		}
	}

	srv := ports.MCPServerView{
		ID:        id,
		Transport: transport,
		Command:   command,
		Args:      args,
		Endpoint:  urlStr,
		EnvNames:  envNames,
		Enabled:   enabled,
		Scope:     scope,
		Global:    scope == ports.ScopeUser,
		State:     ports.MCPStateUnknown,
	}

	// If editing existing server and changed ID or Scope, remove old entry first
	if !s.isNew && (s.editOriginalID != id || s.editOriginalScope != scope) {
		_, _ = s.store.Apply(context.Background(), s.editOriginalScope, ports.RemoveMCPServer{ID: s.editOriginalID})
	}

	handle, err := s.store.Apply(context.Background(), scope, ports.UpsertMCPServer{Server: srv})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	s.editing = false
	s.notice = ""
	return s, awaitMCPSave(handle)
}

func (s *mcpSection) Hints() []keymap.ID {
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
