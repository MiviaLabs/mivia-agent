package clichat

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
)

// wiring_exports.go exposes the helpers that internal/cli wires into the
// cliagents, cliworkflow, cliworktree, and cliorchestrate seam vars. They
// are thin aliases over the unexported definitions in this package.

// AdvertisedSessionToolSpecs is the exported alias for the advertisedSessionToolSpecs function, for seam wiring.
var AdvertisedSessionToolSpecs = advertisedSessionToolSpecs

// AllChunksMerged is the exported alias for the allChunksMerged function, for seam wiring.
var AllChunksMerged = allChunksMerged

// BuiltInSlashCommands is the exported alias for the builtInSlashCommands function, for seam wiring.
var BuiltInSlashCommands = builtInSlashCommands

// ClassifyStackPlanRunDelivery is the exported alias for the classifyStackPlanRunDelivery function, for seam wiring.
var ClassifyStackPlanRunDelivery = classifyStackPlanRunDelivery

// DriveStackToCompletion is the exported alias for the driveStackToCompletion function, for seam wiring.
var DriveStackToCompletion = driveStackToCompletion

// ErrFailedStackPlanRun is the exported alias for the errFailedStackPlanRun function, for seam wiring.
var ErrFailedStackPlanRun = errFailedStackPlanRun

// ErrStackAwaitsGrant is the exported alias for the errStackAwaitsGrant function, for seam wiring.
var ErrStackAwaitsGrant = errStackAwaitsGrant

// ErrUndrivenStackPlanRun is the exported alias for the errUndrivenStackPlanRun function, for seam wiring.
var ErrUndrivenStackPlanRun = errUndrivenStackPlanRun

// GitMergeChecker is the exported alias for the gitMergeChecker type, for seam wiring.
type GitMergeChecker = gitMergeChecker

// InjectBaselineMessaging is the exported alias for the injectBaselineMessaging function, for seam wiring.
var InjectBaselineMessaging = injectBaselineMessaging

// LoadAllStackChunks is the exported alias for the loadAllStackChunks function, for seam wiring.
var LoadAllStackChunks = loadAllStackChunks

// LoadAllStackChunksForDrive is the exported alias for the loadAllStackChunksForDrive function, for seam wiring.
var LoadAllStackChunksForDrive = loadAllStackChunksForDrive

// LoadStackPlanOutput is the exported alias for the loadStackPlanOutput function, for seam wiring.
var LoadStackPlanOutput = loadStackPlanOutput

// LogMCPWarnings is the exported alias for the logMCPWarnings function, for seam wiring.
var LogMCPWarnings = logMCPWarnings

// MessagingDisallowed is the exported alias for the messagingDisallowed function, for seam wiring.
var MessagingDisallowed = messagingDisallowed

// ParseStackPlanOutput is the exported alias for the parseStackPlanOutput function, for seam wiring.
var ParseStackPlanOutput = parseStackPlanOutput

// RegisterSessionTool is the exported alias for the registerSessionTool function, for seam wiring.
var RegisterSessionTool = registerSessionTool

// SeedStackLedger is the exported alias for the seedStackLedger function, for seam wiring.
var SeedStackLedger = seedStackLedger

// SessionAutoDeliveryRepairLoop is the exported alias for the sessionAutoDeliveryRepairLoop function, for seam wiring.
var SessionAutoDeliveryRepairLoop = sessionAutoDeliveryRepairLoop

// SettleStackPlanRunIfComplete is the exported alias for the settleStackPlanRunIfComplete function, for seam wiring.
var SettleStackPlanRunIfComplete = settleStackPlanRunIfComplete

// StackDecomposedChunks is the exported alias for the stackDecomposedChunks function, for seam wiring.
var StackDecomposedChunks = stackDecomposedChunks

// StackHeadBranch is the exported alias for the stackHeadBranch function, for seam wiring.
var StackHeadBranch = stackHeadBranch

