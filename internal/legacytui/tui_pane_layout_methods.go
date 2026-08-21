package legacytui

// chatPaneWidth returns the width shared by chat layout and transcript rendering.
func (m *TUIModel) chatPaneWidth() int {
	return NewChatPaneLayout(m.width, m.sessionsSidebar != nil, m.workflowsSidebar != nil).chatWidth
}
