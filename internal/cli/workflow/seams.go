// Package cliworkflow holds the workflow CLI domain: workflow run, resume,
// deliver, status, events, approve, reject, cancel, cleanup, delete, and gc
// commands, the stack driver (moved here from internal/cli/chat), the session
// workflow tool engine, and the workflow snapshot and verifier pinning
// machinery.
//
// The package must never import internal/cli (the CLI composition root
// imports this package). The remaining nil seam vars below cover helpers
// still owned by internal/cli/chat and the cli root, assigned by the init()
// in internal/cli/cliworkflow_wiring.go; the stack-domain vars are
// initialized overrides over this package's own implementations.
package workflow

import (
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"

	cliagents "github.com/MiviaLabs/mivia-agent/internal/cli/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Seams over internal/cli helpers. All are nil until
// internal/cli/cliworkflow_wiring.go assigns them. Stack-domain vars are
// initialized above to this package's own implementations.

var (
	// InstallHookSessionFunc stands for cli.installHookSession (hooks_command.go).
	InstallHookSessionFunc func(workspaceRoot string, staleBypass, quiet bool) (func(), error)

	// LoadChatSkillsFunc stands for cli.loadChatSkills (chat_command.go).
	LoadChatSkillsFunc func(wsRoot string) (*skills.Registry, error)

	// NewSessionDispatcherFunc stands for cli.NewSessionDispatcher (dispatcher.go).
	NewSessionDispatcherFunc func(opts cliagents.SessionDispatcherOpts) (*runtime.Dispatcher, error)

	// InitCoordinatorFunc stands for cli.initCoordinator (orchestration_state.go).
	InitCoordinatorFunc func(d *runtime.Dispatcher, cfg config.SubagentConfig, repos ...ledger.LedgerRepository) *coordinator.Coordinator

	// InjectBaselineMessagingFunc stands for cli.injectBaselineMessaging
	// (messaging_tools.go).
	InjectBaselineMessagingFunc func(full, scoped *tools.Registry, cfg config.SubagentConfig, disallowed map[string]struct{})

	// ErrStackAwaitsGrant is the durable grant-pause sentinel returned by the
	// drive loop (stack_grant_pause.go); kept as a var so tests can swap it.
	ErrStackAwaitsGrant error = errStackAwaitsGrant

	// StackingDriveAllowPublishFunc overrides stackingDriveAllowPublish.
	StackingDriveAllowPublishFunc = stackingDriveAllowPublish

	// ClassifyStackPlanRunDeliveryFn overrides classifyStackPlanRunDelivery.
	ClassifyStackPlanRunDeliveryFn = classifyStackPlanRunDelivery

	// StackPlanRunFailureReasonFn overrides stackPlanRunFailureReason.
	StackPlanRunFailureReasonFn = stackPlanRunFailureReason

	// ErrFailedStackPlanRunFunc overrides errFailedStackPlanRun.
	ErrFailedStackPlanRunFunc = errFailedStackPlanRun

	// ErrUndrivenStackPlanRunFunc overrides errUndrivenStackPlanRun.
	ErrUndrivenStackPlanRunFunc = errUndrivenStackPlanRun

	// LoadStackPlanOutputFunc overrides loadStackPlanOutput.
	LoadStackPlanOutputFunc = loadStackPlanOutput

	// ParseStackPlanOutputFunc overrides parseStackPlanOutput.
	ParseStackPlanOutputFunc = parseStackPlanOutput

	// StackPlanInputsFunc overrides stackPlanInputs.
	StackPlanInputsFunc = stackPlanInputs

	// LoadAllStackChunksForDriveFunc overrides loadAllStackChunksForDrive.
	LoadAllStackChunksForDriveFunc = loadAllStackChunksForDrive

	// SeedStackLedgerFunc overrides seedStackLedger.
	SeedStackLedgerFunc = seedStackLedger

	// LoadAllStackChunksFunc overrides loadAllStackChunks.
	LoadAllStackChunksFunc = loadAllStackChunks

	// StackTaskMapFunc overrides stackTaskMap.
	StackTaskMapFunc = stackTaskMap

	// StackMergedSetFunc overrides stackMergedSet.
	StackMergedSetFunc = stackMergedSet

	// AllChunksMergedFunc overrides allChunksMerged.
	AllChunksMergedFunc = allChunksMerged

	// StackRunRefFunc overrides stackRunRef.
	StackRunRefFunc = StackRunRef

	// StackHeadBranchFunc overrides stackHeadBranch.
	StackHeadBranchFunc = stackHeadBranch

	// StackRunHeadCommitFunc overrides stackRunHeadCommit.
	StackRunHeadCommitFunc = stackRunHeadCommit

	// StackRunPushedFunc overrides stackRunPushed.
	StackRunPushedFunc = stackRunPushed

	// StackRunPublishWithheldFunc overrides stackRunPublishWithheld.
	StackRunPublishWithheldFunc = stackRunPublishWithheld

	// StackDecomposedChunksFunc overrides stackDecomposedChunks.
	StackDecomposedChunksFunc = stackDecomposedChunks

	// GitMergeCheckFunc overrides gitMergeCheck.
	GitMergeCheckFunc = gitMergeCheck
)

// toolPostMessage is the post_message tool name used by the authority
// validation. It mirrors the cli const of the same value.
const toolPostMessage = "post_message"

// Build and resume test seams whose defaults come from cli-owned helpers;
// InitCLIDefaults fills them from the seams above.
var (
	WorkflowBuildLoadSkills    func(wsRoot string) (*skills.Registry, error)
	WorkflowBuildDispatcher    func(opts cliagents.SessionDispatcherOpts) (*runtime.Dispatcher, error)
	WorkflowResumeInstallHooks func(workspaceRoot string, staleBypass, quiet bool) (func(), error)
	WorkflowExecutionHooks     func(workspaceRoot string, staleBypass, quiet bool) (func(), error)
)

// InitCLIDefaults installs the cli-backed defaults into the package's build
// and lifecycle seams. internal/cli's wiring init calls it after assigning
// the seam vars; test mains call it after wiring stubs so a stub wins.
func InitCLIDefaults() {
	if WorkflowBuildLoadSkills == nil {
		WorkflowBuildLoadSkills = LoadChatSkillsFunc
	}
	if WorkflowBuildDispatcher == nil {
		WorkflowBuildDispatcher = NewSessionDispatcherFunc
	}
	if WorkflowResumeInstallHooks == nil {
		WorkflowResumeInstallHooks = InstallHookSessionFunc
	}
	if WorkflowExecutionHooks == nil {
		WorkflowExecutionHooks = InstallHookSessionFunc
	}
}

// LoadAgentDefinitionsLocal loads agent definitions under the user gate. It
// mirrors cli's test helper of the same name (test_helpers_moved_test.go);
// duplicated here because Go forbids cross-package _test.go sharing.
func LoadAgentDefinitionsLocal(workspaceRoot, agentFlag string, skillReg *skills.Registry) (cliagents.AgentLoadResult, error) {
	return cliagents.LoadAgentDefinitions(workspaceRoot, agentFlag, skillReg)
}

// SettleStackPlanRunIfCompleteFn overrides settleStackPlanRunIfComplete
// (stack_drive.go): the drive loop's completion settle.
var SettleStackPlanRunIfCompleteFn = settleStackPlanRunIfComplete
