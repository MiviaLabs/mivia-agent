package legacytui

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
)

// RenderSuggestPanel implements render suggest panel.
func RenderSuggestPanel(state SuggestState, termW, maxH int) (string, cli.Rect) {
	if !state.open || len(state.commands) == 0 || termW <= 0 || maxH < 1 {
		return "", cli.Rect{}
	}
	items := make([]string, 0, len(state.commands))
	for _, command := range state.commands {
		glyph := "•"
		if command.Kind == cli.SlashKindSkill {
			glyph = cli.GlyphLozenge
			if command.Origin == "project" {
				glyph = cli.GlyphDiamond
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
	return RenderOverlayWindow(items, state.selected, SuggestWindowRows, termW, maxH, " commands "+fmt.Sprintf("(%d)", len(state.commands)), "")
}

// SuggestOverlayRect implements suggest overlay rect.
func SuggestOverlayRect(m *TUIModel, panel string, panelSize cli.Rect) cli.Rect {
	termW, termH := cli.Max(1, m.width), cli.Max(8, m.height)
	if panel == "" || panelSize.W <= 0 || panelSize.H <= 0 {
		return cli.Rect{}
	}
	pane := NewChatPaneLayout(termW, m.sessionsSidebar != nil, m.workflowsSidebar != nil)
	composerTop := m.suggestComposerTop()
	y := cli.Max(1, composerTop-panelSize.H)
	x := pane.chatX + cli.Max(0, cli.Min(2, pane.chatWidth-panelSize.W))
	return cli.Rect{X: x, Y: y, W: panelSize.W, H: cli.Min(panelSize.H, termH-y)}
}
