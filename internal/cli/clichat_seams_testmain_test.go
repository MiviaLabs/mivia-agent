package cli

// TestMain wires the clichat seam vars for the cli test binary, mirroring
// the production wiring in clichat_wiring.go. The seams must be set before
// any chat-path test runs through the moved code.

import (
	"os"
	"testing"

	clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

// TestMain wires seam defaults before running the package tests.
func TestMain(m *testing.M) {
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
	clichat.OpenStackLedgerFunc = openStackLedger
	clichat.ResolveStackIDFunc = resolveStackID
	clichat.ParseStackWorkflowArgsFunc = parseStackWorkflowArgs
	os.Exit(m.Run())
}
