package cli

// clichat_aliases.go re-exports symbols that moved to internal/clichat so
// staying consumers (internal/legacytui) compile without per-file import
// updates. Use the clichat-qualified form in new code. These aliases are
// intentional shims while the extraction stabilises.

import clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"

// AnsiBgDiffAdd re-exports the clichat.AnsiBgDiffAdd constant.
const AnsiBgDiffAdd = clichat.AnsiBgDiffAdd

// AnsiBgDiffDel re-exports the clichat.AnsiBgDiffDel constant.
const AnsiBgDiffDel = clichat.AnsiBgDiffDel

// AnsiBoldEnd re-exports the clichat.AnsiBoldEnd constant.
const AnsiBoldEnd = clichat.AnsiBoldEnd

// AnsiDimEnd re-exports the clichat.AnsiDimEnd constant.
const AnsiDimEnd = clichat.AnsiDimEnd

// AppendCtxSuffix re-exports the clichat.AppendCtxSuffix function.
var AppendCtxSuffix = clichat.AppendCtxSuffix

// ApplyChatBlockEvent re-exports the clichat.ApplyChatBlockEvent function.
var ApplyChatBlockEvent = clichat.ApplyChatBlockEvent

// BindManagedWorktreeSessionExpected re-exports the clichat.BindManagedWorktreeSessionExpected function.
var BindManagedWorktreeSessionExpected = clichat.BindManagedWorktreeSessionExpected

// BoundedToolText re-exports the clichat.BoundedToolText function.
var BoundedToolText = clichat.BoundedToolText

// BrandColorThinking re-exports the clichat.BrandColorThinking constant.
const BrandColorThinking = clichat.BrandColorThinking

// BridgeDrain re-exports the clichat.BridgeDrain type.
type BridgeDrain = clichat.BridgeDrain

// ChatBlockEvent re-exports the clichat.ChatBlockEvent type.
type ChatBlockEvent = clichat.ChatBlockEvent

// ChatBlockID re-exports the clichat.ChatBlockID function.
var ChatBlockID = clichat.ChatBlockID

// ChatBlockRender re-exports the clichat.ChatBlockRender type.
type ChatBlockRender = clichat.ChatBlockRender

// ChatInvocation re-exports the clichat.ChatInvocation type.
type ChatInvocation = clichat.ChatInvocation

// ChatRenderer re-exports the clichat.ChatRenderer type.
type ChatRenderer = clichat.ChatRenderer

// ClampWorkGroupScroll re-exports the clichat.ClampWorkGroupScroll function.
var ClampWorkGroupScroll = clichat.ClampWorkGroupScroll

// ClassicAgentStatePtr re-exports the clichat.ClassicAgentStatePtr variable.
var ClassicAgentStatePtr = clichat.ClassicAgentStatePtr

// ClearSubagentProgress re-exports the clichat.ClearSubagentProgress function.
var ClearSubagentProgress = clichat.ClearSubagentProgress

// ClipPreviewLine re-exports the clichat.ClipPreviewLine function.
var ClipPreviewLine = clichat.ClipPreviewLine

// CollapseConversations re-exports the clichat.CollapseConversations function.
var CollapseConversations = clichat.CollapseConversations

// ColorDiffLine re-exports the clichat.ColorDiffLine function.
var ColorDiffLine = clichat.ColorDiffLine

// CompactStructuralOnlyNotice re-exports the clichat.CompactStructuralOnlyNotice function.
var CompactStructuralOnlyNotice = clichat.CompactStructuralOnlyNotice

// ContextWorkspaceID re-exports the clichat.ContextWorkspaceID function.
var ContextWorkspaceID = clichat.ContextWorkspaceID

// DialogRectFor re-exports the clichat.DialogRectFor function.
var DialogRectFor = clichat.DialogRectFor

// DisplaySessionName re-exports the clichat.DisplaySessionName function.
var DisplaySessionName = clichat.DisplaySessionName

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

// EnableSessionContext re-exports the clichat.EnableSessionContext function.
var EnableSessionContext = clichat.EnableSessionContext

// EventPreview re-exports the clichat.EventPreview function.
var EventPreview = clichat.EventPreview

// FilterSkillsForScope re-exports the clichat.FilterSkillsForScope function.
var FilterSkillsForScope = clichat.FilterSkillsForScope