// StackMergedSet is the exported alias for the stackMergedSet function, for seam wiring.
var StackMergedSet = stackMergedSet

// StackPlanInputs is the exported alias for the stackPlanInputs function, for seam wiring.
var StackPlanInputs = stackPlanInputs

// StackPlanRunFailureReason is the exported alias for the stackPlanRunFailureReason function, for seam wiring.
var StackPlanRunFailureReason = stackPlanRunFailureReason

// StackRunHeadCommit is the exported alias for the stackRunHeadCommit function, for seam wiring.
var StackRunHeadCommit = stackRunHeadCommit

// StackRunPublishWithheld is the exported alias for the stackRunPublishWithheld function, for seam wiring.
var StackRunPublishWithheld = stackRunPublishWithheld

// StackRunPushed is the exported alias for the stackRunPushed function, for seam wiring.
var StackRunPushed = stackRunPushed

// StackRunRef is the exported alias for the stackRunRef function, for seam wiring.
var StackRunRef = stackRunRef

// StackTaskMap is the exported alias for the stackTaskMap function, for seam wiring.
var StackTaskMap = stackTaskMap

// StackingDriveAllowPublish is the exported alias for the stackingDriveAllowPublish function, for seam wiring.
var StackingDriveAllowPublish = stackingDriveAllowPublish

// SummaryWiring is the exported alias for the summaryWiring function, for seam wiring.
var SummaryWiring = summaryWiring

// GitMergeCheck wraps the gitMergeChecker merge oracle for cross-package
// seam wiring.
func GitMergeCheck(ctx context.Context, git delivery.GitRunner, pr delivery.PRClient, gc delivery.GitContext, headBranch, baseBranch, headCommit, repoSlug string, wasPushed bool) (bool, error) {
	return gitMergeChecker{git: git, pr: pr, gc: gc}.Merged(ctx, headBranch, baseBranch, headCommit, repoSlug, wasPushed)
}

// RunChat is the exported alias for the runChat command entry point.
var RunChat = runChat

// RunSessions is the exported alias for the runSessions command entry point.
var RunSessions = runSessions

// RunCompact is the exported alias for the runCompact command entry point.
var RunCompact = runCompact

// ChatWorkspaceRoot is the exported alias for chatWorkspaceRoot.
var ChatWorkspaceRoot = chatWorkspaceRoot

// RunStackDrive is the exported alias for the runStackDrive entry point.
var RunStackDrive = runStackDrive

// StackScope is the exported alias for stackScope.
var StackScope = stackScope

// StackGrantHintLines is the exported alias for stackGrantHintLines.
var StackGrantHintLines = stackGrantHintLines

// StackRunRefExport is the exported alias for stackRunRef, for
// internal/cli/stack_command.go. The StackRunRef name is taken by the
// cliworkflow wiring alias.
var StackRunRefExport = stackRunRef

// StackPRNumber is the exported alias for stackPRNumber.
var StackPRNumber = stackPRNumber

// RunConfiguredChat is the exported alias for the runConfiguredChat entry
// point, for the cli characterization test shim.
var RunConfiguredChat = runConfiguredChat

// RunChatCharacterization drives runConfiguredChat with the four invocation
// fields the cli characterization suite sets.
func RunChatCharacterization(workspacePath string, jsonMode, plainUI, quiet bool, res *config.Resolved) error {
	return runConfiguredChat(chatInvocation{workspacePath: workspacePath, jsonMode: jsonMode, plainUI: plainUI, quiet: quiet}, res)
}

// ChatFlags is the exported alias for the chatFlags flag parser.
var ChatFlags = chatFlags

// HandleSlashCommand is the exported alias for the handleSlash dispatcher.
var HandleSlashCommand = handleSlash

// SlashSurfaceBoth is the exported alias for the slashSurfaceBoth surface.
var SlashSurfaceBoth = slashSurfaceBoth
