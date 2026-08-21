package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func newTestSessionForModel(model string) *chat.Session {
	return chat.NewSession(&config.Resolved{Model: model}, nil)
}

// stubWorkspaceRestart satisfies workspaceRestartError without importing the
// concrete WorkspaceRestart type. Tests that call validateWorkspaceRestart
// directly use this instead of a WorkspaceRestart struct literal, so they
// stay buildable once WorkspaceRestart moves to package legacytui.
type stubWorkspaceRestart struct {
	dir, resumeSessionName string
	wt                     contextstate.WorktreeInstance
}

func (s stubWorkspaceRestart) Error() string { return "restart chat in workspace " + s.dir }

func (s stubWorkspaceRestart) WorkspaceRestartInfo() (string, string, contextstate.WorktreeInstance) {
	return s.dir, s.resumeSessionName, s.wt
}
