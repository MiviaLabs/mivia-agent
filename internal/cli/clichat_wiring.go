package cli

// clichat_wiring.go wires the clichat seam vars to their cli
// implementations, breaking what would otherwise be an import cycle: the
// hook session, memory store accessors, and stack command helpers stayed in
// the cli router while the chat domain moved to internal/clichat.

import (
	clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hooksession"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

func init() {
	clichat.FlagValueFunc = flagValue
	clichat.FlagVarFunc = flagVar
	clichat.InstallHookSessionFunc = installHookSession
	// hooksession.Session already implements clichat.HookSessionState
	// (RunnableGroups, NoteRunWarnings), so no adapter type is needed here.
	clichat.CurrentHookSessionFunc = func() clichat.HookSessionState { return hooksession.Current() }
	clichat.HookSessionConfiguredFunc = hookSessionConfigured
	clichat.HandleSlashHooksFunc = handleSlashHooks
	clichat.MemoryOfFunc = func(state *AgentSessionState) memory.Store { return memoryOf(state) }
	clichat.MemoryConfigOfFunc = func(state *AgentSessionState) config.MemoryConfig {
		return memoryConfigOf(state)
	}
}
