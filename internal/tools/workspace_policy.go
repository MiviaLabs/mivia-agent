package tools

import (
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
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

// writePathDenied reports whether a requested write target is denied by the
// blocklist. Both the resolved relative path (where the write lands) and the
// lexical workspace-relative path (what the caller named) must pass: an
// in-workspace symlink can otherwise redirect a blocked name to an allowed
// directory, and a resolved-only check would miss it. A requested name that
// cannot be derived as a workspace-relative path is denied (fail closed).
func writePathDenied(ws *workspace.Root, userPath, resolvedRel string, denylist []string) bool {
	if isWriteDeniedPath(resolvedRel, denylist) {
		return true
	}
	lex, err := ws.LexicalRel(userPath)
	if err != nil {
		return true
	}
	return isWriteDeniedPath(lex, denylist)
}

// isWriteDeniedPath reports whether rel names a protected workspace file or
// is inside a protected workspace directory.
func isWriteDeniedPath(rel string, denylist []string) bool {
	// Lowercase both sides so a blocklist entry matches regardless of the
	// case used in the config or on a case-insensitive filesystem, mirroring
	// the secret-path filter (internal/secretpath). Over-blocking a
	// differently-cased directory is the safe direction for a deny list.
	path := strings.ToLower(filepath.ToSlash(filepath.Clean(rel)))
	for _, denied := range denylist {
		denied = strings.ToLower(filepath.ToSlash(filepath.Clean(strings.TrimSpace(denied))))
		if denied != "." && (path == denied || strings.HasPrefix(path, denied+"/")) {
			return true
		}
	}
	return false
}
