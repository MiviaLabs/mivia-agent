package cli

// cliworkflow_wiring.go breaks the cli <-> cliworkflow import cycle.
// internal/cli/workflow owns the workflow domain (including the stack
// driver, moved here from internal/cli/chat). The remaining seams cover
// helpers still owned by internal/cli/chat and the cli root; the long-term
// fix is to lift those into the packages that own them.

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli/chat"

	"github.com/MiviaLabs/mivia-agent/internal/cli/orchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/cli/workflow"
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
	workflow.OpenContextStoreFunc = chat.OpenContextStore
	workflow.InjectSkillResourceToolFunc = chat.InjectSkillResourceTool
	workflow.InitCLIDefaults()
}
