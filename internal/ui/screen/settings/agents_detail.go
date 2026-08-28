package settings

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

const agentRowGap = 2

func (s *agentsSection) View() string {
	if s.store == nil {
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("Agents is unavailable.")
	}

	if s.editing {
		return s.renderEditor()
	}

	selectedRowIdx := -1
	if len(s.agentIndices) > 0 && s.cursor >= 0 && s.cursor < len(s.agentIndices) {
		selectedRowIdx = s.agentIndices[s.cursor]
	}

	listLines := s.renderListLines(selectedRowIdx)
	var detailLines []string
	if ag, ok := s.selectedAgent(); ok {
		detailLines = s.renderDetail(ag)
	}

	notice := ""
	if s.notice != "" {
		notice = render.Role(s.theme, s.tier, theme.RoleWarning).Render(s.notice)
	}
	return render.SplitListDetail(listLines, detailLines, selectedRowIdx, s.height, notice)
}

func (s *agentsSection) renderListLines(selectedRowIdx int) []string {
	cells := make([][]string, len(s.rows))
	for i, row := range s.rows {
		if !row.isHeader {
			cells[i] = s.renderAgentCells(row.agent)
		}
	}
	aligned := render.Columns(agentRowGap, cells)

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
		marker := "  "
		if i == selectedRowIdx {
			marker = "> "
		}
		listLines = append(listLines, marker+aligned[i])
	}
	return listLines
}

func (s *agentsSection) renderAgentCells(ag ports.AgentView) []string {
	fg := render.Role(s.theme, s.tier, theme.RoleFG)
	name := fg.Bold(true).Render(ag.Name)

	modelStr := strings.TrimSpace(ag.Provider + "/" + ag.Model)
	if modelStr == "/" || modelStr == "" {
		modelStr = "-"
	}
	model := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(modelStr)

	tools := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(fmt.Sprintf("%d tools", len(ag.Tools)))

	promptChars := ag.SystemPromptChars
	if promptChars == 0 && ag.SystemPrompt != "" {
		promptChars = len(ag.SystemPrompt)
	}
	prompt := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(fmt.Sprintf("prompt %d chars", promptChars))

	desc := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(ag.Description)

	return []string{name, model, tools, prompt, desc}
}

func (s *agentsSection) renderDetail(ag ports.AgentView) []string {
	subtle := render.Role(s.theme, s.tier, theme.RoleFGSubtle)
	fg := render.Role(s.theme, s.tier, theme.RoleFG)
	accent := render.Role(s.theme, s.tier, theme.RoleAccent)

	originLabel := "Project (workspace: .agents/agents/" + ag.Name + ".md)"
	if ag.Scope == ports.ScopeUser {
		originLabel = "Global (user home: ~/.mivia/agents/" + ag.Name + ".md)"
	}
	if ag.Scope == ports.ScopeBuiltin {
		originLabel = "Built-in (shipped with mivia)"
	}

	lines := []string{
		accent.Bold(true).Render(ag.Name) + "  " + subtle.Render(originLabel),
	}

	if ag.Description != "" {
		lines = append(lines, fg.Render(ag.Description))
	}

	var meta []string
	if ag.Provider != "" || ag.Model != "" {
		meta = append(meta, fmt.Sprintf("model: %s/%s", ag.Provider, ag.Model))
	}
	if ag.MaxTurns > 0 {
		meta = append(meta, fmt.Sprintf("max_turns: %d", ag.MaxTurns))
	}
	if len(ag.Tools) > 0 {
		meta = append(meta, fmt.Sprintf("tools: [%s]", strings.Join(ag.Tools, ", ")))
	}
	if len(ag.Skills) > 0 {
		meta = append(meta, fmt.Sprintf("skills: [%s]", strings.Join(ag.Skills, ", ")))
	}
	if len(ag.MCPServers) > 0 {
		meta = append(meta, fmt.Sprintf("mcp: [%s]", strings.Join(ag.MCPServers, ", ")))
	}
	if len(meta) > 0 {
		lines = append(lines, subtle.Render(strings.Join(meta, "  •  ")))
	}

	promptText := ag.SystemPrompt
	if promptText != "" {
		lines = append(lines, "")
		lines = append(lines, accent.Render("System Prompt:"))
		for _, pl := range strings.Split(promptText, "\n") {
			lines = append(lines, "  "+subtle.Render(pl))
		}
	} else if ag.SystemPromptChars > 0 {
		lines = append(lines, "")
		lines = append(lines, subtle.Render(fmt.Sprintf("System Prompt: (%d characters configured)", ag.SystemPromptChars)))
	}

	return lines
}

func (s *agentsSection) renderEditor() string {
	accent := render.Role(s.theme, s.tier, theme.RoleAccent)
	subtle := render.Role(s.theme, s.tier, theme.RoleFGSubtle)

	title := "Add New Agent"
	if !s.isNew {
		title = "Edit Agent: " + s.editOriginalName
	}

	var lines []string
	lines = append(lines, accent.Bold(true).Render(title))
	lines = append(lines, "")

	for _, f := range s.formFields {
		lines = append(lines, "  "+f.View())
	}

	lines = append(lines, "")
	if s.notice != "" {
		lines = append(lines, "  "+render.Role(s.theme, s.tier, theme.RoleDanger).Render(s.notice))
		lines = append(lines, "")
	}

	hint := "[tab/shift+tab] navigate  [space] cycle  [enter] next  [ctrl+s] save  [esc] cancel"
	lines = append(lines, "  "+subtle.Render(hint))

	return strings.Join(lines, "\n")
}
