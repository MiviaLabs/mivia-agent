package clichat

// EventPreview returns preview, falling back to fallback when preview is
// empty. Relocated from internal/legacytui/tui_events.go: needed unqualified
// there and by the classic-mode UI and the JSON event writer here.
func EventPreview(preview, fallback string) string {
	if preview != "" {
		return preview
	}
	return fallback
}

// FormatAgentUnavailable renders an agent-switch error for display. Relocated
// from internal/legacytui/agent_dialog.go: needed unqualified there and by
// the classic-mode slash-command handlers here.
func FormatAgentUnavailable(err error) string {
	if err == nil {
		return "agent switch failed"
	}
	return err.Error()
}
