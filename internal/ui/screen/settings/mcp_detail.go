package settings

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// secretArgMarkers are the substrings after which an MCP server's Args
// value is elided before it is ever drawn - settings-screen.md §5's
// masked-by-default rule.
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

const mcpRowGap = 2

func (s *mcpSection) View() string {
	if s.store == nil {
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("MCP is unavailable.")
	}

	selectedRowIdx := -1
	if len(s.serverIndices) > 0 && s.cursor >= 0 && s.cursor < len(s.serverIndices) {
		selectedRowIdx = s.serverIndices[s.cursor]
	}

	listLines := s.renderListLines(selectedRowIdx)
	var detailLines []string
	if srv, ok := s.selectedServer(); ok {
		detailLines = s.renderDetail(srv)
	}

	notice := ""
	if s.notice != "" {
		notice = render.Role(s.theme, s.tier, theme.RoleWarning).Render(s.notice)
	}
	return render.SplitListDetail(listLines, detailLines, selectedRowIdx, s.height, notice)
}

func (s *mcpSection) renderListLines(selectedRowIdx int) []string {
	cells := make([][]string, len(s.rows))
	for i, row := range s.rows {
		if !row.isHeader {
			cells[i] = s.renderServerCells(row.server)
		}
	}
	aligned := render.Columns(mcpRowGap, cells)

	var listLines []string
	for i, row := range s.rows {
		if row.isHeader {
			headerText := row.header
			if strings.HasPrefix(headerText, "  (") {
				listLines = append(listLines, render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(headerText))
			} else {
				listLines = append(listLines, render.Role(s.theme, s.tier, theme.RoleAccent).Bold(true).Render(headerText))
			}
			continue
		}

		line := ""
		if i < len(aligned) {
			line = aligned[i]
		}
		marker := "  "
		if i == selectedRowIdx {
			marker = "> "
		}
		listLines = append(listLines, marker+line)
	}
	return listLines
}

func (s *mcpSection) renderServerCells(row ports.MCPServerView) []string {
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

	stateStr := s.stateLabel(row)

	enabledStr := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("disabled")
	if row.Enabled {
		enabledStr = render.Role(s.theme, s.tier, theme.RoleSuccess).Render("enabled")
	}

	toolsStr := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(fmt.Sprintf("%d tools", row.ToolCount))

	return []string{name, targetStr, stateStr, enabledStr, toolsStr}
}

func (s *mcpSection) stateLabel(row ports.MCPServerView) string {
	if !row.Enabled {
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("disabled")
	}
	switch row.State {
	case ports.MCPStateConnected:
		return render.Role(s.theme, s.tier, theme.RoleSuccess).Render("connected")
	case ports.MCPStateFailed:
		lbl := "failed"
		if row.FailMessage != "" {
			lbl = fmt.Sprintf("failed (%s)", row.FailMessage)
		}
		return render.Role(s.theme, s.tier, theme.RoleDanger).Render(lbl)
	case ports.MCPStateDisabled:
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("disabled")
	default:
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("unknown")
	}
}

func (s *mcpSection) renderDetail(srv ports.MCPServerView) []string {
	subtle := render.Role(s.theme, s.tier, theme.RoleFGSubtle)
	fg := render.Role(s.theme, s.tier, theme.RoleFG)
	accent := render.Role(s.theme, s.tier, theme.RoleAccent)

	originLabel := "Project (workspace: .mivia/mivia.toml)"
	if srv.Scope == ports.ScopeUser || srv.Global {
		originLabel = "Global (user: ~/.mivia/mivia.toml)"
	}

	lines := []string{
		accent.Bold(true).Render(srv.ID) + "  " + subtle.Render(originLabel),
	}

	// Status line
	lines = append(lines, "Status: "+s.detailStatus(srv))

	// Transport & timeout
	transportInfo := fmt.Sprintf("Transport: %s", srv.Transport)
	if srv.TimeoutSeconds > 0 {
		transportInfo += fmt.Sprintf(" • Timeout: %ds", srv.TimeoutSeconds)
	}
	lines = append(lines, subtle.Render(transportInfo))

	// Command/Args or Endpoint
	if srv.Transport == "stdio" || (srv.Command != "" && srv.Endpoint == "") {
		if srv.Command != "" {
			lines = append(lines, fg.Render(fmt.Sprintf("Command: %s", srv.Command)))
		}
		if len(srv.Args) > 0 {
			maskedArgs := make([]string, len(srv.Args))
			for i, a := range srv.Args {
				maskedArgs[i] = maskArg(a)
			}
			lines = append(lines, subtle.Render(fmt.Sprintf("Args: [%s]", strings.Join(maskedArgs, ", "))))
		}
	} else if srv.Endpoint != "" {
		lines = append(lines, fg.Render(fmt.Sprintf("Endpoint: %s", srv.Endpoint)))
	}

	// Env vars & Headers & Tool count
	var meta []string
	if len(srv.EnvNames) > 0 {
		meta = append(meta, fmt.Sprintf("env: [%s]", strings.Join(srv.EnvNames, ", ")))
	}
	if len(srv.HeaderEnvNames) > 0 {
		var headers []string
		for k, v := range srv.HeaderEnvNames {
			headers = append(headers, fmt.Sprintf("%s: $%s", k, v))
		}
		sort.Strings(headers)
		meta = append(meta, fmt.Sprintf("headers: [%s]", strings.Join(headers, ", ")))
	}
	if srv.ToolCount > 0 {
		meta = append(meta, fmt.Sprintf("tools: %d registered", srv.ToolCount))
	}
	if len(meta) > 0 {
		lines = append(lines, subtle.Render(strings.Join(meta, " • ")))
	}

	// Failure details
	if srv.State == ports.MCPStateFailed && (srv.FailMessage != "" || srv.FailKind != ports.MCPFailNone) {
		lines = append(lines, "")
		failKindStr := failKindLabel(srv.FailKind)
		if failKindStr != "" {
			lines = append(lines, render.Role(s.theme, s.tier, theme.RoleDanger).Render("Error ("+failKindStr+"): ")+fg.Render(srv.FailMessage))
		} else if srv.FailMessage != "" {
			lines = append(lines, render.Role(s.theme, s.tier, theme.RoleDanger).Render("Error: ")+fg.Render(srv.FailMessage))
		}
	}

	return lines
}

func (s *mcpSection) detailStatus(srv ports.MCPServerView) string {
	if !srv.Enabled {
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("disabled")
	}
	switch srv.State {
	case ports.MCPStateConnected:
		return render.Role(s.theme, s.tier, theme.RoleSuccess).Bold(true).Render("● connected")
	case ports.MCPStateFailed:
		msg := "✖ failed"
		if srv.FailMessage != "" {
			msg += " (" + srv.FailMessage + ")"
		}
		return render.Role(s.theme, s.tier, theme.RoleDanger).Bold(true).Render(msg)
	case ports.MCPStateDisabled:
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("disabled")
	default:
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("unknown")
	}
}

func failKindLabel(k ports.MCPFailKind) string {
	switch k {
	case ports.MCPFailSpawn:
		return "process spawn"
	case ports.MCPFailConnect:
		return "network connection"
	case ports.MCPFailTLS:
		return "TLS handshake"
	case ports.MCPFailTimeout:
		return "connection timeout"
	case ports.MCPFailProtocol:
		return "protocol negotiation"
	case ports.MCPFailAuth:
		return "authentication"
	default:
		return ""
	}
}
