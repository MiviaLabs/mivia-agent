package clichat

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/storage"

	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
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
	FlagValueFunc = flagValueLocal
	FlagVarFunc = flagVarLocal
	wireMemorySeams()
	wireHookSeams()
	wireOrchestrationSeams()
	wireCliagentsSeams()
	wireWorkflowSeams()
	wireCliworkflowSeams()
	wireStackSeams()
	os.Exit(m.Run())
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

// wireOrchestrationSeams wires the cliorchestrate tool registration seam.
func wireOrchestrationSeams() {
	cliorchestrate.SessionToolRegister = RegisterSessionTool
}

// wireCliagentsSeams wires the cliagents seam vars that production wiring
// in internal/cli sets; the implementations now live in this package.
func wireCliagentsSeams() {
	cliagents.NewSessionDispatcherVar = NewSessionDispatcher
	cliagents.RemainderSpoolFromRegistryVar = RemainderSpoolFromRegistry
	cliagents.SummaryWiringVar = summaryWiring
	cliagents.AdvertisedSessionToolSpecsVar = advertisedSessionToolSpecs
	cliagents.ContextDispatcherForVar = contextDispatcherFor
	cliagents.BuiltInSlashTokensVar = builtInSlashTokenSetLocal
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
	cliagents.WireWorkflowToolOptionsVar = cliworkflow.WireWorkflowToolOptions
	cliworktree.OpenRepositoryContextStoreFunc = OpenRepositoryContextStore
}

// wireCliworkflowSeams mirrors internal/cli/cliworkflow_wiring.go for the
// test binary; every implementation now lives in this package.
func wireCliworkflowSeams() {
	cliworkflow.ContextStorePath = ContextStorePath
	cliworkflow.ApplyPrivacyPolicyFunc = applyPrivacyPolicy
	cliworkflow.LogMCPWarningsFunc = logMCPWarnings
	cliworkflow.SliceErrorsFunc = sliceErrors
	cliworkflow.FlagValueFunc = flagValueLocal
	cliworkflow.FlagVarFunc = flagVarLocal
	cliworkflow.InstallHookSessionFunc = installHookSessionStub
	cliworkflow.LoadChatSkillsFunc = loadChatSkills
	cliworkflow.NewSessionDispatcherFunc = NewSessionDispatcher
	cliworkflow.InitCoordinatorFunc = cliorchestrate.InitCoordinator
	cliworkflow.InjectBaselineMessagingFunc = injectBaselineMessaging
	cliworkflow.MessagingDisallowedFunc = messagingDisallowed
	cliworkflow.SessionAutoDeliveryRepairLoopFunc = sessionAutoDeliveryRepairLoop
	cliworkflow.ErrStackAwaitsGrant = errStackAwaitsGrant
	cliworkflow.StackingDriveAllowPublishFunc = stackingDriveAllowPublish
	cliworkflow.ClassifyStackPlanRunDeliveryFunc = func(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string, oracle bool) cliworkflow.StackPlanRunGate {
		return cliworkflow.StackPlanRunGate(int(classifyStackPlanRunDelivery(ctx, root, store, repo, runID, oracle)))
	}
	cliworkflow.StackPlanRunFailureReasonFunc = stackPlanRunFailureReason
	cliworkflow.ErrFailedStackPlanRunFunc = errFailedStackPlanRun
	cliworkflow.ErrUndrivenStackPlanRunFunc = errUndrivenStackPlanRun
	cliworkflow.LoadStackPlanOutputFunc = loadStackPlanOutput
	cliworkflow.ParseStackPlanOutputFunc = parseStackPlanOutput
	cliworkflow.StackPlanInputsFunc = stackPlanInputs
	cliworkflow.LoadAllStackChunksForDriveFunc = loadAllStackChunksForDrive
	cliworkflow.SeedStackLedgerFunc = seedStackLedger
	cliworkflow.DriveStackToCompletionFunc = driveStackToCompletion
	cliworkflow.LoadAllStackChunksFunc = loadAllStackChunks
	cliworkflow.StackTaskMapFunc = stackTaskMap
	cliworkflow.StackMergedSetFunc = stackMergedSet
	cliworkflow.AllChunksMergedFunc = allChunksMerged
	cliworkflow.StackRunRefFunc = stackRunRef
	cliworkflow.StackHeadBranchFunc = stackHeadBranch
	cliworkflow.StackRunHeadCommitFunc = stackRunHeadCommit
	cliworkflow.StackRunPushedFunc = stackRunPushed
	cliworkflow.StackRunPublishWithheldFunc = stackRunPublishWithheld
	cliworkflow.StackDecomposedChunksFunc = stackDecomposedChunks
	cliworkflow.OpenContextStoreFunc = openContextStore
	cliworkflow.InjectSkillResourceToolFunc = InjectSkillResourceTool
	cliworkflow.GitMergeCheckFunc = GitMergeCheck
	cliworkflow.SettleStackPlanRunIfCompleteFn = settleStackPlanRunIfComplete
	cliworkflow.InitCLIDefaults()
}

// installHookSessionStub stands in for cli.installHookSession in the
// cliworkflow seam wiring.
func installHookSessionStub(string, bool, bool) (func(), error) { return func() {}, nil }

// wireStackSeams wires the stack command helper seams with the local
// implementations that moved into this package.
func wireStackSeams() {
	OpenStackLedgerFunc = openStackLedger
	ResolveStackIDFunc = resolveStackID
	ParseStackWorkflowArgsFunc = parseStackWorkflowArgs
}