// FindSlashCommand re-exports the clichat.FindSlashCommand function.
var FindSlashCommand = clichat.FindSlashCommand

// FitDialogRow re-exports the clichat.FitDialogRow function.
var FitDialogRow = clichat.FitDialogRow

// ForbiddenKeys re-exports the clichat.ForbiddenKeys variable.
var ForbiddenKeys = clichat.ForbiddenKeys

// FormatAgentCurrent re-exports the clichat.FormatAgentCurrent function.
var FormatAgentCurrent = clichat.FormatAgentCurrent

// FormatAgentSet re-exports the clichat.FormatAgentSet function.
var FormatAgentSet = clichat.FormatAgentSet

// FormatAgentUnavailable re-exports the clichat.FormatAgentUnavailable function.
var FormatAgentUnavailable = clichat.FormatAgentUnavailable

// FormatDuration re-exports the clichat.FormatDuration function.
var FormatDuration = clichat.FormatDuration

// FormatEffortSet re-exports the clichat.FormatEffortSet function.
var FormatEffortSet = clichat.FormatEffortSet

// FormatEffortStatus re-exports the clichat.FormatEffortStatus function.
var FormatEffortStatus = clichat.FormatEffortStatus

// FormatEffortSummary re-exports the clichat.FormatEffortSummary function.
var FormatEffortSummary = clichat.FormatEffortSummary

// FormatLiveToolWaveSummary re-exports the clichat.FormatLiveToolWaveSummary function.
var FormatLiveToolWaveSummary = clichat.FormatLiveToolWaveSummary

// FormatSessionAge re-exports the clichat.FormatSessionAge function.
var FormatSessionAge = clichat.FormatSessionAge

// FormatUserBubbleTime re-exports the clichat.FormatUserBubbleTime function.
var FormatUserBubbleTime = clichat.FormatUserBubbleTime

// FormatUserMessageCard re-exports the clichat.FormatUserMessageCard function.
var FormatUserMessageCard = clichat.FormatUserMessageCard

// GlyphCheck re-exports the clichat.GlyphCheck constant.
const GlyphCheck = clichat.GlyphCheck

// GlyphCross re-exports the clichat.GlyphCross constant.
const GlyphCross = clichat.GlyphCross

// GlyphDiamond re-exports the clichat.GlyphDiamond constant.
const GlyphDiamond = clichat.GlyphDiamond

// GlyphLozenge re-exports the clichat.GlyphLozenge constant.
const GlyphLozenge = clichat.GlyphLozenge

// GlyphTriR re-exports the clichat.GlyphTriR constant.
const GlyphTriR = clichat.GlyphTriR

// HandleSlash re-exports the clichat.HandleSlash function.
var HandleSlash = clichat.HandleSlash

// HandleSlashAgent re-exports the clichat.HandleSlashAgent function.
var HandleSlashAgent = clichat.HandleSlashAgent

// HandleSlashEffort re-exports the clichat.HandleSlashEffort function.
var HandleSlashEffort = clichat.HandleSlashEffort

// HandleSlashInfo re-exports the clichat.HandleSlashInfo function.
var HandleSlashInfo = clichat.HandleSlashInfo

// HandleSlashSessions re-exports the clichat.HandleSlashSessions function.
var HandleSlashSessions = clichat.HandleSlashSessions

// HighlightCodeBlock re-exports the clichat.HighlightCodeBlock function.
var HighlightCodeBlock = clichat.HighlightCodeBlock

// IsBannerTool re-exports the clichat.IsBannerTool function.
var IsBannerTool = clichat.IsBannerTool

// IsEditTool re-exports the clichat.IsEditTool function.
var IsEditTool = clichat.IsEditTool

// IsLifecycleStatus re-exports the clichat.IsLifecycleStatus function.
var IsLifecycleStatus = clichat.IsLifecycleStatus

// IsLocalSlash re-exports the clichat.IsLocalSlash function.
var IsLocalSlash = clichat.IsLocalSlash

// JoinHub re-exports the clichat.JoinHub function.
var JoinHub = clichat.JoinHub

// KeyLabel re-exports the clichat.KeyLabel function.
var KeyLabel = clichat.KeyLabel

