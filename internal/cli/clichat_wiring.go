package cli

// clichat_wiring.go wires the clichat seam vars to their cli
// implementations, breaking what would otherwise be an import cycle: the
// hook session, memory store accessors, and stack command helpers stayed in
// the cli router while the chat domain moved to internal/cli/chat.

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hooks/session"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

func init() {
	chat.FlagValueFunc = flagValue
	chat.FlagVarFunc = flagVar
	chat.InstallHookSessionFunc = installHookSession
	// session.Session already implements chat.HookSessionState
	// (RunnableGroups, NoteRunWarnings), so no adapter type is needed here.
	chat.CurrentHookSessionFunc = func() chat.HookSessionState { return session.Current() }
	chat.HookSessionConfiguredFunc = hookSessionConfigured
	chat.HandleSlashHooksFunc = handleSlashHooks
	chat.MemoryOfFunc = func(state *AgentSessionState) memory.Store { return memoryOf(state) }
	chat.MemoryConfigOfFunc = func(state *AgentSessionState) config.MemoryConfig {
		return memoryConfigOf(state)
	}
}
