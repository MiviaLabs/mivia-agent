package cli

// clichat_aliases.go re-exports symbols that moved to internal/cli/chat so
// staying consumers (internal/legacytui) compile without per-file import
// updates. Use the clichat-qualified form in new code. These aliases are
// intentional shims while the extraction stabilises.

import clichat "github.com/MiviaLabs/mivia-agent/internal/cli/chat"

// BoundedToolText re-exports the clichat.BoundedToolText function.
var BoundedToolText = clichat.BoundedToolText

// ClassicAgentStatePtr re-exports the clichat.ClassicAgentStatePtr variable.
var ClassicAgentStatePtr = clichat.ClassicAgentStatePtr

// ClearSubagentProgress re-exports the clichat.ClearSubagentProgress function.
var ClearSubagentProgress = clichat.ClearSubagentProgress

// CompactStructuralOnlyNotice re-exports the clichat.CompactStructuralOnlyNotice function.
var CompactStructuralOnlyNotice = clichat.CompactStructuralOnlyNotice

// ContextWorkspaceID re-exports the clichat.ContextWorkspaceID function.
var ContextWorkspaceID = clichat.ContextWorkspaceID

// EffortBusyNotice re-exports the clichat.EffortBusyNotice constant.
const EffortBusyNotice = clichat.EffortBusyNotice

// EffortDiscardedSuffix re-exports the clichat.EffortDiscardedSuffix function.
var EffortDiscardedSuffix = clichat.EffortDiscardedSuffix

// EffortRowName re-exports the clichat.EffortRowName function.
var EffortRowName = clichat.EffortRowName

// EffortUnsetWord re-exports the clichat.EffortUnsetWord constant.
const EffortUnsetWord = clichat.EffortUnsetWord

// EmitSubagentProgress re-exports the clichat.EmitSubagentProgress function.
var EmitSubagentProgress = clichat.EmitSubagentProgress

// EventPreview re-exports the clichat.EventPreview function.
var EventPreview = clichat.EventPreview

// FindSlashCommand re-exports the clichat.FindSlashCommand function.
var FindSlashCommand = clichat.FindSlashCommand

// HandleSlash re-exports the clichat.HandleSlash function.
var HandleSlash = clichat.HandleSlash

// HandleSlashAgent re-exports the clichat.HandleSlashAgent function.
var HandleSlashAgent = clichat.HandleSlashAgent

// HandleSlashEffort re-exports the clichat.HandleSlashEffort function.
var HandleSlashEffort = clichat.HandleSlashEffort

// HandleSlashInfo re-exports the clichat.HandleSlashInfo function.
var HandleSlashInfo = clichat.HandleSlashInfo

// IsBannerTool re-exports the clichat.IsBannerTool function.
var IsBannerTool = clichat.IsBannerTool

// IsEditTool re-exports the clichat.IsEditTool function.
var IsEditTool = clichat.IsEditTool

// JoinHub re-exports the clichat.JoinHub function.
var JoinHub = clichat.JoinHub

// LatestAutoSaveName re-exports the clichat.LatestAutoSaveName function.
var LatestAutoSaveName = clichat.LatestAutoSaveName

// Max re-exports the clichat.Max function.
var Max = clichat.Max

// NewAgentTaskHandler re-exports the clichat.NewAgentTaskHandler function.
var NewAgentTaskHandler = clichat.NewAgentTaskHandler

// NewTerminal re-exports the clichat.NewTerminal function.
var NewTerminal = clichat.NewTerminal

// NewTestTerminal re-exports the clichat.NewTestTerminal function.
var NewTestTerminal = clichat.NewTestTerminal

// ParseEffortArg re-exports the clichat.ParseEffortArg function.
var ParseEffortArg = clichat.ParseEffortArg

// ParseToolPath re-exports the clichat.ParseToolPath function.
var ParseToolPath = clichat.ParseToolPath

// RegistryForState re-exports the clichat.RegistryForState function.
var RegistryForState = clichat.RegistryForState

// ReplHelpContent re-exports the clichat.ReplHelpContent function.
var ReplHelpContent = clichat.ReplHelpContent

// RestoreREPLRuntime re-exports the clichat.RestoreREPLRuntime function.
var RestoreREPLRuntime = clichat.RestoreREPLRuntime

// RegisterSessionBus re-exports the clichat.RegisterSessionBus function.
var RegisterSessionBus = clichat.RegisterSessionBus

// SetSubagentProgress re-exports the clichat.SetSubagentProgress function.
var SetSubagentProgress = clichat.SetSubagentProgress

// SkillTurnPreamble re-exports the clichat.SkillTurnPreamble constant.
const SkillTurnPreamble = clichat.SkillTurnPreamble

// SlashKindBuiltin re-exports the clichat.SlashKindBuiltin constant.
const SlashKindBuiltin = clichat.SlashKindBuiltin

// SummarizeToolDetail re-exports the clichat.SummarizeToolDetail function.
var SummarizeToolDetail = clichat.SummarizeToolDetail

// ToolWaveCounts re-exports the clichat.ToolWaveCounts function.
var ToolWaveCounts = clichat.ToolWaveCounts

// TruncatePreviewUTF8 re-exports the clichat.TruncatePreviewUTF8 function.
var TruncatePreviewUTF8 = clichat.TruncatePreviewUTF8

// ValidateWorkspaceRestart re-exports the clichat.ValidateWorkspaceRestart function.
var ValidateWorkspaceRestart = clichat.ValidateWorkspaceRestart

