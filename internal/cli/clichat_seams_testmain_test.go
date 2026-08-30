package cli

// TestMain wires the clichat seam vars for the cli test binary, mirroring
// the production wiring in clichat_wiring.go. The seams must be set before
// any chat-path test runs through the moved code.

import (
	"fmt"
	"os"
	"testing"

	clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"
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
	clichat.FlagValueFunc = flagValue
	clichat.FlagVarFunc = flagVar
	clichat.InstallHookSessionFunc = installHookSession
	// CurrentHookSessionFunc stays as wired by clichat_wiring.go's init: the
	// production closure has the same body the testmain used to install, and
	// keeping the production one lets the wiring file's own lines run.
	clichat.HookSessionConfiguredFunc = hookSessionConfigured
	clichat.HandleSlashHooksFunc = handleSlashHooks
	clichat.MemoryOfFunc = func(state *AgentSessionState) memory.Store { return memoryOf(state) }
	clichat.MemoryConfigOfFunc = func(state *AgentSessionState) config.MemoryConfig {
		return memoryConfigOf(state)
	}
	clichat.OpenStackLedgerFunc = openStackLedger
	clichat.ResolveStackIDFunc = resolveStackID
	// The parseStackWorkflowArgs shim captures clichat.ParseStackWorkflowArgsFunc
	// before anything wires it (nil), so wire the real semantics here through
	// the already-assigned FlagValueFunc instead of the shim.
	clichat.ParseStackWorkflowArgsFunc = func(args []string) (name, stackFlag string, rest []string, err error) {
		stackFlag, rest, _, err = clichat.FlagValueFunc(args, "--stack")
		if err != nil {
			return "", "", nil, err
		}
		if len(rest) != 1 {
			if len(rest) == 0 {
				return "", "", nil, fmt.Errorf("stack: expected a workflow name (or --stack <id> with a workflow name)")
			}
			return "", "", nil, fmt.Errorf("stack: unexpected argument %q", rest[0])
		}
		return rest[0], stackFlag, rest[1:], nil
	}
	// os.Exit skips deferred calls, so restore explicitly.
	code := m.Run()
	restoreHome()
	os.Exit(code)
}
