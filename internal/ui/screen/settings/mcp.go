package settings

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// secretArgMarkers are the substrings after which an MCP server's Args
// value is elided before it is ever drawn - settings-screen.md §5's
// masked-by-default rule. ctrl+r ("reveal") is bound in the keymap
// (keymap.IDSettingsReveal) but not wired to per-field reveal in this
// slice; every row renders masked, with no unmask path yet, which is
// the conservative direction to leave a gap in (a missing reveal key
// is an inconvenience, a missing mask is a leak).
var secretArgMarkers = []string{"--token=", "--api-key=", "--apikey=", "key=", "password="}

// maskArg elides the value after the first secret marker found in arg,
// keeping the marker itself so the argument's SHAPE stays readable
// ("--token=***" tells the user there is a token, without showing it).
func maskArg(arg string) string {
	for _, marker := range secretArgMarkers {
		if i := strings.Index(arg, marker); i >= 0 {
			return arg[:i+len(marker)] + "***"
		}
	}
	return arg
}

// mcpSection is the MCP settings section. Endpoint is already
// host-only at the ports boundary (MCPServerView doc comment); Args is
// masked here, at render time, since the raw value is still needed
// verbatim by a future editor.
type mcpSection struct {
	store         ports.MCPSettings
	theme         theme.Theme
	tier          theme.Tier
	width, height int

	rows   []ports.MCPServerView
	cursor int
	notice string
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
	s.rows = s.store.MCPServers()
	if s.cursor >= len(s.rows) {
		s.cursor = len(s.rows) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
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
		if s.cursor < len(s.rows)-1 {
			s.cursor++
		}
		s.notice = ""
	case "enter", "space":
		return s.toggleEnabled()
	case "x":
		return s.remove()
	case "n":
		s.notice = "adding an MCP server is not available in this build yet"
	}
	return s, nil
}

func (s *mcpSection) toggleEnabled() (section, tea.Cmd) {
	row := s.rows[s.cursor]
	handle, err := s.store.Apply(context.Background(), ports.ScopeUser,
		ports.SetMCPServerEnabled{ID: row.ID, On: !row.Enabled})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitMCPSave(handle)
}

func (s *mcpSection) remove() (section, tea.Cmd) {
	handle, err := s.store.Apply(context.Background(), ports.ScopeUser, ports.RemoveMCPServer{ID: s.rows[s.cursor].ID})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitMCPSave(handle)
}

func (s *mcpSection) View() string {
	if s.store == nil {
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("MCP is unavailable.")
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

// renderRow draws one server: id, transport target (endpoint or
// command, masked), state, enabled flag, tool count.
func (s *mcpSection) renderRow(row ports.MCPServerView) string {
	fg := render.Role(s.theme, s.tier, theme.RoleFG)
	name := fg.Bold(true).Render(row.ID)

	target := row.Endpoint
	if target == "" {
		parts := append([]string{row.Command}, row.Args...)
		masked := make([]string, len(parts))
		for i, p := range parts {
			masked[i] = maskArg(p)
		}
		target = strings.Join(masked, " ")
	}
	targetStr := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(target)

	state := s.stateLabel(row)
	enabled := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("disabled")
	if row.Enabled {
		enabled = render.Role(s.theme, s.tier, theme.RoleSuccess).Render("enabled")
	}
	tools := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(fmt.Sprintf("%d tools", row.ToolCount))

	return name + "  " + targetStr + "  " + enabled + "  " + state + "  " + tools
}

func (s *mcpSection) stateLabel(row ports.MCPServerView) string {
	switch row.State {
	case ports.MCPStateConnected:
		return render.Role(s.theme, s.tier, theme.RoleSuccess).Render("connected")
	case ports.MCPStateFailed:
		msg := row.FailMessage
		if msg == "" {
			msg = "failed"
		}
		return render.Role(s.theme, s.tier, theme.RoleDanger).Render(msg)
	case ports.MCPStateDisabled:
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("disabled")
	default:
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("unknown")
	}
}

func (s *mcpSection) Hints() []keymap.ID {
	return []keymap.ID{keymap.IDSettingsUp, keymap.IDSettingsDown, keymap.IDSettingsToggle, keymap.IDSettingsDelete}
}