// KeyRegistry re-exports the clichat.KeyRegistry variable.
var KeyRegistry = clichat.KeyRegistry

// KeyScope re-exports the clichat.KeyScope type.
type KeyScope = clichat.KeyScope

// LatestAutoSaveName re-exports the clichat.LatestAutoSaveName function.
var LatestAutoSaveName = clichat.LatestAutoSaveName

// LifecycleStatusFailed re-exports the clichat.LifecycleStatusFailed function.
var LifecycleStatusFailed = clichat.LifecycleStatusFailed

// LoadContextSessionResult re-exports the clichat.LoadContextSessionResult function.
var LoadContextSessionResult = clichat.LoadContextSessionResult

// MakeDialogLayout re-exports the clichat.MakeDialogLayout function.
var MakeDialogLayout = clichat.MakeDialogLayout

// Max re-exports the clichat.Max function.
var Max = clichat.Max

// MaxThinkingLines re-exports the clichat.MaxThinkingLines constant.
const MaxThinkingLines = clichat.MaxThinkingLines

// MinCardWidth re-exports the clichat.MinCardWidth constant.
const MinCardWidth = clichat.MinCardWidth

// NewAgentTaskHandler re-exports the clichat.NewAgentTaskHandler function.
var NewAgentTaskHandler = clichat.NewAgentTaskHandler

// NewChatInvocationRepositorySessionStorePath re-exports the clichat.NewChatInvocationRepositorySessionStorePath function.
var NewChatInvocationRepositorySessionStorePath = clichat.NewChatInvocationRepositorySessionStorePath

// NewChatInvocationWorkspacePath re-exports the clichat.NewChatInvocationWorkspacePath function.
var NewChatInvocationWorkspacePath = clichat.NewChatInvocationWorkspacePath

// NewSessionDispatcherMinimal re-exports the clichat.NewSessionDispatcherMinimal function.
var NewSessionDispatcherMinimal = clichat.NewSessionDispatcherMinimal

// NewStreamBridge re-exports the clichat.NewStreamBridge function.
var NewStreamBridge = clichat.NewStreamBridge

// NewSubagentTracker re-exports the clichat.NewSubagentTracker function.
var NewSubagentTracker = clichat.NewSubagentTracker

// NewTerminal re-exports the clichat.NewTerminal function.
var NewTerminal = clichat.NewTerminal

// NewTestTerminal re-exports the clichat.NewTestTerminal function.
var NewTestTerminal = clichat.NewTestTerminal

// NewToolRenderItem re-exports the clichat.NewToolRenderItem function.
var NewToolRenderItem = clichat.NewToolRenderItem

// ParseEffortArg re-exports the clichat.ParseEffortArg function.
var ParseEffortArg = clichat.ParseEffortArg

// ParseToolPath re-exports the clichat.ParseToolPath function.
var ParseToolPath = clichat.ParseToolPath

// ReadAutosaveStatus re-exports the clichat.ReadAutosaveStatus function.
var ReadAutosaveStatus = clichat.ReadAutosaveStatus

// RealToolStarts re-exports the clichat.RealToolStarts function.
var RealToolStarts = clichat.RealToolStarts

// RegistryForState re-exports the clichat.RegistryForState function.
var RegistryForState = clichat.RegistryForState

// RenderChatBlocksWithWorkGroupsWindow re-exports the clichat.RenderChatBlocksWithWorkGroupsWindow function.
var RenderChatBlocksWithWorkGroupsWindow = clichat.RenderChatBlocksWithWorkGroupsWindow

// RenderDialogFrame re-exports the clichat.RenderDialogFrame function.
var RenderDialogFrame = clichat.RenderDialogFrame

// RenderOneChatBlock re-exports the clichat.RenderOneChatBlock function.
var RenderOneChatBlock = clichat.RenderOneChatBlock

// RenderReplHelpInline re-exports the clichat.RenderReplHelpInline function.
var RenderReplHelpInline = clichat.RenderReplHelpInline

// RenderSkillSlashPrompt re-exports the clichat.RenderSkillSlashPrompt function.
var RenderSkillSlashPrompt = clichat.RenderSkillSlashPrompt

// RenderThinkingBlock re-exports the clichat.RenderThinkingBlock function.
var RenderThinkingBlock = clichat.RenderThinkingBlock

