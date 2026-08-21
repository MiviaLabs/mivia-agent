package legacytui

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
)

const (
	preferredSidebarWidth = 28
	minimumSidebarWidth   = 20
	minimumChatWidth      = 40
	sidebarDividerWidth   = 1
	sidebarDividerPadding = 1
	SidebarDividerLanes   = 2*sidebarDividerPadding + sidebarDividerWidth
)

// chatPaneLayout defines the terminal areas for the chat view.
type chatPaneLayout struct {
	sidebarVisible      bool // left (sessions) sidebar
	sidebarWidth        int
	rightSidebarVisible bool // right (workflows) sidebar
	rightSidebarWidth   int
	dividerPadding      int
	dividerWidth        int
	chatX               int
	chatWidth           int
}

// NewChatPaneLayout returns the areas for the chat view. leftVisible selects
// the sessions sidebar, rightVisible the workflows sidebar. The chat pane
// always keeps minimumChatWidth; a sidebar that does not fit is hidden rather
// than shrinking the chat below the floor.
func NewChatPaneLayout(width int, leftVisible, rightVisible bool) chatPaneLayout {
	layout := chatPaneLayout{chatWidth: width}
	dividerCount := 0
	if leftVisible {
		dividerCount++
	}
	if rightVisible {
		dividerCount++
	}
	if dividerCount == 0 || width < dividerCount*SidebarDividerLanes+minimumChatWidth {
		return layout
	}
	layout.dividerPadding = sidebarDividerPadding
	layout.dividerWidth = sidebarDividerWidth
	avail := width - dividerCount*SidebarDividerLanes - minimumChatWidth
	if leftVisible {
		if avail >= minimumSidebarWidth {
			layout.sidebarVisible = true
			layout.sidebarWidth = minimumSidebarWidth
			if avail >= preferredSidebarWidth {
				layout.sidebarWidth = preferredSidebarWidth
			}
			avail -= layout.sidebarWidth
		}
	}
	if rightVisible {
		if avail >= minimumSidebarWidth {
			layout.rightSidebarVisible = true
			layout.rightSidebarWidth = minimumSidebarWidth
			if avail >= preferredSidebarWidth {
				layout.rightSidebarWidth = preferredSidebarWidth
			}
			avail -= layout.rightSidebarWidth
		}
	}
	if !layout.sidebarVisible && !layout.rightSidebarVisible {
		return chatPaneLayout{chatWidth: width}
	}
	if layout.sidebarVisible {
		layout.chatX = layout.sidebarWidth + SidebarDividerLanes
	}
	layout.chatWidth = width - layout.chatX
	if layout.rightSidebarVisible {
		layout.chatWidth -= layout.rightSidebarWidth + SidebarDividerLanes
	}
	return layout
}

// rightSidebarX returns the first column of the right sidebar for a layout
// where the right sidebar is visible.
func (l chatPaneLayout) rightSidebarX() int {
	return l.chatX + l.chatWidth + SidebarDividerLanes
}

// SidebarDivider returns the separator between the session and chat panes.
func SidebarDivider(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	line := cli.TUIDimStyle.Render(strings.Repeat("│", width))
	return strings.Repeat(line+"\n", height-1) + line
}

// PaneSpacer returns a blank terminal area between adjacent chat panes.
func PaneSpacer(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	line := strings.Repeat(" ", width)
	return strings.Repeat(line+"\n", height-1) + line
}
