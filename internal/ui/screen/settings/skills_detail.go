package settings

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

const skillRowGap = 2

func (s *skillsSection) View() string {
	if s.store == nil {
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("Skills is unavailable.")
	}

	selectedRowIdx := -1
	if len(s.skillIndices) > 0 && s.cursor >= 0 && s.cursor < len(s.skillIndices) {
		selectedRowIdx = s.skillIndices[s.cursor]
	}

	listLines := s.renderListLines(selectedRowIdx)
	var detailLines []string
	if sk, ok := s.selectedSkill(); ok {
		detailLines = s.renderDetail(sk)
	}

	avail := s.height
	if s.notice != "" && avail > 1 {
		avail--
	}

	detailHeight := 0
	if len(detailLines) > 0 && avail > 4 {
		needed := len(listLines)
		if needed > avail-4 {
			needed = avail - 4
		}
		if needed < 2 {
			needed = 2
		}
		detailHeight = avail - needed
		if detailHeight > len(detailLines)+1 {
			detailHeight = len(detailLines) + 1
		}
	}
	listHeight := avail - detailHeight
	if listHeight < 1 {
		listHeight = 1
	}

	targetCursorLine := selectedRowIdx
	if targetCursorLine < 0 {
		targetCursorLine = 0
	}
	start, end := render.WindowSlice(len(listLines), targetCursorLine, listHeight)

	var b []byte
	for _, line := range listLines[start:end] {
		b = append(b, line...)
		b = append(b, '\n')
	}

	if detailHeight > 0 && len(detailLines) > 0 {
		b = append(b, '\n')
		maxDetailLines := detailHeight - 1
		if maxDetailLines > len(detailLines) {
			maxDetailLines = len(detailLines)
		}
		for _, dl := range detailLines[:maxDetailLines] {
			b = append(b, dl...)
			b = append(b, '\n')
		}
	}

	if s.notice != "" {
		b = append(b, render.Role(s.theme, s.tier, theme.RoleWarning).Render(s.notice)...)
	}

	return strings.TrimRight(string(b), "\n")
}

func (s *skillsSection) renderListLines(selectedRowIdx int) []string {
	cells := make([][]string, len(s.rows))
	for i, row := range s.rows {
		if !row.isHeader {
			cells[i] = s.renderSkillCells(row.skill)
		}
	}
	aligned := render.Columns(skillRowGap, cells)

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

func (s *skillsSection) renderSkillCells(sk ports.SkillView) []string {
	fg := render.Role(s.theme, s.tier, theme.RoleFG)
	icon := "⚡"
	if s.tier == theme.TierASCII {
		icon = "*"
	}
	name := fg.Bold(true).Render(icon + " /" + sk.Name)

	invocable := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("internal")
	if sk.UserInvocable {
		invocable = render.Role(s.theme, s.tier, theme.RoleSuccess).Render("invocable")
	}

	tools := ""
	if len(sk.Tools) > 0 {
		tools = render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(fmt.Sprintf("%d tools", len(sk.Tools)))
	}

	desc := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(sk.Description)

	return []string{name, invocable, tools, desc}
}

func (s *skillsSection) renderDetail(sk ports.SkillView) []string {
	subtle := render.Role(s.theme, s.tier, theme.RoleFGSubtle)
	fg := render.Role(s.theme, s.tier, theme.RoleFG)
	accent := render.Role(s.theme, s.tier, theme.RoleAccent)

	originLabel := "Project (.agents/skills/" + sk.Name + "/SKILL.md)"
	if sk.Origin == "user" {
		originLabel = "Global (~/.mivia/skills/" + sk.Name + "/SKILL.md)"
	}

	lines := []string{
		accent.Bold(true).Render("/"+sk.Name) + "  " + subtle.Render(originLabel),
	}

	if sk.Description != "" {
		lines = append(lines, fg.Render(sk.Description))
	}

	var meta []string
	if sk.UserInvocable {
		meta = append(meta, "user-invocable")
	} else {
		meta = append(meta, "internal-only")
	}
	if len(sk.Triggers) > 0 {
		meta = append(meta, fmt.Sprintf("triggers: [%s]", strings.Join(sk.Triggers, ", ")))
	}
	if len(sk.Tools) > 0 {
		meta = append(meta, fmt.Sprintf("tools: [%s]", strings.Join(sk.Tools, ", ")))
	}
	if sk.InstructionsChars > 0 {
		meta = append(meta, fmt.Sprintf("instructions: %d chars", sk.InstructionsChars))
	}
	if len(meta) > 0 {
		lines = append(lines, subtle.Render(strings.Join(meta, " • ")))
	}

	if sk.Instructions != "" {
		lines = append(lines, "")
		for _, rawLine := range strings.Split(sk.Instructions, "\n") {
			lines = append(lines, subtle.Render("  "+rawLine))
		}
	}

	return lines
}
