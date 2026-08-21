package cli

// chatPaneWidth returns the width shared by chat layout and transcript rendering.
func (m *tuiModel) chatPaneWidth() int {
	return newChatPaneLayout(m.width, m.sessionsSidebar != nil, m.workflowsSidebar != nil).chatWidth
}
