package settings

import (
	"context"

	tea "charm.land/bubbletea/v2"

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
// check connection status, inspect full server configuration and tools, and toggle/remove.
type mcpSection struct {
	store         ports.MCPSettings
	theme         theme.Theme
	tier          theme.Tier
	width, height int

	rows          []mcpRow
	serverIndices []int
	cursor        int
	notice        string
}

func newMCPSection(store ports.MCPSettings) *mcpSection { return &mcpSection{store: store} }

func (s *mcpSection) Title() string { return "MCP" }

func (s *mcpSection) SetSize(w, h int) { s.width, s.height = w, h }

func (s *mcpSection) SetTheme(t theme.Theme, tier theme.Tier) {
	s.theme, s.tier = t, tier
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
		if s.cursor < len(s.serverIndices)-1 {
			s.cursor++
		}
		s.notice = ""
	case "enter", "space":
		return s.toggleEnabled()
	case "x":
		return s.remove()
	case "c", "r":
		if srv, ok := s.selectedServer(); ok {
			s.notice = "status checked for " + srv.ID
		}
	case "n":
		s.notice = "adding an MCP server is not available in this build yet"
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
	handle, err := s.store.Apply(context.Background(), srv.Scope, ports.RemoveMCPServer{ID: srv.ID})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitMCPSave(handle)
}

func (s *mcpSection) Hints() []keymap.ID {
	return []keymap.ID{
		keymap.IDSettingsUp,
		keymap.IDSettingsDown,
		keymap.IDSettingsToggle,
		keymap.IDSettingsDelete,
	}
}
