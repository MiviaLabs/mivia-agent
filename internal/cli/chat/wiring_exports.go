package chat

import (
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// wiring_exports.go exposes the helpers that internal/cli wires into the
// cliagents, cliworktree, and cliorchestrate seam vars. They are thin
// aliases over the unexported definitions in this package. The workflow
// stack helpers moved to internal/cli/workflow and no longer need aliases.

// AdvertisedSessionToolSpecs is the exported alias for the advertisedSessionToolSpecs function, for seam wiring.
var AdvertisedSessionToolSpecs = advertisedSessionToolSpecs

// BuiltInSlashCommands is the exported alias for the builtInSlashCommands function, for seam wiring.
var BuiltInSlashCommands = builtInSlashCommands

// InjectBaselineMessaging is the exported alias for the injectBaselineMessaging function, for seam wiring.
var InjectBaselineMessaging = injectBaselineMessaging

// SummaryWiring is the exported alias for the summaryWiring function, for seam wiring.
var SummaryWiring = summaryWiring

// RunChat is the exported alias for the runChat command entry point.
var RunChat = runChat

// RunSessions is the exported alias for the runSessions command entry point.
var RunSessions = runSessions

// RunCompact is the exported alias for the runCompact command entry point.
var RunCompact = runCompact

// ChatWorkspaceRoot is the exported alias for chatWorkspaceRoot.
var ChatWorkspaceRoot = chatWorkspaceRoot

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
