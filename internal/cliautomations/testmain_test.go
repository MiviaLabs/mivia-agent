package cliautomations

// testmain_test.go isolates HOME before any test in this package runs.
// This package resolves user configuration (config.Load merges the
// USER-level config even behind an explicit ConfigPath) and opens
// checkpoint stores under a workspace/user .mivia directory, so an
// unisolated HOME would let ambient developer-machine configuration leak
// into test outcomes and could write into the real home directory. See
// internal/testenv and internal/testenv/home_isolation_gate_test.go,
// which enforces that every package resolving user configuration defines
// this TestMain.

import (
	"fmt"
	"os"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/testenv"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// wireProcessSeams installs the cliagents function variables internal/cli's
// init() wires in the real binary. A test binary that does not import
// internal/cli leaves them nil, and the consequences are not symmetric:
//
//   - WireWorkflowToolOptionsVar is called UNCONDITIONALLY by
//     cliagents.BuildToolsForRoot, so a nil one panics. Stubbed to a no-op,
//     the same way internal/uiadapter's own tests do; nothing here
//     exercises real workflow tool wiring.
//   - NewSessionDispatcherVar and RemainderSpoolFromRegistryVar are
//     nil-GUARDED by cliagents.AttachRebuiltSurface, which returns
//     "not published" without an error when either is missing. Leaving them
//     nil would make every spawned session in this package silently get no
//     surface at all, and every assertion about that session vacuous. They
//     are wired to the real implementations so the tests exercise the real
//     attach.
func wireProcessSeams() {
	cliagents.WireWorkflowToolOptionsVar = func(
		*tools.DefaultOptions, string, *config.Resolved, func() *events.Bus, bool, bool, ledger.LedgerRepository,
	) {
	}
	cliagents.NewSessionDispatcherVar = clichat.NewSessionDispatcher
	cliagents.RemainderSpoolFromRegistryVar = clichat.RemainderSpoolFromRegistry
	cliagents.AdvertisedSessionToolSpecsVar = clichat.AdvertisedSessionToolSpecs
	cliagents.ContextDispatcherForVar = clichat.ContextDispatcherFor
	// clichat.NewSessionDispatcher reads both hook seams unconditionally
	// (dispatcher.go's HooksConfigured/HookGroups fields), so a nil one
	// panics rather than degrading. Stubbed to "no hooks configured",
	// mirroring internal/clichat's own TestMain and internal/uiadapter's.
	clichat.HookSessionConfiguredFunc = func() bool { return false }
	clichat.CurrentHookSessionFunc = func() clichat.HookSessionState { return stubHookSession{} }
}

// stubHookSession satisfies clichat.HookSessionState with no hooks.
type stubHookSession struct{}

func (stubHookSession) RunnableGroups() []hooks.Group { return nil }
func (stubHookSession) NoteRunWarnings([]string)      {}

func TestMain(m *testing.M) {
	wireProcessSeams()
	restoreHome, err := testenv.IsolateHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testenv: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	restoreHome()
	os.Exit(code)
}
