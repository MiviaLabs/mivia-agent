package cli

import "strings"

const (
	preferredSidebarWidth = 28
	minimumSidebarWidth   = 20
	minimumChatWidth      = 40
	sidebarDividerWidth   = 1
	sidebarDividerPadding = 1
	sidebarDividerLanes   = 2*sidebarDividerPadding + sidebarDividerWidth
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

// newChatPaneLayout returns the areas for the chat view. leftVisible selects
// the sessions sidebar, rightVisible the workflows sidebar. The chat pane
// always keeps minimumChatWidth; a sidebar that does not fit is hidden rather
// than shrinking the chat below the floor.
func newChatPaneLayout(width int, leftVisible, rightVisible bool) chatPaneLayout {
	layout := chatPaneLayout{chatWidth: width}
	dividerCount := 0
	if leftVisible {
		dividerCount++
	}
	if rightVisible {
		dividerCount++
	}
	if dividerCount == 0 || width < dividerCount*sidebarDividerLanes+minimumChatWidth {
		return layout
	}
	layout.dividerPadding = sidebarDividerPadding
	layout.dividerWidth = sidebarDividerWidth
	avail := width - dividerCount*sidebarDividerLanes - minimumChatWidth
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
		layout.chatX = layout.sidebarWidth + sidebarDividerLanes
	}
	layout.chatWidth = width - layout.chatX
	if layout.rightSidebarVisible {
		layout.chatWidth -= layout.rightSidebarWidth + sidebarDividerLanes
	}
	return layout
}

// rightSidebarX returns the first column of the right sidebar for a layout
// where the right sidebar is visible.
func (l chatPaneLayout) rightSidebarX() int {
	return l.chatX + l.chatWidth + sidebarDividerLanes
}

// sidebarDivider returns the separator between the session and chat panes.
func sidebarDivider(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	line := TUIDimStyle.Render(strings.Repeat("│", width))
	return strings.Repeat(line+"\n", height-1) + line
}

// paneSpacer returns a blank terminal area between adjacent chat panes.
func paneSpacer(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	line := strings.Repeat(" ", width)
	return strings.Repeat(line+"\n", height-1) + line
}

// chatPaneWidth returns the width shared by chat layout and transcript rendering.
func (m *tuiModel) chatPaneWidth() int {
	return newChatPaneLayout(m.width, m.sessionsSidebar != nil, m.workflowsSidebar != nil).chatWidth
}
