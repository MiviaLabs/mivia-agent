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
	sidebarVisible bool
	sidebarWidth   int
	dividerPadding int
	dividerWidth   int
	chatX          int
	chatWidth      int
}

// newChatPaneLayout returns the areas for the chat view.
func newChatPaneLayout(width int, sidebarVisible bool) chatPaneLayout {
	layout := chatPaneLayout{chatWidth: width}
	if !sidebarVisible || width < minimumSidebarWidth+sidebarDividerLanes+minimumChatWidth {
		return layout
	}

	layout.sidebarVisible = true
	layout.dividerPadding = sidebarDividerPadding
	layout.dividerWidth = sidebarDividerWidth
	layout.sidebarWidth = minimumSidebarWidth
	if width >= preferredSidebarWidth+sidebarDividerLanes+minimumChatWidth {
		layout.sidebarWidth = preferredSidebarWidth
	}
	layout.chatX = layout.sidebarWidth + sidebarDividerLanes
	layout.chatWidth = width - layout.chatX
	return layout
}

// sidebarDivider returns the separator between the session and chat panes.
func sidebarDivider(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	line := tuiDimStyle.Render(strings.Repeat("│", width))
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
	return newChatPaneLayout(m.width, m.sessionsSidebar != nil).chatWidth
}
