package cli

// IsLocalSlash reports whether command is a slash command this TUI surface
// recognizes. Exported: relocated from internal/legacytui/tui_slash_handlers.go
// (moved to internal/legacytui, its sole caller).
func IsLocalSlash(command string) bool {
	_, ok := FindSlashCommand(command, SlashSurfaceTUI, nil)
	return ok
}