// ReplHelpContent re-exports the clichat.ReplHelpContent function.
var ReplHelpContent = clichat.ReplHelpContent

// RepositorySessionStorePath re-exports the clichat.RepositorySessionStorePath function.
var RepositorySessionStorePath = clichat.RepositorySessionStorePath

// RestoreREPLRuntime re-exports the clichat.RestoreREPLRuntime function.
var RestoreREPLRuntime = clichat.RestoreREPLRuntime

// ResultLooksLikeDiff re-exports the clichat.ResultLooksLikeDiff function.
var ResultLooksLikeDiff = clichat.ResultLooksLikeDiff

// SafeChatBlockText re-exports the clichat.SafeChatBlockText function.
var SafeChatBlockText = clichat.SafeChatBlockText

// ScopeComposer re-exports the clichat.ScopeComposer variable.
var ScopeComposer = clichat.ScopeComposer

// ScopeDashboard re-exports the clichat.ScopeDashboard variable.
var ScopeDashboard = clichat.ScopeDashboard

// ScopeGlobal re-exports the clichat.ScopeGlobal variable.
var ScopeGlobal = clichat.ScopeGlobal

// ScopeHistory re-exports the clichat.ScopeHistory variable.
var ScopeHistory = clichat.ScopeHistory

// ScopeOverlay re-exports the clichat.ScopeOverlay variable.
var ScopeOverlay = clichat.ScopeOverlay

// ScopeQueue re-exports the clichat.ScopeQueue variable.
var ScopeQueue = clichat.ScopeQueue

// ScopeScrollback re-exports the clichat.ScopeScrollback variable.
var ScopeScrollback = clichat.ScopeScrollback

// ScopeSessions re-exports the clichat.ScopeSessions variable.
var ScopeSessions = clichat.ScopeSessions

// ScopeSuggest re-exports the clichat.ScopeSuggest variable.
var ScopeSuggest = clichat.ScopeSuggest

// ScopeWelcome re-exports the clichat.ScopeWelcome variable.
var ScopeWelcome = clichat.ScopeWelcome

// ScopeWorkflows re-exports the clichat.ScopeWorkflows variable.
var ScopeWorkflows = clichat.ScopeWorkflows

// SessionEffortBusyRefusal re-exports the clichat.SessionEffortBusyRefusal constant.
const SessionEffortBusyRefusal = clichat.SessionEffortBusyRefusal

// SessionIdentity re-exports the clichat.SessionIdentity function.
var SessionIdentity = clichat.SessionIdentity

// SetGlobalBus re-exports the clichat.SetGlobalBus function.
var SetGlobalBus = clichat.SetGlobalBus

// SetSubagentProgress re-exports the clichat.SetSubagentProgress function.
var SetSubagentProgress = clichat.SetSubagentProgress

// SetupChatSessionContext re-exports the clichat.SetupChatSessionContext function.
var SetupChatSessionContext = clichat.SetupChatSessionContext

// SetupRepositorySessionContext re-exports the clichat.SetupRepositorySessionContext function.
var SetupRepositorySessionContext = clichat.SetupRepositorySessionContext

// SetupSessionContext re-exports the clichat.SetupSessionContext function.
var SetupSessionContext = clichat.SetupSessionContext

// ShortenModel re-exports the clichat.ShortenModel function.
var ShortenModel = clichat.ShortenModel

// ShortenWorkspacePath re-exports the clichat.ShortenWorkspacePath function.
var ShortenWorkspacePath = clichat.ShortenWorkspacePath

// SkillTurnPreamble re-exports the clichat.SkillTurnPreamble constant.
const SkillTurnPreamble = clichat.SkillTurnPreamble

// SlashKindBuiltin re-exports the clichat.SlashKindBuiltin constant.
const SlashKindBuiltin = clichat.SlashKindBuiltin

// SlashSurfacePlain re-exports the clichat.SlashSurfacePlain constant.
const SlashSurfacePlain = clichat.SlashSurfacePlain

// SlashSurfaceTUI re-exports the clichat.SlashSurfaceTUI constant.
const SlashSurfaceTUI = clichat.SlashSurfaceTUI

// StripANSI re-exports the clichat.StripANSI function.
var StripANSI = clichat.StripANSI

