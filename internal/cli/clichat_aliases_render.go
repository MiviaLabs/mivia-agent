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

// ApplyChatBlockEvent re-exports the clichat.ApplyChatBlockEvent function.
var ApplyChatBlockEvent = clichat.ApplyChatBlockEvent

// BrandColorThinking re-exports the clichat.BrandColorThinking constant.
const BrandColorThinking = clichat.BrandColorThinking

// ChatBlockEvent re-exports the clichat.ChatBlockEvent type.
type ChatBlockEvent = clichat.ChatBlockEvent

// ChatBlockID re-exports the clichat.ChatBlockID function.
var ChatBlockID = clichat.ChatBlockID

// ChatBlockRender re-exports the clichat.ChatBlockRender type.
type ChatBlockRender = clichat.ChatBlockRender

// ChatRenderer re-exports the clichat.ChatRenderer type.
type ChatRenderer = clichat.ChatRenderer

// ClampWorkGroupScroll re-exports the clichat.ClampWorkGroupScroll function.
var ClampWorkGroupScroll = clichat.ClampWorkGroupScroll

// CollapseConversations re-exports the clichat.CollapseConversations function.
var CollapseConversations = clichat.CollapseConversations

// ColorDiffLine re-exports the clichat.ColorDiffLine function.
var ColorDiffLine = clichat.ColorDiffLine

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

// IsLifecycleStatus re-exports the clichat.IsLifecycleStatus function.
var IsLifecycleStatus = clichat.IsLifecycleStatus

// LifecycleStatusFailed re-exports the clichat.LifecycleStatusFailed function.
var LifecycleStatusFailed = clichat.LifecycleStatusFailed

// NewToolRenderItem re-exports the clichat.NewToolRenderItem function.
var NewToolRenderItem = clichat.NewToolRenderItem

// ReadAutosaveStatus re-exports the clichat.ReadAutosaveStatus function.
var ReadAutosaveStatus = clichat.ReadAutosaveStatus

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

// ResultLooksLikeDiff re-exports the clichat.ResultLooksLikeDiff function.
var ResultLooksLikeDiff = clichat.ResultLooksLikeDiff

// SafeChatBlockText re-exports the clichat.SafeChatBlockText function.
var SafeChatBlockText = clichat.SafeChatBlockText

// ShortenModel re-exports the clichat.ShortenModel function.
var ShortenModel = clichat.ShortenModel

// ShortenWorkspacePath re-exports the clichat.ShortenWorkspacePath function.
var ShortenWorkspacePath = clichat.ShortenWorkspacePath

// ThemeColorDiffAdd re-exports the clichat.ThemeColorDiffAdd constant.
const ThemeColorDiffAdd = clichat.ThemeColorDiffAdd

// ThemeColorDiffDel re-exports the clichat.ThemeColorDiffDel constant.
const ThemeColorDiffDel = clichat.ThemeColorDiffDel

// ToolRenderItem re-exports the clichat.ToolRenderItem type.
type ToolRenderItem = clichat.ToolRenderItem

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

// BrandWorkFrames re-exports the clichat.BrandWorkFrames variable.
var BrandWorkFrames = clichat.BrandWorkFrames

// UserRailStyle re-exports the clichat.UserRailStyle variable.
var UserRailStyle = clichat.UserRailStyle

// ChatBlockSystem re-exports the clichat.ChatBlockSystem constant.
const ChatBlockSystem = clichat.ChatBlockSystem

// ChatBlockThinking re-exports the clichat.ChatBlockThinking constant.
const ChatBlockThinking = clichat.ChatBlockThinking

// ChatBlockTool re-exports the clichat.ChatBlockTool constant.
const ChatBlockTool = clichat.ChatBlockTool

// FindWorkGroups re-exports the clichat.FindWorkGroups function.
var FindWorkGroups = clichat.FindWorkGroups

// RailView re-exports the clichat.RailView type.
type RailView = clichat.RailView

// ChatBlockDivider re-exports the clichat.ChatBlockDivider constant.
const ChatBlockDivider = clichat.ChatBlockDivider

// ChatBlockUser re-exports the clichat.ChatBlockUser constant.
const ChatBlockUser = clichat.ChatBlockUser

// IsWorkStatusBlock re-exports the clichat.IsWorkStatusBlock function.
var IsWorkStatusBlock = clichat.IsWorkStatusBlock

// RenderDiffBody re-exports the clichat.RenderDiffBody function.
var RenderDiffBody = clichat.RenderDiffBody

// ChatBlockAssistant re-exports the clichat.ChatBlockAssistant constant.
const ChatBlockAssistant = clichat.ChatBlockAssistant

// HydrateChatBlocksForView re-exports the clichat.HydrateChatBlocksForView function.
var HydrateChatBlocksForView = clichat.HydrateChatBlocksForView

// RenderChatBlocksWithWorkGroups re-exports the clichat.RenderChatBlocksWithWorkGroups function.
var RenderChatBlocksWithWorkGroups = clichat.RenderChatBlocksWithWorkGroups

// FormatModelUnavailable re-exports the clichat.FormatModelUnavailable function.
var FormatModelUnavailable = clichat.FormatModelUnavailable

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

// ToolBatchStatusDetail re-exports the clichat.ToolBatchStatusDetail function.
var ToolBatchStatusDetail = clichat.ToolBatchStatusDetail

// ToolStatusLine re-exports the clichat.ToolStatusLine function.
var ToolStatusLine = clichat.ToolStatusLine

// ChatBlock re-exports the clichat.ChatBlock type.
type ChatBlock = clichat.ChatBlock

// ChatBlockKind re-exports the clichat.ChatBlockKind type.
type ChatBlockKind = clichat.ChatBlockKind

// RenderChatBlocks re-exports the clichat.RenderChatBlocks function.
var RenderChatBlocks = clichat.RenderChatBlocks

// AssistantBubble re-exports the clichat.AssistantBubble variable.
var AssistantBubble = clichat.AssistantBubble

// UserBubble re-exports the clichat.UserBubble variable.
var UserBubble = clichat.UserBubble

// HydrateChatBlocks re-exports the clichat.HydrateChatBlocks function.
var HydrateChatBlocks = clichat.HydrateChatBlocks

// RenderMarkdown re-exports the clichat.RenderMarkdown function.
var RenderMarkdown = clichat.RenderMarkdown

// NewMarkdownWriter re-exports the clichat.NewMarkdownWriter function.
var NewMarkdownWriter = clichat.NewMarkdownWriter

// WrapANSIv2 re-exports the clichat.WrapANSIv2 function.
var WrapANSIv2 = clichat.WrapANSIv2

// NewChatRenderer re-exports the clichat.NewChatRenderer function.
var NewChatRenderer = clichat.NewChatRenderer