// EffortOrchestrationNotice re-exports the clichat.EffortOrchestrationNotice constant.
const EffortOrchestrationNotice = clichat.EffortOrchestrationNotice

// SafeEffortError re-exports the clichat.SafeEffortError function.
var SafeEffortError = clichat.SafeEffortError

// ToolIconForName re-exports the clichat.ToolIconForName function.
var ToolIconForName = clichat.ToolIconForName

// CurrentAgentName re-exports the clichat.CurrentAgentName function.
var CurrentAgentName = clichat.CurrentAgentName

// Min re-exports the clichat.Min function.
var Min = clichat.Min

// TruncateToWidth re-exports the clichat.TruncateToWidth function.
var TruncateToWidth = clichat.TruncateToWidth

// MaxHistorySize re-exports the clichat.MaxHistorySize constant.
const MaxHistorySize = clichat.MaxHistorySize

// SwitchModelCommand re-exports the clichat.SwitchModelCommand function.
var SwitchModelCommand = clichat.SwitchModelCommand

// RuneWidth re-exports the clichat.RuneWidth function.
var RuneWidth = clichat.RuneWidth

// SlashCommands re-exports the clichat.SlashCommands function.
var SlashCommands = clichat.SlashCommands

// SlashKindSkill re-exports the clichat.SlashKindSkill constant.
const SlashKindSkill = clichat.SlashKindSkill

// SummaryDisabledReason re-exports the clichat.SummaryDisabledReason function.
var SummaryDisabledReason = clichat.SummaryDisabledReason

// ActionAgent re-exports the clichat.ActionAgent constant.
const ActionAgent = clichat.ActionAgent

// ActionKindForTool re-exports the clichat.ActionKindForTool function.
var ActionKindForTool = clichat.ActionKindForTool

// ModelSwitchChoices re-exports the clichat.ModelSwitchChoices function.
var ModelSwitchChoices = clichat.ModelSwitchChoices

// ParseModelArgs re-exports the clichat.ParseModelArgs function.
var ParseModelArgs = clichat.ParseModelArgs

// ShouldCommitInterim re-exports the clichat.ShouldCommitInterim function.
var ShouldCommitInterim = clichat.ShouldCommitInterim

// VisibleWidth re-exports the clichat.VisibleWidth function.
var VisibleWidth = clichat.VisibleWidth

// ParseNonNegInt re-exports the clichat.ParseNonNegInt function.
var ParseNonNegInt = clichat.ParseNonNegInt

// CancellationCanReplaceTurnError re-exports the clichat.CancellationCanReplaceTurnError function.
var CancellationCanReplaceTurnError = clichat.CancellationCanReplaceTurnError

// ModelRestoreNoticeText re-exports the clichat.ModelRestoreNoticeText function.
var ModelRestoreNoticeText = clichat.ModelRestoreNoticeText

// OpenRepositoryContextStore re-exports the clichat.OpenRepositoryContextStore function.
var OpenRepositoryContextStore = clichat.OpenRepositoryContextStore

// SlashCommand re-exports the clichat.SlashCommand type.
type SlashCommand = clichat.SlashCommand

// ToolRow re-exports the clichat.ToolRow type.
type ToolRow = clichat.ToolRow

// Terminal re-exports the clichat.Terminal type.
type Terminal = clichat.Terminal

// OnEventForMultiStep re-exports the clichat.OnEventForMultiStep function.
var OnEventForMultiStep = clichat.OnEventForMultiStep

// ContextDispatcherFor re-exports the clichat.ContextDispatcherFor function.
var ContextDispatcherFor = clichat.ContextDispatcherFor

// OrchestrationRepoForDispatcher re-exports the clichat.OrchestrationRepoForDispatcher function.
var OrchestrationRepoForDispatcher = clichat.OrchestrationRepoForDispatcher

// ContextStorePath re-exports the clichat.ContextStorePath function.
var ContextStorePath = clichat.ContextStorePath

// OpenContextStorePath re-exports the clichat.OpenContextStorePath function.
var OpenContextStorePath = clichat.OpenContextStorePath

// ConfigureChatWorkspace re-exports the clichat.ConfigureChatWorkspace function.
var ConfigureChatWorkspace = clichat.ConfigureChatWorkspace

// BuildModelBinding re-exports the clichat.BuildModelBinding function.
var BuildModelBinding = clichat.BuildModelBinding

// OpenContextStore re-exports the clichat.OpenContextStore function.
var OpenContextStore = clichat.OpenContextStore

// ApplyPrivacyPolicy re-exports the clichat.ApplyPrivacyPolicy function.
var ApplyPrivacyPolicy = clichat.ApplyPrivacyPolicy

// ApplyWorkflowStoreRoot re-exports the clichat.ApplyWorkflowStoreRoot function.
var ApplyWorkflowStoreRoot = clichat.ApplyWorkflowStoreRoot

// OpenWorkflowStore re-exports the clichat.OpenWorkflowStore function.
var OpenWorkflowStore = clichat.OpenWorkflowStore

// LoadChatSkills re-exports the clichat.LoadChatSkills function.
var LoadChatSkills = clichat.LoadChatSkills

// LoadAgentDefinitions re-exports the clichat.LoadAgentDefinitions function.
var LoadAgentDefinitions = clichat.LoadAgentDefinitions

// StepTimeout re-exports the clichat.StepTimeout helper.
var StepTimeout = clichat.StepTimeout
