package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
)

// scrollSelectedWorkGroup moves the selected group's window by one row.
// Reports whether a group was actually scrolled (so the key can fall
// through to other handlers when the selection is not a group).
func (m *TUIModel) scrollSelectedWorkGroup(down bool) bool {
	if m.selectedBlockID == "" || !strings.HasPrefix(m.selectedBlockID, "work:") {
		return false
	}
	var group *cli.WorkGroup
	for _, g := range cli.FindWorkGroups(m.blocks) {
		if g.Key == m.selectedBlockID {
			gg := g
			group = &gg
			break
		}
	}
	if group == nil || cli.WorkGroupCollapsedDefault(*group, m.workGroupCollapsed) {
		return false
	}
	if m.workGroupScroll == nil {
		m.workGroupScroll = map[string]int{}
	}
	delta := -1
	if down {
		delta = 1
	}
	next := cli.ClampWorkGroupScroll(m.workGroupScroll[m.selectedBlockID]+delta, group.End-group.Start)
	m.workGroupScroll[m.selectedBlockID] = next
	m.renderVP()
	return true
}
