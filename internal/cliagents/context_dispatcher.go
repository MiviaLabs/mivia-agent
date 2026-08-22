package cliagents

import (
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// ContextDispatcherWiring carries the context-preparation state from the
// session setup into the agent surface builders. The function that assembles
// it lives in internal/cli (contextDispatcherFor) and is injected here via
// ContextDispatcherForVar so the import direction stays inward. See
// internal/cli/cliagents_wiring.go for the wiring init.
type ContextDispatcherWiring struct {
	Preparation      contextmgr.PreparationManager
	PreparationInput contextmgr.PrepareInput
	SharedSQLite     *storage.SQLite
}

// ContextDispatcherForVar is wired at process start by
// internal/cli/cliagents_wiring.go to cli's contextDispatcherFor.
var ContextDispatcherForVar func(*chat.Session, config.SubagentConfig) ContextDispatcherWiring