// SubagentRun re-exports the clichat.SubagentRun type.
type SubagentRun = clichat.SubagentRun

// SummarizeToolDetail re-exports the clichat.SummarizeToolDetail function.
var SummarizeToolDetail = clichat.SummarizeToolDetail

// TUIDimStyle re-exports the clichat.TUIDimStyle variable.
var TUIDimStyle = clichat.TUIDimStyle

// TUIHelpContentFor re-exports the clichat.TUIHelpContentFor function.
var TUIHelpContentFor = clichat.TUIHelpContentFor

// ThemeColorDiffAdd re-exports the clichat.ThemeColorDiffAdd constant.
const ThemeColorDiffAdd = clichat.ThemeColorDiffAdd

// ThemeColorDiffDel re-exports the clichat.ThemeColorDiffDel constant.
const ThemeColorDiffDel = clichat.ThemeColorDiffDel

// ThemeColorDim re-exports the clichat.ThemeColorDim constant.
const ThemeColorDim = clichat.ThemeColorDim

// ToolRenderItem re-exports the clichat.ToolRenderItem type.
type ToolRenderItem = clichat.ToolRenderItem

// ToolResultFailed re-exports the clichat.ToolResultFailed function.
var ToolResultFailed = clichat.ToolResultFailed

// ToolWaveCounts re-exports the clichat.ToolWaveCounts function.
var ToolWaveCounts = clichat.ToolWaveCounts

// TruncatePreviewUTF8 re-exports the clichat.TruncatePreviewUTF8 function.
var TruncatePreviewUTF8 = clichat.TruncatePreviewUTF8

// TuiHelpCommands re-exports the clichat.TuiHelpCommands function.
var TuiHelpCommands = clichat.TuiHelpCommands

// ValidateKeyRegistry re-exports the clichat.ValidateKeyRegistry function.
var ValidateKeyRegistry = clichat.ValidateKeyRegistry

// ValidateWorkspaceRestart re-exports the clichat.ValidateWorkspaceRestart function.
var ValidateWorkspaceRestart = clichat.ValidateWorkspaceRestart

// VisualLineCount re-exports the clichat.VisualLineCount function.
var VisualLineCount = clichat.VisualLineCount

// WorkGroup re-exports the clichat.WorkGroup type.
type WorkGroup = clichat.WorkGroup

// WorkGroupCollapsedDefault re-exports the clichat.WorkGroupCollapsedDefault function.
var WorkGroupCollapsedDefault = clichat.WorkGroupCollapsedDefault

// WorkGroupWindowRows re-exports the clichat.WorkGroupWindowRows constant.
const WorkGroupWindowRows = clichat.WorkGroupWindowRows

// WrapDisplayRows re-exports the clichat.WrapDisplayRows function.
var WrapDisplayRows = clichat.WrapDisplayRows

// WrapDisplayRowsWithSources re-exports the clichat.WrapDisplayRowsWithSources function.
var WrapDisplayRowsWithSources = clichat.WrapDisplayRowsWithSources

// WriteAutosaveStatus re-exports the clichat.WriteAutosaveStatus function.
var WriteAutosaveStatus = clichat.WriteAutosaveStatus

// AnsiBold re-exports the clichat.AnsiBold constant.
const AnsiBold = clichat.AnsiBold

// BrandWorkFrames re-exports the clichat.BrandWorkFrames variable.
var BrandWorkFrames = clichat.BrandWorkFrames

// EffortOrchestrationNotice re-exports the clichat.EffortOrchestrationNotice constant.
const EffortOrchestrationNotice = clichat.EffortOrchestrationNotice

// Rect re-exports the clichat.Rect type.
type Rect = clichat.Rect

// SafeEffortError re-exports the clichat.SafeEffortError function.
var SafeEffortError = clichat.SafeEffortError

// AnsiBlue re-exports the clichat.AnsiBlue constant.
const AnsiBlue = clichat.AnsiBlue

// AnsiCyan re-exports the clichat.AnsiCyan constant.
const AnsiCyan = clichat.AnsiCyan

// AnsiDim re-exports the clichat.AnsiDim constant.
const AnsiDim = clichat.AnsiDim

