package cli

// cliworkflow_wiring.go breaks the cli <-> cliworkflow import cycle.
// internal/cliworkflow owns the workflow domain but needs helpers that still
// live in internal/cli (stack drivers, privacy and hook plumbing, the context
// store path, skill loading). Each var below assigns the cliworkflow seam
// declared in internal/cliworkflow/seams.go. The real fix is to move the
// stack helpers into a future internal/clistack package both sides import,
// and to lift the chat/config helpers into the packages that own them.

import (
	"context"
	clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"

	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func init() {
	cliworkflow.ContextStorePath = clichat.ContextStorePath
	cliworkflow.ApplyPrivacyPolicyFunc = clichat.ApplyPrivacyPolicy
	cliworkflow.LogMCPWarningsFunc = clichat.LogMCPWarnings
	cliworkflow.SliceErrorsFunc = sliceErrors
	cliworkflow.FlagValueFunc = flagValue
	cliworkflow.FlagVarFunc = flagVar
	cliworkflow.InstallHookSessionFunc = installHookSession
	cliworkflow.LoadChatSkillsFunc = clichat.LoadChatSkills
	cliworkflow.NewSessionDispatcherFunc = clichat.NewSessionDispatcher
	cliworkflow.InitCoordinatorFunc = cliorchestrate.InitCoordinator
	cliworkflow.InjectBaselineMessagingFunc = clichat.InjectBaselineMessaging
	cliworkflow.MessagingDisallowedFunc = clichat.MessagingDisallowed
	cliworkflow.SessionAutoDeliveryRepairLoopFunc = clichat.SessionAutoDeliveryRepairLoop
	cliworkflow.ErrStackAwaitsGrant = clichat.ErrStackAwaitsGrant
	cliworkflow.StackingDriveAllowPublishFunc = clichat.StackingDriveAllowPublish
	cliworkflow.ClassifyStackPlanRunDeliveryFunc = func(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string, oracle bool) cliworkflow.StackPlanRunGate {
		return cliworkflow.StackPlanRunGate(int(clichat.ClassifyStackPlanRunDelivery(ctx, root, store, repo, runID, oracle)))
	}
	cliworkflow.StackPlanRunFailureReasonFunc = clichat.StackPlanRunFailureReason
	cliworkflow.ErrFailedStackPlanRunFunc = clichat.ErrFailedStackPlanRun
	cliworkflow.ErrUndrivenStackPlanRunFunc = clichat.ErrUndrivenStackPlanRun
	cliworkflow.LoadStackPlanOutputFunc = clichat.LoadStackPlanOutput
	cliworkflow.ParseStackPlanOutputFunc = clichat.ParseStackPlanOutput
	cliworkflow.StackPlanInputsFunc = clichat.StackPlanInputs
	cliworkflow.LoadAllStackChunksForDriveFunc = clichat.LoadAllStackChunksForDrive
	cliworkflow.SeedStackLedgerFunc = clichat.SeedStackLedger
	cliworkflow.DriveStackToCompletionFunc = clichat.DriveStackToCompletion
	cliworkflow.LoadAllStackChunksFunc = clichat.LoadAllStackChunks
	cliworkflow.StackTaskMapFunc = clichat.StackTaskMap
	cliworkflow.StackMergedSetFunc = clichat.StackMergedSet
	cliworkflow.AllChunksMergedFunc = clichat.AllChunksMerged
	cliworkflow.StackRunRefFunc = clichat.StackRunRef
	cliworkflow.StackHeadBranchFunc = clichat.StackHeadBranch
	cliworkflow.StackRunHeadCommitFunc = clichat.StackRunHeadCommit
	cliworkflow.StackRunPushedFunc = clichat.StackRunPushed
	cliworkflow.StackRunPublishWithheldFunc = clichat.StackRunPublishWithheld
	cliworkflow.StackDecomposedChunksFunc = clichat.StackDecomposedChunks
	cliworkflow.OpenContextStoreFunc = clichat.OpenContextStore
	cliworkflow.InjectSkillResourceToolFunc = clichat.InjectSkillResourceTool
	cliworkflow.GitMergeCheckFunc = clichat.GitMergeCheck
	cliworkflow.SettleStackPlanRunIfCompleteFn = clichat.SettleStackPlanRunIfComplete
	cliworkflow.InitCLIDefaults()
}
