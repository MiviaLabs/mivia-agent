package chat

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/gittest"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/testenv"

	"github.com/MiviaLabs/mivia-agent/internal/cli/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cli/orchestrate"
	cliworkflow "github.com/MiviaLabs/mivia-agent/internal/cli/workflow"
	cliworktree "github.com/MiviaLabs/mivia-agent/internal/cli/worktree"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

// TestMain wires the cli seam vars for the test binary. In production
// internal/cli/clichat_wiring.go wires them; tests cannot import internal/cli,
// so faithful local copies of the flag parsers stand in.

// flagValueLocal is a faithful copy of cli.flagValue.
func flagValueLocal(args []string, names ...string) (string, []string, bool, error) {
	out := make([]string, 0, len(args))
	var val string
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		matched := false
		for _, n := range names {
			if a == n {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return "", nil, found, fmt.Errorf("%s requires a value", n)
				}
				val = args[i+1]
				found = true
				i++
				matched = true
				break
			}
			if strings.HasPrefix(a, n+"=") {
				val = strings.TrimPrefix(a, n+"=")
				found = true
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, a)
		}
	}
	return val, out, found, nil
}

// flagVarLocal is a faithful copy of cli.flagVar.
func flagVarLocal(args []string, names ...string) ([]string, []string, bool, error) {
	var vals []string
	rest := make([]string, 0, len(args))
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		matched := false
		for _, n := range names {
			if a == n {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return nil, nil, found, fmt.Errorf("%s requires a value", n)
				}
				vals = append(vals, args[i+1])
				found = true
				i++
				matched = true
				break
			}
			if strings.HasPrefix(a, n+"=") {
				vals = append(vals, strings.TrimPrefix(a, n+"="))
				found = true
				matched = true
				break
			}
		}
		if !matched {
			rest = append(rest, a)
		}
	}
	return vals, rest, found, nil
}

// TestMain wires seam defaults before running the package tests.
func TestMain(m *testing.M) {
	gittest.DisableDetachedMaintenance()
	// Isolate the home directory first. Several paths here resolve through
	// workspace.GlobalContextStorePath, which without this writes test
	// sessions, checkpoints, and worktree rows into the developer's real
	// ~/.mivia/context.db - permanently, and indistinguishably from real
	// sessions. See internal/testenv.
	restoreHome, err := testenv.IsolateHome()
	if err != nil {
		// Continuing unprotected would write into the real home.
		fmt.Fprintf(os.Stderr, "testenv: %v\n", err)
		os.Exit(1)
	}
	FlagValueFunc = flagValueLocal
	FlagVarFunc = flagVarLocal
	wireMemorySeams()
	wireHookSeams()
	wireCliagentsSeams()
	wireWorkflowSeams()
	wireCliworkflowSeams()
	// os.Exit skips deferred calls, so restore the environment explicitly
	// before exiting with the suite's own status.
	code := m.Run()
	restoreHome()
	os.Exit(code)
}

// wireMemorySeams wires the memory seam vars with the faithful cli logic.
func wireMemorySeams() {
	MemoryOfFunc = func(state *AgentSessionState) memory.Store {
		if state == nil {
			return nil
		}
		return state.Memory
	}
	MemoryConfigOfFunc = func(state *AgentSessionState) config.MemoryConfig {
		if state == nil {
			return config.MemoryConfig{}
		}
		return state.MemoryConfig
	}
}

// emptyHookSession is the no-hooks stand-in for seam wiring in tests.
type emptyHookSession struct{}

// RunnableGroups returns no hook groups.
func (emptyHookSession) RunnableGroups() []hooks.Group { return nil }

// NoteRunWarnings discards warnings.
func (emptyHookSession) NoteRunWarnings([]string) {}

// wireHookSeams wires the hook seam vars with no-hook defaults.
func wireHookSeams() {
	HookSessionConfiguredFunc = func() bool { return false }
	CurrentHookSessionFunc = func() HookSessionState { return emptyHookSession{} }
	InstallHookSessionFunc = func(string, bool, bool) (func(), error) { return func() {}, nil }
}

// wireCliagentsSeams wires the cliagents seam vars that production wiring
// in internal/cli sets; the implementations now live in this package.
func wireCliagentsSeams() {
	agents.NewSessionDispatcherVar = NewSessionDispatcher
	agents.RemainderSpoolFromRegistryVar = RemainderSpoolFromRegistry
	agents.SummaryWiringVar = summaryWiring
	agents.AdvertisedSessionToolSpecsVar = advertisedSessionToolSpecs
	agents.ContextDispatcherForVar = contextDispatcherFor
	agents.BuiltInSlashTokensVar = builtInSlashTokenSetLocal
}

// builtInSlashTokenSetLocal mirrors cli.builtInSlashTokenSet.
func builtInSlashTokenSetLocal() map[string]struct{} {
	cmds := builtInSlashCommands()
	out := make(map[string]struct{}, len(cmds))
	for _, cmd := range cmds {
		out[cmd.Name] = struct{}{}
	}
	return out
}

// wireWorkflowSeams wires the workflow tool options seam and the worktree
// context store seam for the test binary.
func wireWorkflowSeams() {
	agents.WireWorkflowToolOptionsVar = cliworkflow.WireWorkflowToolOptions
	cliworktree.OpenRepositoryContextStoreFunc = OpenRepositoryContextStore
}

// wireCliworkflowSeams mirrors internal/cli/cliworkflow_wiring.go for the
// test binary; every implementation now lives in this package.
func wireCliworkflowSeams() {
	cliworkflow.InstallHookSessionFunc = installHookSessionStub
	cliworkflow.LoadChatSkillsFunc = loadChatSkills
	cliworkflow.NewSessionDispatcherFunc = NewSessionDispatcher
	cliworkflow.InitCoordinatorFunc = orchestrate.InitCoordinator
	cliworkflow.InjectBaselineMessagingFunc = injectBaselineMessaging
	cliworkflow.InitCLIDefaults()
}

// installHookSessionStub stands in for cli.installHookSession in the
// cliworkflow seam wiring.
func installHookSessionStub(string, bool, bool) (func(), error) { return func() {}, nil }
