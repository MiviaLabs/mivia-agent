package tools

import (
	"path/filepath"
	"strings"
)

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

// isWriteDeniedPath reports whether rel names a protected workspace file or
// is inside a protected workspace directory.
func isWriteDeniedPath(rel string, denylist []string) bool {
	path := filepath.ToSlash(filepath.Clean(rel))
	for _, denied := range denylist {
		denied = filepath.ToSlash(filepath.Clean(strings.TrimSpace(denied)))
		if denied != "." && (path == denied || strings.HasPrefix(path, denied+"/")) {
			return true
		}
	}
	return false
}
