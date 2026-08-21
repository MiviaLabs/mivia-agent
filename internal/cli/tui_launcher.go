package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// tuiLauncher runs the interactive TUI program. Wired by cmd/mivia's process
// startup to legacytui.RunTUI once that package exists; nil means unwired.
var tuiLauncher func(sess *chat.Session, res *config.Resolved, toolsOn bool, agentState *AgentSessionState, resumeSessionName string) error

// SetTUILauncher wires the TUI backend. Called once during process startup,
// before any command that might launch the TUI runs.
func SetTUILauncher(fn func(sess *chat.Session, res *config.Resolved, toolsOn bool, agentState *AgentSessionState, resumeSessionName string) error) {
	tuiLauncher = fn
}
