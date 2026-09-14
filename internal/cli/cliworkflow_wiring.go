package cli

// cliworkflow_wiring.go breaks the cli <-> cliworkflow import cycle.
// internal/cli/workflow owns the workflow domain but needs helpers that still
// live in internal/cli (stack drivers, privacy and hook plumbing, the context
// store path, skill loading). Each var below assigns the cliworkflow seam
// declared in internal/cli/workflow/seams.go. The real fix is to move the
// stack helpers into a future internal/clistack package both sides import,
// and to lift the chat/config helpers into the packages that own them.

import (
	"context"
	"github.com/MiviaLabs/mivia-agent/internal/cli/chat"

	"github.com/MiviaLabs/mivia-agent/internal/cli/orchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/cli/workflow"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func init() {
	workflow.ContextStorePath = chat.ContextStorePath
	workflow.ApplyPrivacyPolicyFunc = chat.ApplyPrivacyPolicy
	workflow.LogMCPWarningsFunc = chat.LogMCPWarnings
	workflow.SliceErrorsFunc = sliceErrors
	workflow.FlagValueFunc = flagValue
	workflow.FlagVarFunc = flagVar
	workflow.InstallHookSessionFunc = installHookSession
	workflow.LoadChatSkillsFunc = chat.LoadChatSkills
	workflow.NewSessionDispatcherFunc = chat.NewSessionDispatcher
	workflow.InitCoordinatorFunc = orchestrate.InitCoordinator
	workflow.InjectBaselineMessagingFunc = chat.InjectBaselineMessaging
	workflow.MessagingDisallowedFunc = chat.MessagingDisallowed
	workflow.SessionAutoDeliveryRepairLoopFunc = chat.SessionAutoDeliveryRepairLoop
	workflow.ErrStackAwaitsGrant = chat.ErrStackAwaitsGrant
	workflow.StackingDriveAllowPublishFunc = chat.StackingDriveAllowPublish
	workflow.ClassifyStackPlanRunDeliveryFunc = func(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string, oracle bool) workflow.StackPlanRunGate {
		return workflow.StackPlanRunGate(int(chat.ClassifyStackPlanRunDelivery(ctx, root, store, repo, runID, oracle)))
	}
	workflow.StackPlanRunFailureReasonFunc = chat.StackPlanRunFailureReason
	workflow.ErrFailedStackPlanRunFunc = chat.ErrFailedStackPlanRun
	workflow.ErrUndrivenStackPlanRunFunc = chat.ErrUndrivenStackPlanRun
	workflow.LoadStackPlanOutputFunc = chat.LoadStackPlanOutput
	workflow.ParseStackPlanOutputFunc = chat.ParseStackPlanOutput
	workflow.StackPlanInputsFunc = chat.StackPlanInputs
	workflow.LoadAllStackChunksForDriveFunc = chat.LoadAllStackChunksForDrive
	workflow.SeedStackLedgerFunc = chat.SeedStackLedger
	workflow.DriveStackToCompletionFunc = chat.DriveStackToCompletion
	workflow.LoadAllStackChunksFunc = chat.LoadAllStackChunks
	workflow.StackTaskMapFunc = chat.StackTaskMap
	workflow.StackMergedSetFunc = chat.StackMergedSet
	workflow.AllChunksMergedFunc = chat.AllChunksMerged
	workflow.StackRunRefFunc = chat.StackRunRef
	workflow.StackHeadBranchFunc = chat.StackHeadBranch
	workflow.StackRunHeadCommitFunc = chat.StackRunHeadCommit
	workflow.StackRunPushedFunc = chat.StackRunPushed
	workflow.StackRunPublishWithheldFunc = chat.StackRunPublishWithheld
	workflow.StackDecomposedChunksFunc = chat.StackDecomposedChunks
	workflow.OpenContextStoreFunc = chat.OpenContextStore
	workflow.InjectSkillResourceToolFunc = chat.InjectSkillResourceTool
	workflow.GitMergeCheckFunc = chat.GitMergeCheck
	workflow.SettleStackPlanRunIfCompleteFn = chat.SettleStackPlanRunIfComplete
	workflow.InitCLIDefaults()
}
