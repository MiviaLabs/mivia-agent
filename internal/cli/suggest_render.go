package cli

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func renderSuggestPanel(state suggestState, termW, maxH int) (string, rect) {
	if !state.open || len(state.commands) == 0 || termW <= 0 || maxH < 1 {
		return "", rect{}
	}
	w := min(termW, max(24, min(72, termW-4)))
	if maxH < 3 {
		command := state.commands[state.selected]
		row := "› " + command.Name
		if command.ArgsHint != "" {
			row += " " + command.ArgsHint
		}
		if remaining := len(state.commands) - 1; remaining > 0 {
			row += fmt.Sprintf("  +%d", remaining)
		}
		return fitDialogRow(row, w), rect{w: w, h: 1}
	}
	visible := min(suggestWindowRows, len(state.commands))
	h := min(maxH, visible+2)
	if h < 2 {
		return "", rect{}
	}
	pageRows := max(0, h-2)
	start := 0
	if state.selected >= pageRows && pageRows > 0 {
		start = state.selected - pageRows + 1
	}
	rows := make([]string, 0, pageRows)
	for i := 0; i < pageRows && start+i < len(state.commands); i++ {
		index := start + i
		command := state.commands[index]
		prefix := "  "
		if index == state.selected {
			prefix = "› "
		}
		glyph := "•"
		if command.Kind == slashKindSkill {
			glyph = "◇"
			if command.Origin == "project" {
				glyph = "◆"
			}
		}
		label := command.Name
		if command.ArgsHint != "" {
			label += " " + command.ArgsHint
		}
		row := prefix + glyph + " " + label
		if command.Description != "" {
			row += "  " + command.Description
		}
		rows = append(rows, row)
	}
	footer := ""
	if remaining := len(state.commands) - (start + len(rows)); remaining > 0 {
		footer = fmt.Sprintf("+%d more", remaining)
	}
	l := dialogLayout{rect: rect{w: w, h: h}, innerW: max(0, w-4), pageH: pageRows, frameCols: 4, frameRows: 2}
	panel := renderDialogFrame(" commands "+fmt.Sprintf("(%d)", len(state.commands)), rows, footer, l)
	return panel, rect{w: lipgloss.Width(panel), h: lipgloss.Height(panel)}
}

func suggestOverlayRect(m *tuiModel, panel string, panelSize rect) rect {
	termW, termH := max(1, m.width), max(8, m.height)
	if panel == "" || panelSize.w <= 0 || panelSize.h <= 0 {
		return rect{}
	}
	composerTop := m.suggestComposerTop()
	y := max(1, composerTop-panelSize.h)
	return rect{x: max(0, min(2, termW-panelSize.w)), y: y, w: panelSize.w, h: min(panelSize.h, termH-y)}
}

func (m *tuiModel) suggestComposerTop() int {
	liveH := 0
	if live := m.renderLivePanel(max(1, m.width), time.Now()); live != "" {
		liveH = lipgloss.Height(live)
	}
	return 1 + m.viewport.Height + liveH
}
