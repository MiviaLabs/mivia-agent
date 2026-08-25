package settings

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func (s *projectsSection) View() string {
	if s.store == nil {
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("Projects is unavailable.")
	}

	selectedRowIdx := -1
	if len(s.rows) > 0 && s.cursor >= 0 && s.cursor < len(s.rows) {
		selectedRowIdx = s.cursor
	}

	listLines := s.renderListLines(selectedRowIdx)
	var detailLines []string
	if selectedRowIdx >= 0 && selectedRowIdx < len(s.rows) {
		detailLines = s.renderDetail(selectedRowIdx)
	}

	notice := ""
	if s.notice != "" {
		notice = render.Role(s.theme, s.tier, theme.RoleWarning).Render(s.notice)
	}

	return render.SplitListDetail(listLines, detailLines, selectedRowIdx, s.height, notice)
}

func (s *projectsSection) renderListLines(selectedRowIdx int) []string {
	cells := make([][]string, len(s.rows))
	for i, row := range s.rows {
		labelStyle := render.Role(s.theme, s.tier, theme.RoleFG)
		if row.isReadOnly {
			labelStyle = render.Role(s.theme, s.tier, theme.RoleFGSubtle)
		}
		label := labelStyle.Render(row.label)
		val := row.f.View()
		cells[i] = []string{label, val}
	}
	aligned := render.Columns(rowGap, cells)

	listLines := make([]string, 0, len(s.rows))
	for i, line := range aligned {
		marker := "  "
		if i == selectedRowIdx {
			marker = "> "
		}
		listLines = append(listLines, marker+line)
	}
	return listLines
}

func (s *projectsSection) renderDetail(rowIdx int) []string {
	if rowIdx < 0 || rowIdx >= len(s.rows) {
		return nil
	}
	row := s.rows[rowIdx]

	subtle := render.Role(s.theme, s.tier, theme.RoleFGSubtle)
	fg := render.Role(s.theme, s.tier, theme.RoleFG)
	accent := render.Role(s.theme, s.tier, theme.RoleAccent)

	scopeLabel := "Project (.mivia/mivia.toml)"
	if row.isReadOnly {
		scopeLabel = "Read-Only (Workspace Environment)"
	}

	lines := []string{
		accent.Bold(true).Render(strings.TrimSpace(row.label)) + "  " + subtle.Render(scopeLabel),
		"",
		subtle.Render(fmt.Sprintf("key:   [%s]", row.configKey)),
		subtle.Render(fmt.Sprintf("value: %s", row.f.Value())),
		"",
		fg.Render(row.description),
	}

	if s.editing && s.editRowIndex == rowIdx {
		lines = append(lines, "")
		lines = append(lines, render.Role(s.theme, s.tier, theme.RoleAccent).Render("[Editing] Press enter/ctrl+s to save, esc to cancel"))
	} else if !row.isReadOnly {
		lines = append(lines, "")
		if row.isChoice {
			lines = append(lines, subtle.Render("Press [space] or [enter] to cycle choices"))
		} else {
			lines = append(lines, subtle.Render("Press [enter] to edit text"))
		}
	}

	return lines
}
