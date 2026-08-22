// Package cliworkflow holds the workflow CLI domain: workflow run, resume,
// deliver, status, events, approve, reject, cancel, cleanup, delete, and gc
// commands, the session workflow tool engine, and the workflow snapshot and
// verifier pinning machinery.
//
// The package must never import internal/cli (the CLI composition root
// imports this package). Every symbol this package needs from internal/cli is
// declared below as a nil package variable and assigned by the init() in
// internal/cli/cliworkflow_wiring.go. Each seam documents the cli helper it
// stands for and the cycle it breaks. The real fix is to move the helper into
// a domain package both sides can import (the stack helpers belong in a
// future internal/clistack; the chat/config helpers belong in the packages
// that own them).
package cliworkflow

import (
	"context"
	"fmt"
	"io"

	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// StackPlanRunGate classifies a stacking plan run's delivery gate. It mirrors
// internal/cli's unexported stackPlanRunGate; the wiring converts the cli
// value with StackPlanRunGate(int(...)).
type StackPlanRunGate int

// Stack plan delivery gate values. The order mirrors internal/cli's
// stackPlanRunGate iota so the int conversion in the wiring is exact.
const (
	stackPlanRunNotApplicable StackPlanRunGate = iota
	stackPlanRunIncomplete
	stackPlanRunComplete
	stackPlanRunFailed
)

// Seams over internal/cli helpers. All are nil until
// internal/cli/cliworkflow_wiring.go assigns them.

var (
	// ContextStorePath stands for cli.ContextStorePath (context_setup.go).
	ContextStorePath func(root string, cfg config.SubagentConfig) string

	// ApplyPrivacyPolicyFunc stands for cli.applyPrivacyPolicy (chat_command.go).
	ApplyPrivacyPolicyFunc func(res *config.Resolved)

	// LogMCPWarningsFunc stands for cli.logMCPWarnings (limits_summary.go).
	LogMCPWarningsFunc func(w io.Writer, res *config.Resolved)

	// SliceErrorsFunc stands for cli.sliceErrors (errors.go).
	SliceErrorsFunc func(context string, errs []string) error

	// FlagValueFunc stands for cli.flagValue (root.go).
	FlagValueFunc func(args []string, names ...string) (string, []string, bool, error)

	// FlagVarFunc stands for cli.flagVar (root.go).
	FlagVarFunc func(args []string, names ...string) ([]string, []string, bool, error)

	// InstallHookSessionFunc stands for cli.installHookSession (hooks_command.go).
	InstallHookSessionFunc func(workspaceRoot string, staleBypass, quiet bool) (func(), error)

	// LoadChatSkillsFunc stands for cli.loadChatSkills (chat_command.go).
	LoadChatSkillsFunc func(wsRoot string) (*skills.Registry, error)

	// NewSessionDispatcherFunc stands for cli.NewSessionDispatcher (dispatcher.go).
	NewSessionDispatcherFunc func(opts cliagents.SessionDispatcherOpts) (*runtime.Dispatcher, error)

	// InitCoordinatorFunc stands for cli.initCoordinator (orchestration_state.go).
	InitCoordinatorFunc func(d *runtime.Dispatcher, cfg config.SubagentConfig, repos ...ledger.LedgerRepository) coordinator.Coordinator

	// InjectBaselineMessagingFunc stands for cli.injectBaselineMessaging
	// (messaging_tools.go).
	InjectBaselineMessagingFunc func(full, scoped *tools.Registry, cfg config.SubagentConfig, disallowed map[string]struct{})

	// MessagingDisallowedFunc stands for cli.messagingDisallowed
	// (agent_task_handler.go).
	MessagingDisallowedFunc func(names []string) map[string]struct{}

	// SessionAutoDeliveryRepairLoopFunc stands for
	// cli.sessionAutoDeliveryRepairLoop (session_delivery_repair.go).
	SessionAutoDeliveryRepairLoopFunc func(runCtx context.Context, repo workflowledger.Repository, root string, res *config.Resolved, store *storage.SQLite, runID string, advance func(context.Context) (workflowledger.RunSnapshot, error), driveStack func(context.Context) (bool, error), deliverPlanRun bool)

	// ErrStackAwaitsGrant stands for cli.errStackAwaitsGrant
	// (stack_grant_pause.go).
	ErrStackAwaitsGrant error

	// StackingDriveAllowPublishFunc stands for cli.stackingDriveAllowPublish
	// (stack_grant_pause.go).
	StackingDriveAllowPublishFunc func(compiled *definition.CompiledWorkflow) bool

	// ClassifyStackPlanRunDeliveryFunc stands for
	// cli.classifyStackPlanRunDelivery (stack_admit_integration.go).
	ClassifyStackPlanRunDeliveryFunc func(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string, remoteMergeOracle bool) StackPlanRunGate

	// ClassifyStackPlanRunDeliveryFn is the test seam over
	// ClassifyStackPlanRunDeliveryFunc (mirrors the cli var of the same name).
	ClassifyStackPlanRunDeliveryFn func(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string, remoteMergeOracle bool) StackPlanRunGate

	// StackPlanRunFailureReasonFunc stands for cli.stackPlanRunFailureReason
	// (stack_admit_integration.go).
	StackPlanRunFailureReasonFunc func(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string) (failed bool, reason string)

	// StackPlanRunFailureReasonFn is the test seam over
	// StackPlanRunFailureReasonFunc (mirrors the cli var of the same name).
	StackPlanRunFailureReasonFn func(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string) (failed bool, reason string)

	// ErrFailedStackPlanRunFunc stands for cli.errFailedStackPlanRun
	// (stack_admit_integration.go).
	ErrFailedStackPlanRunFunc func(runID, reason string) error

	// ErrUndrivenStackPlanRunFunc stands for cli.errUndrivenStackPlanRun
	// (stack_admit_integration.go).
	ErrUndrivenStackPlanRunFunc func(runID string) error

	// LoadStackPlanOutputFunc stands for cli.loadStackPlanOutput (stack_state.go).
	LoadStackPlanOutputFunc func(repo workflowledger.Repository, stackID string) ([]byte, error)

	// ParseStackPlanOutputFunc stands for cli.parseStackPlanOutput
	// (stack_reconcile.go).
	ParseStackPlanOutputFunc func(raw []byte) (mode string, chunks []delivery.ChunkPlan, hasMore bool, remainingScope string, err error)

	// StackPlanInputsFunc stands for cli.stackPlanInputs (stack_state.go).
	StackPlanInputsFunc func(repo workflowledger.Repository, stackID string) (map[string]string, error)

	// LoadAllStackChunksForDriveFunc stands for
	// cli.loadAllStackChunksForDrive (stack_decompose_continue.go).
	LoadAllStackChunksForDriveFunc func(prepared *PreparedWorkflowRun, stackID string, planOutput []byte, planInputs map[string]string, stdout, stderr io.Writer) (chunks []delivery.ChunkPlan, hasMore bool, hasUnsettledWave bool, remainingScope string, err error)

	// SeedStackLedgerFunc stands for cli.seedStackLedger (stack_state.go).
	SeedStackLedgerFunc func(ledger *workflowledger.Store, stackID string, chunks []delivery.ChunkPlan) error

	// DriveStackToCompletionFunc stands for cli.driveStackToCompletion
	// (stack_drive.go).
	DriveStackToCompletionFunc func(ctx context.Context, prepared *PreparedWorkflowRun, ledger *workflowledger.Store, stackID string, chunks []delivery.ChunkPlan, hasMore bool, hasUnsettledWave bool, remainingScope string, planInputs map[string]string, allowPublish bool, stdout, stderr io.Writer) error

	// LoadAllStackChunksFunc stands for cli.loadAllStackChunks (stack_state.go).
	LoadAllStackChunksFunc func(repo workflowledger.Repository, stackID string) (chunks []delivery.ChunkPlan, hasMore bool, remainingScope string, err error)

	// StackTaskMapFunc stands for cli.stackTaskMap (stack_state.go).
	StackTaskMapFunc func(ledger *workflowledger.Store, stackID string) (map[string]workflowledger.Task, error)

	// StackMergedSetFunc stands for cli.stackMergedSet (stack_state.go).
	StackMergedSetFunc func(byID map[string]workflowledger.Task) map[string]bool

	// AllChunksMergedFunc stands for cli.allChunksMerged (stack_state.go).
	AllChunksMergedFunc func(chunks []delivery.ChunkPlan, merged map[string]bool) bool

	// StackRunRefFunc stands for cli.stackRunRef (stack_reconcile.go).
	StackRunRefFunc func(repo workflowledger.Repository, stackID, chunkID string) (workflowledger.RunSnapshot, bool, error)

	// StackHeadBranchFunc stands for cli.stackHeadBranch (stack_reconcile.go).
	StackHeadBranchFunc func(run workflowledger.RunSnapshot) string

	// StackRunHeadCommitFunc stands for cli.stackRunHeadCommit (stack_state.go).
	StackRunHeadCommitFunc func(repo workflowledger.Repository, run workflowledger.RunSnapshot) string

	// StackRunPushedFunc stands for cli.stackRunPushed (stack_state.go).
	StackRunPushedFunc func(repo workflowledger.Repository, run workflowledger.RunSnapshot) bool

	// StackRunPublishWithheldFunc stands for cli.stackRunPublishWithheld
	// (stack_publish_gate.go).
	StackRunPublishWithheldFunc func(ctx context.Context, repo workflowledger.Repository, runID string, quiet bool) bool

	// StackDecomposedChunksFunc stands for cli.stackDecomposedChunks
	// (stack_admit_integration.go).
	StackDecomposedChunksFunc func(ctx context.Context, repo workflowledger.Repository, runID string) (chunks int, ok bool)

	// OpenContextStoreFunc stands for cli.openContextStore (context_setup.go).
	OpenContextStoreFunc func(root string, cfg config.SubagentConfig) (*storage.SQLite, error)

	// InjectSkillResourceToolFunc stands for cli.InjectSkillResourceTool
	// (skill_resource_tool.go).
	InjectSkillResourceToolFunc func(registry *tools.Registry, activation *skills.SkillActivation) (*tools.Registry, error)

	// GitMergeCheckFunc stands for cli.gitMergeChecker{}.Merged
	// (stack_merge_checker.go).
	GitMergeCheckFunc func(ctx context.Context, git delivery.GitRunner, pr delivery.PRClient, gc delivery.GitContext, headBranch, baseBranch, headCommit, repoSlug string, wasPushed bool) (bool, error)
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
	if WorkflowStackDriveToCompletion == nil {
		WorkflowStackDriveToCompletion = DriveStackToCompletionFunc
	}
	if WorkflowResumeInstallHooks == nil {
		WorkflowResumeInstallHooks = InstallHookSessionFunc
	}
	if WorkflowExecutionHooks == nil {
		WorkflowExecutionHooks = InstallHookSessionFunc
	}
	if ClassifyStackPlanRunDeliveryFn == nil {
		ClassifyStackPlanRunDeliveryFn = ClassifyStackPlanRunDeliveryFunc
	}
	if StackPlanRunFailureReasonFn == nil {
		StackPlanRunFailureReasonFn = StackPlanRunFailureReasonFunc
	}
}

// OpenContextStorePath opens the SQLite context store at path. It mirrors
// cli.openContextStorePath (context_setup.go); both wrap storage.OpenSQLite.
func OpenContextStorePath(path string) (*storage.SQLite, error) {
	store, err := storage.OpenSQLite(path)
	if err != nil {
		return nil, fmt.Errorf("open context store %q: %w", path, err)
	}
	return store, nil
}

// LoadAgentDefinitionsLocal loads agent definitions under the user gate. It
// mirrors cli's test helper of the same name (test_helpers_moved_test.go);
// duplicated here because Go forbids cross-package _test.go sharing.
func LoadAgentDefinitionsLocal(workspaceRoot, agentFlag string, skillReg *skills.Registry) (cliagents.AgentLoadResult, error) {
	return cliagents.LoadAgentDefinitions(workspaceRoot, agentFlag, skillReg)
}

// SettleStackPlanRunIfCompleteFn stands for cli.settleStackPlanRunIfComplete
// (stack_drive.go): the drive loop's completion settle.
var SettleStackPlanRunIfCompleteFn func(ctx context.Context, prepared *PreparedWorkflowRun, stackID string, stdout io.Writer) error
