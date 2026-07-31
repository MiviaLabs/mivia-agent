package cli

func isLocalSlash(command string) bool {
	_, ok := findSlashCommand(command, slashSurfaceTUI, nil)
	return ok
}