// AnsiGreen re-exports the clichat.AnsiGreen constant.
const AnsiGreen = clichat.AnsiGreen

// AnsiItalic re-exports the clichat.AnsiItalic constant.
const AnsiItalic = clichat.AnsiItalic

// AnsiMagenta re-exports the clichat.AnsiMagenta constant.
const AnsiMagenta = clichat.AnsiMagenta

// AnsiRed re-exports the clichat.AnsiRed constant.
const AnsiRed = clichat.AnsiRed

// AnsiYellow re-exports the clichat.AnsiYellow constant.
const AnsiYellow = clichat.AnsiYellow

// ToolIconForName re-exports the clichat.ToolIconForName function.
var ToolIconForName = clichat.ToolIconForName

// AnsiBgDark re-exports the clichat.AnsiBgDark constant.
const AnsiBgDark = clichat.AnsiBgDark

// AnsiReset re-exports the clichat.AnsiReset constant.
const AnsiReset = clichat.AnsiReset

// TUIErrorStyle re-exports the clichat.TUIErrorStyle variable.
var TUIErrorStyle = clichat.TUIErrorStyle

// ToolDimStyle re-exports the clichat.ToolDimStyle variable.
var ToolDimStyle = clichat.ToolDimStyle

// ToolErrStyle re-exports the clichat.ToolErrStyle variable.
var ToolErrStyle = clichat.ToolErrStyle

// ToolOkStyle re-exports the clichat.ToolOkStyle variable.
var ToolOkStyle = clichat.ToolOkStyle

// UserLabelStyle re-exports the clichat.UserLabelStyle variable.
var UserLabelStyle = clichat.UserLabelStyle

// UserRailStyle re-exports the clichat.UserRailStyle variable.
var UserRailStyle = clichat.UserRailStyle

// AgentBadgeStyle re-exports the clichat.AgentBadgeStyle variable.
var AgentBadgeStyle = clichat.AgentBadgeStyle

// BridgeToolEvt re-exports the clichat.BridgeToolEvt type.
type BridgeToolEvt = clichat.BridgeToolEvt

// TUIThinkingStyle re-exports the clichat.TUIThinkingStyle variable.
var TUIThinkingStyle = clichat.TUIThinkingStyle

// ToolNameStyle re-exports the clichat.ToolNameStyle variable.
var ToolNameStyle = clichat.ToolNameStyle

// ToolPathStyle re-exports the clichat.ToolPathStyle variable.
var ToolPathStyle = clichat.ToolPathStyle

// ToolTimeStyle re-exports the clichat.ToolTimeStyle variable.
var ToolTimeStyle = clichat.ToolTimeStyle

// ApplySessionAgent re-exports the clichat.ApplySessionAgent function.
var ApplySessionAgent = clichat.ApplySessionAgent

// ChatBlockSystem re-exports the clichat.ChatBlockSystem constant.
const ChatBlockSystem = clichat.ChatBlockSystem

// CurrentAgentName re-exports the clichat.CurrentAgentName function.
var CurrentAgentName = clichat.CurrentAgentName

// FocusScrollback re-exports the clichat.FocusScrollback constant.
const FocusScrollback = clichat.FocusScrollback

// Min re-exports the clichat.Min function.
var Min = clichat.Min

// TruncateToWidth re-exports the clichat.TruncateToWidth function.
var TruncateToWidth = clichat.TruncateToWidth

// ChatBlockThinking re-exports the clichat.ChatBlockThinking constant.
const ChatBlockThinking = clichat.ChatBlockThinking

// ChatBlockTool re-exports the clichat.ChatBlockTool constant.
const ChatBlockTool = clichat.ChatBlockTool

// FindWorkGroups re-exports the clichat.FindWorkGroups function.
var FindWorkGroups = clichat.FindWorkGroups

// FocusComposer re-exports the clichat.FocusComposer constant.
const FocusComposer = clichat.FocusComposer

// MaxHistorySize re-exports the clichat.MaxHistorySize constant.
const MaxHistorySize = clichat.MaxHistorySize

// RailView re-exports the clichat.RailView type.
type RailView = clichat.RailView

// SwitchModelCommand re-exports the clichat.SwitchModelCommand function.
var SwitchModelCommand = clichat.SwitchModelCommand

