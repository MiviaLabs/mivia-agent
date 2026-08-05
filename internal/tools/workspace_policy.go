package tools

// WorkspaceWriteCapable reports whether a registered tool surface can change workspace state.
func WorkspaceWriteCapable(reg *Registry, names []string) bool {
	if reg == nil {
		return false
	}
	for _, name := range names {
		if _, ok := reg.Get(name); !ok {
			continue
		}
		if name == RunCommandToolName || reg.Capability(name, nil).Class == ExecutionWrite {
			return true
		}
	}
	return false
}
