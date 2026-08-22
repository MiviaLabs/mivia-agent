package cli

// clichat_wiring.go wires the clichat seam vars to their cli
// implementations, breaking what would otherwise be an import cycle: the
// hook session, memory store accessors, and stack command helpers stayed in
// the cli router while the chat domain moved to internal/clichat.

import (
	clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

// hookSessionStateAdapter exposes the cli hook session's read surface.
type hookSessionStateAdapter struct{ s *hookSession }

// RunnableGroups returns the hook groups that tool calls may run.
func (a hookSessionStateAdapter) RunnableGroups() []hooks.Group { return a.s.runnable() }

// NoteRunWarnings records bounded diagnostics from executed hooks.
func (a hookSessionStateAdapter) NoteRunWarnings(w []string) { a.s.noteRunWarnings(w) }

func init() {
	clichat.FlagValueFunc = flagValue
	clichat.FlagVarFunc = flagVar
	clichat.InstallHookSessionFunc = installHookSession
	clichat.CurrentHookSessionFunc = func() clichat.HookSessionState {
		return hookSessionStateAdapter{currentHookSession()}
	}
	clichat.HookSessionConfiguredFunc = hookSessionConfigured
	clichat.HandleSlashHooksFunc = handleSlashHooks
	clichat.MemoryOfFunc = func(state *AgentSessionState) memory.Store { return memoryOf(state) }
	clichat.MemoryConfigOfFunc = func(state *AgentSessionState) config.MemoryConfig {
		return memoryConfigOf(state)
	}
}
