package cliagents

import "github.com/MiviaLabs/mivia-agent/internal/chat"

// sessionToolRoot keeps hook execution in the same directory as file tools.
// The fallback supports sessions with hand-built registries and tools off.
func sessionToolRoot(sess *chat.Session, fallback string) string {
	if sess != nil {
		if sess.ToolBaseResolver != nil {
			if base := sess.ToolBaseResolver(); base != nil && base.WorkspaceRoot() != "" {
				return base.WorkspaceRoot()
			}
		}
		if reg, _, _ := sess.AgentSurfaceSnapshot(); reg != nil && reg.WorkspaceRoot() != "" {
			return reg.WorkspaceRoot()
		}
	}
	if fallback == "" {
		return "."
	}
	return fallback
}