// ChatBlockDivider re-exports the clichat.ChatBlockDivider constant.
const ChatBlockDivider = clichat.ChatBlockDivider

// ChatBlockUser re-exports the clichat.ChatBlockUser constant.
const ChatBlockUser = clichat.ChatBlockUser

// IsWorkStatusBlock re-exports the clichat.IsWorkStatusBlock function.
var IsWorkStatusBlock = clichat.IsWorkStatusBlock

// RenderDiffBody re-exports the clichat.RenderDiffBody function.
var RenderDiffBody = clichat.RenderDiffBody

// RuneWidth re-exports the clichat.RuneWidth function.
var RuneWidth = clichat.RuneWidth

// SlashCommands re-exports the clichat.SlashCommands function.
var SlashCommands = clichat.SlashCommands

// SlashKindSkill re-exports the clichat.SlashKindSkill constant.
const SlashKindSkill = clichat.SlashKindSkill

// ChatBlockAssistant re-exports the clichat.ChatBlockAssistant constant.
const ChatBlockAssistant = clichat.ChatBlockAssistant

// FocusSidebar re-exports the clichat.FocusSidebar constant.
const FocusSidebar = clichat.FocusSidebar

// FocusWorkflowsSidebar re-exports the clichat.FocusWorkflowsSidebar constant.
const FocusWorkflowsSidebar = clichat.FocusWorkflowsSidebar

// HydrateChatBlocksForView re-exports the clichat.HydrateChatBlocksForView function.
var HydrateChatBlocksForView = clichat.HydrateChatBlocksForView

// RenderChatBlocksWithWorkGroups re-exports the clichat.RenderChatBlocksWithWorkGroups function.
var RenderChatBlocksWithWorkGroups = clichat.RenderChatBlocksWithWorkGroups

// ShouldFollowOutput re-exports the clichat.ShouldFollowOutput function.
var ShouldFollowOutput = clichat.ShouldFollowOutput

// SummaryDisabledReason re-exports the clichat.SummaryDisabledReason function.
var SummaryDisabledReason = clichat.SummaryDisabledReason

// ActionAgent re-exports the clichat.ActionAgent constant.
const ActionAgent = clichat.ActionAgent

// ActionKindForTool re-exports the clichat.ActionKindForTool function.
var ActionKindForTool = clichat.ActionKindForTool

// FormatModelUnavailable re-exports the clichat.FormatModelUnavailable function.
var FormatModelUnavailable = clichat.FormatModelUnavailable

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

// FormatBudgetInvalid re-exports the clichat.FormatBudgetInvalid function.
var FormatBudgetInvalid = clichat.FormatBudgetInvalid

// FormatBudgetSet re-exports the clichat.FormatBudgetSet function.
var FormatBudgetSet = clichat.FormatBudgetSet

// FormatBudgetSummary re-exports the clichat.FormatBudgetSummary function.
var FormatBudgetSummary = clichat.FormatBudgetSummary

// FormatModelSet re-exports the clichat.FormatModelSet function.
var FormatModelSet = clichat.FormatModelSet

// FormatStepsInvalid re-exports the clichat.FormatStepsInvalid function.
var FormatStepsInvalid = clichat.FormatStepsInvalid

// FormatStepsSet re-exports the clichat.FormatStepsSet function.
var FormatStepsSet = clichat.FormatStepsSet

// FormatStepsSummary re-exports the clichat.FormatStepsSummary function.
var FormatStepsSummary = clichat.FormatStepsSummary

// ParseNonNegInt re-exports the clichat.ParseNonNegInt function.
var ParseNonNegInt = clichat.ParseNonNegInt

// SaveSessionResult re-exports the clichat.SaveSessionResult function.
var SaveSessionResult = clichat.SaveSessionResult

// CancellationCanReplaceTurnError re-exports the clichat.CancellationCanReplaceTurnError function.
var CancellationCanReplaceTurnError = clichat.CancellationCanReplaceTurnError

// DeleteSessionResult re-exports the clichat.DeleteSessionResult function.
var DeleteSessionResult = clichat.DeleteSessionResult

// InjectSkillResourceTool re-exports the clichat.InjectSkillResourceTool function.
var InjectSkillResourceTool = clichat.InjectSkillResourceTool

