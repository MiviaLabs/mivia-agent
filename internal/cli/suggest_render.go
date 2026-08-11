package cli

import (
	"fmt"
)

func renderSuggestPanel(state suggestState, termW, maxH int) (string, rect) {
	if !state.open || len(state.commands) == 0 || termW <= 0 || maxH < 1 {
		return "", rect{}
	}
	items := make([]string, 0, len(state.commands))
	for _, command := range state.commands {
		glyph := "•"
		if command.Kind == slashKindSkill {
			glyph = glyphLozenge
			if command.Origin == "project" {
				glyph = glyphDiamond
			}
		}
		label := command.Name
		if command.ArgsHint != "" {
			label += " " + command.ArgsHint
		}
		item := glyph + " " + label
		if command.Description != "" {
			item += "  " + command.Description
		}
		items = append(items, item)
	}
	return renderOverlayWindow(items, state.selected, suggestWindowRows, termW, maxH, " commands "+fmt.Sprintf("(%d)", len(state.commands)), "")
}

func suggestOverlayRect(m *tuiModel, panel string, panelSize rect) rect {
	termW, termH := max(1, m.width), max(8, m.height)
	if panel == "" || panelSize.w <= 0 || panelSize.h <= 0 {
		return rect{}
	}
	pane := newChatPaneLayout(termW, m.sessionsSidebar != nil, m.workflowsSidebar != nil)
	composerTop := m.suggestComposerTop()
	y := max(1, composerTop-panelSize.h)
	x := pane.chatX + max(0, min(2, pane.chatWidth-panelSize.w))
	return rect{x: x, y: y, w: panelSize.w, h: min(panelSize.h, termH-y)}
}

func (m *tuiModel) suggestComposerTop() int {
	// The live panel is an overlay and holds no layout band, so the composer
	// sits directly below the full-height viewport.
	return 1 + m.viewport.Height
}
