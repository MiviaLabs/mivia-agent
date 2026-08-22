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

	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func init() {
	cliworkflow.ContextStorePath = ContextStorePath
	cliworkflow.ApplyPrivacyPolicyFunc = applyPrivacyPolicy
	cliworkflow.LogMCPWarningsFunc = logMCPWarnings
	cliworkflow.SliceErrorsFunc = sliceErrors
	cliworkflow.FlagValueFunc = flagValue
	cliworkflow.FlagVarFunc = flagVar
	cliworkflow.InstallHookSessionFunc = installHookSession
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
	cliworkflow.GitMergeCheckFunc = func(ctx context.Context, git delivery.GitRunner, pr delivery.PRClient, gc delivery.GitContext, headBranch, baseBranch, headCommit, repoSlug string, wasPushed bool) (bool, error) {
		return gitMergeChecker{git: git, pr: pr, gc: gc}.Merged(ctx, headBranch, baseBranch, headCommit, repoSlug, wasPushed)
	}
	cliworkflow.SettleStackPlanRunIfCompleteFn = settleStackPlanRunIfComplete
	cliworkflow.InitCLIDefaults()
}
