package cli

import (
	"fmt"
)

func renderSuggestPanel(state suggestState, termW, maxH int) (string, Rect) {
	if !state.open || len(state.commands) == 0 || termW <= 0 || maxH < 1 {
		return "", Rect{}
	}
	items := make([]string, 0, len(state.commands))
	for _, command := range state.commands {
		glyph := "•"
		if command.Kind == slashKindSkill {
			glyph = GlyphLozenge
			if command.Origin == "project" {
				glyph = GlyphDiamond
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
	return RenderOverlayWindow(items, state.selected, suggestWindowRows, termW, maxH, " commands "+fmt.Sprintf("(%d)", len(state.commands)), "")
}

func suggestOverlayRect(m *tuiModel, panel string, panelSize Rect) Rect {
	termW, termH := Max(1, m.width), Max(8, m.height)
	if panel == "" || panelSize.W <= 0 || panelSize.H <= 0 {
		return Rect{}
	}
	pane := newChatPaneLayout(termW, m.sessionsSidebar != nil, m.workflowsSidebar != nil)
	composerTop := m.suggestComposerTop()
	y := Max(1, composerTop-panelSize.H)
	x := pane.chatX + Max(0, Min(2, pane.chatWidth-panelSize.W))
	return Rect{X: x, Y: y, W: panelSize.W, H: Min(panelSize.H, termH-y)}
}

func (m *tuiModel) suggestComposerTop() int {
	// The live panel is an overlay and holds no layout band, so the composer
	// sits directly below the full-height viewport.
	return 1 + m.viewport.Height
}
