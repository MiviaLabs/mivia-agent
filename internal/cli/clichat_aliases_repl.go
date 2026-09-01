package cli

// clichat_aliases.go re-exports symbols that moved to internal/clichat so
// staying consumers (internal/legacytui) compile without per-file import
// updates. Use the clichat-qualified form in new code. These aliases are
// intentional shims while the extraction stabilises.

import clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"

// AppendCtxSuffix re-exports the clichat.AppendCtxSuffix function.
var AppendCtxSuffix = clichat.AppendCtxSuffix

// BoundedToolText re-exports the clichat.BoundedToolText function.
var BoundedToolText = clichat.BoundedToolText

// BridgeDrain re-exports the clichat.BridgeDrain type.
type BridgeDrain = clichat.BridgeDrain

// ClassicAgentStatePtr re-exports the clichat.ClassicAgentStatePtr variable.
var ClassicAgentStatePtr = clichat.ClassicAgentStatePtr

// ClearSubagentProgress re-exports the clichat.ClearSubagentProgress function.
var ClearSubagentProgress = clichat.ClearSubagentProgress

// ClipPreviewLine re-exports the clichat.ClipPreviewLine function.
var ClipPreviewLine = clichat.ClipPreviewLine

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

// ForbiddenKeys re-exports the clichat.ForbiddenKeys variable.
var ForbiddenKeys = clichat.ForbiddenKeys

// HandleSlash re-exports the clichat.HandleSlash function.
var HandleSlash = clichat.HandleSlash

// HandleSlashAgent re-exports the clichat.HandleSlashAgent function.
var HandleSlashAgent = clichat.HandleSlashAgent

// HandleSlashEffort re-exports the clichat.HandleSlashEffort function.
var HandleSlashEffort = clichat.HandleSlashEffort

// HandleSlashInfo re-exports the clichat.HandleSlashInfo function.
var HandleSlashInfo = clichat.HandleSlashInfo

// HighlightCodeBlock re-exports the clichat.HighlightCodeBlock function.
var HighlightCodeBlock = clichat.HighlightCodeBlock

// IsBannerTool re-exports the clichat.IsBannerTool function.
var IsBannerTool = clichat.IsBannerTool

// IsEditTool re-exports the clichat.IsEditTool function.
var IsEditTool = clichat.IsEditTool

// IsLocalSlash re-exports the clichat.IsLocalSlash function.
var IsLocalSlash = clichat.IsLocalSlash

// JoinHub re-exports the clichat.JoinHub function.
var JoinHub = clichat.JoinHub

// KeyLabel re-exports the clichat.KeyLabel function.
var KeyLabel = clichat.KeyLabel

// KeyRegistry re-exports the clichat.KeyRegistry variable.
var KeyRegistry = clichat.KeyRegistry

// LatestAutoSaveName re-exports the clichat.LatestAutoSaveName function.
var LatestAutoSaveName = clichat.LatestAutoSaveName

// Max re-exports the clichat.Max function.
var Max = clichat.Max

// MaxThinkingLines re-exports the clichat.MaxThinkingLines constant.
const MaxThinkingLines = clichat.MaxThinkingLines

// MinCardWidth re-exports the clichat.MinCardWidth constant.
const MinCardWidth = clichat.MinCardWidth

// NewAgentTaskHandler re-exports the clichat.NewAgentTaskHandler function.
var NewAgentTaskHandler = clichat.NewAgentTaskHandler

// NewStreamBridge re-exports the clichat.NewStreamBridge function.
var NewStreamBridge = clichat.NewStreamBridge

// NewSubagentTracker re-exports the clichat.NewSubagentTracker function.
var NewSubagentTracker = clichat.NewSubagentTracker

// NewTerminal re-exports the clichat.NewTerminal function.
var NewTerminal = clichat.NewTerminal

// NewTestTerminal re-exports the clichat.NewTestTerminal function.
var NewTestTerminal = clichat.NewTestTerminal

// ParseEffortArg re-exports the clichat.ParseEffortArg function.
var ParseEffortArg = clichat.ParseEffortArg