// LoadSessionResult re-exports the clichat.LoadSessionResult function.
var LoadSessionResult = clichat.LoadSessionResult

// ModelRestoreNoticeText re-exports the clichat.ModelRestoreNoticeText function.
var ModelRestoreNoticeText = clichat.ModelRestoreNoticeText

// OverlayAt re-exports the clichat.OverlayAt function.
var OverlayAt = clichat.OverlayAt

// ToolBatchStatusDetail re-exports the clichat.ToolBatchStatusDetail function.
var ToolBatchStatusDetail = clichat.ToolBatchStatusDetail

// ToolStatusLine re-exports the clichat.ToolStatusLine function.
var ToolStatusLine = clichat.ToolStatusLine

// OpenRepositoryContextStore re-exports the clichat.OpenRepositoryContextStore function.
var OpenRepositoryContextStore = clichat.OpenRepositoryContextStore

// DialogLayout re-exports the clichat.DialogLayout type.
type DialogLayout = clichat.DialogLayout

// DialogPrefs re-exports the clichat.DialogPrefs type.
type DialogPrefs = clichat.DialogPrefs

// SlashCommand re-exports the clichat.SlashCommand type.
type SlashCommand = clichat.SlashCommand

// StreamBridge re-exports the clichat.StreamBridge type.
type StreamBridge = clichat.StreamBridge

// SubagentTracker re-exports the clichat.SubagentTracker type.
type SubagentTracker = clichat.SubagentTracker

// ToolRow re-exports the clichat.ToolRow type.
type ToolRow = clichat.ToolRow

// TuiFocus re-exports the clichat.TuiFocus type.
type TuiFocus = clichat.TuiFocus

// ChatBlock re-exports the clichat.ChatBlock type.
type ChatBlock = clichat.ChatBlock

// Terminal re-exports the clichat.Terminal type.
type Terminal = clichat.Terminal

// ChatBlockKind re-exports the clichat.ChatBlockKind type.
type ChatBlockKind = clichat.ChatBlockKind

// SkillScopeFromAgent re-exports the clichat.SkillScopeFromAgent function.
var SkillScopeFromAgent = clichat.SkillScopeFromAgent

// OnEventForMultiStep re-exports the clichat.OnEventForMultiStep function.
var OnEventForMultiStep = clichat.OnEventForMultiStep

// NewSessionDispatcher re-exports the clichat.NewSessionDispatcher function.
var NewSessionDispatcher = clichat.NewSessionDispatcher

// RenderChatBlocks re-exports the clichat.RenderChatBlocks function.
var RenderChatBlocks = clichat.RenderChatBlocks

// AssistantBubble re-exports the clichat.AssistantBubble variable.
var AssistantBubble = clichat.AssistantBubble

// UserBubble re-exports the clichat.UserBubble variable.
var UserBubble = clichat.UserBubble

// HydrateChatBlocks re-exports the clichat.HydrateChatBlocks function.
var HydrateChatBlocks = clichat.HydrateChatBlocks

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

// RenderMarkdown re-exports the clichat.RenderMarkdown function.
var RenderMarkdown = clichat.RenderMarkdown

// ConfigureChatWorkspace re-exports the clichat.ConfigureChatWorkspace function.
var ConfigureChatWorkspace = clichat.ConfigureChatWorkspace

// BuildModelBinding re-exports the clichat.BuildModelBinding function.
var BuildModelBinding = clichat.BuildModelBinding

// AttachSessionDispatcher re-exports the clichat.AttachSessionDispatcher function.
var AttachSessionDispatcher = clichat.AttachSessionDispatcher

// SessionRouting re-exports the clichat.SessionRouting type.
type SessionRouting = clichat.SessionRouting

// LoadSessionSkills re-exports the clichat.LoadSessionSkills function.
var LoadSessionSkills = clichat.LoadSessionSkills

// NewMarkdownWriter re-exports the clichat.NewMarkdownWriter function.
var NewMarkdownWriter = clichat.NewMarkdownWriter

// WrapANSIv2 re-exports the clichat.WrapANSIv2 function.
var WrapANSIv2 = clichat.WrapANSIv2

// NewChatRenderer re-exports the clichat.NewChatRenderer function.
var NewChatRenderer = clichat.NewChatRenderer

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
