package cli

// TestMain wires the clichat seam vars for the cli test binary, mirroring
// the production wiring in clichat_wiring.go. The seams must be set before
// any chat-path test runs through the moved code.

import (
	"fmt"
	workflow "github.com/MiviaLabs/mivia-agent/internal/cli/workflow"
	"os"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cli/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/gittest"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/testenv"
)

// TestMain wires seam defaults before running the package tests.
func TestMain(m *testing.M) {
	gittest.DisableDetachedMaintenance()
	// See internal/testenv: without this, chat-path tests here resolve
	// through workspace.GlobalContextStorePath and write into the
	// developer's real ~/.mivia/context.db.
	restoreHome, err := testenv.IsolateHome()
	if err != nil {
		// Continuing unprotected would write into the real home.
		fmt.Fprintf(os.Stderr, "testenv: %v\n", err)
		os.Exit(1)
	}
	chat.FlagValueFunc = workflow.FlagValue
	chat.FlagVarFunc = workflow.FlagVar
	chat.InstallHookSessionFunc = installHookSession
	// CurrentHookSessionFunc stays as wired by clichat_wiring.go's init: the
	// production closure has the same body the testmain used to install, and
	// keeping the production one lets the wiring file's own lines run.
	chat.HookSessionConfiguredFunc = hookSessionConfigured
	chat.HandleSlashHooksFunc = handleSlashHooks
	chat.MemoryOfFunc = func(state *AgentSessionState) memory.Store { return memoryOf(state) }
	chat.MemoryConfigOfFunc = func(state *AgentSessionState) config.MemoryConfig {
		return memoryConfigOf(state)
	}
	// os.Exit skips deferred calls, so restore explicitly.
	code := m.Run()
	restoreHome()
	os.Exit(code)
}