// ParseToolPath re-exports the clichat.ParseToolPath function.
var ParseToolPath = clichat.ParseToolPath

// RealToolStarts re-exports the clichat.RealToolStarts function.
var RealToolStarts = clichat.RealToolStarts

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

// SubagentRun re-exports the clichat.SubagentRun type.
type SubagentRun = clichat.SubagentRun

// SummarizeToolDetail re-exports the clichat.SummarizeToolDetail function.
var SummarizeToolDetail = clichat.SummarizeToolDetail

// ToolResultFailed re-exports the clichat.ToolResultFailed function.
var ToolResultFailed = clichat.ToolResultFailed

// ToolWaveCounts re-exports the clichat.ToolWaveCounts function.
var ToolWaveCounts = clichat.ToolWaveCounts

// TruncatePreviewUTF8 re-exports the clichat.TruncatePreviewUTF8 function.
var TruncatePreviewUTF8 = clichat.TruncatePreviewUTF8

// ValidateKeyRegistry re-exports the clichat.ValidateKeyRegistry function.
var ValidateKeyRegistry = clichat.ValidateKeyRegistry

// ValidateWorkspaceRestart re-exports the clichat.ValidateWorkspaceRestart function.
var ValidateWorkspaceRestart = clichat.ValidateWorkspaceRestart

// VisualLineCount re-exports the clichat.VisualLineCount function.
var VisualLineCount = clichat.VisualLineCount

// EffortOrchestrationNotice re-exports the clichat.EffortOrchestrationNotice constant.
const EffortOrchestrationNotice = clichat.EffortOrchestrationNotice

// Rect re-exports the clichat.Rect type.
type Rect = clichat.Rect

// SafeEffortError re-exports the clichat.SafeEffortError function.
var SafeEffortError = clichat.SafeEffortError

// ToolIconForName re-exports the clichat.ToolIconForName function.
var ToolIconForName = clichat.ToolIconForName

// BridgeToolEvt re-exports the clichat.BridgeToolEvt type.
type BridgeToolEvt = clichat.BridgeToolEvt

// CurrentAgentName re-exports the clichat.CurrentAgentName function.
var CurrentAgentName = clichat.CurrentAgentName

// FocusScrollback re-exports the clichat.FocusScrollback constant.
const FocusScrollback = clichat.FocusScrollback

// Min re-exports the clichat.Min function.
var Min = clichat.Min

// TruncateToWidth re-exports the clichat.TruncateToWidth function.
var TruncateToWidth = clichat.TruncateToWidth

// FocusComposer re-exports the clichat.FocusComposer constant.
const FocusComposer = clichat.FocusComposer

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

// FocusSidebar re-exports the clichat.FocusSidebar constant.
const FocusSidebar = clichat.FocusSidebar

// FocusWorkflowsSidebar re-exports the clichat.FocusWorkflowsSidebar constant.
const FocusWorkflowsSidebar = clichat.FocusWorkflowsSidebar

// ShouldFollowOutput re-exports the clichat.ShouldFollowOutput function.
var ShouldFollowOutput = clichat.ShouldFollowOutput

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

// RouteFocusKey re-exports the clichat.RouteFocusKey function.
var RouteFocusKey = clichat.RouteFocusKey

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

// OverlayAt re-exports the clichat.OverlayAt function.
var OverlayAt = clichat.OverlayAt

// OpenRepositoryContextStore re-exports the clichat.OpenRepositoryContextStore function.
var OpenRepositoryContextStore = clichat.OpenRepositoryContextStore

// SlashCommand re-exports the clichat.SlashCommand type.
type SlashCommand = clichat.SlashCommand

// StreamBridge re-exports the clichat.StreamBridge type.
type StreamBridge = clichat.StreamBridge

// SubagentTracker re-exports the clichat.SubagentTracker type.
type SubagentTracker = clichat.SubagentTracker

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

// Binding re-exports the clichat.Binding type.
type Binding = clichat.Binding

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
