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

// ChatRenderer re-exports the clichat.ChatRenderer type.
type ChatRenderer = clichat.ChatRenderer

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

// FormatUserMessageCard re-exports the clichat.FormatUserMessageCard function.
var FormatUserMessageCard = clichat.FormatUserMessageCard

// IsLifecycleStatus re-exports the clichat.IsLifecycleStatus function.
var IsLifecycleStatus = clichat.IsLifecycleStatus

// LifecycleStatusFailed re-exports the clichat.LifecycleStatusFailed function.
var LifecycleStatusFailed = clichat.LifecycleStatusFailed

// NewToolRenderItem re-exports the clichat.NewToolRenderItem function.
var NewToolRenderItem = clichat.NewToolRenderItem

// RenderReplHelpInline re-exports the clichat.RenderReplHelpInline function.
var RenderReplHelpInline = clichat.RenderReplHelpInline

// RenderSkillSlashPrompt re-exports the clichat.RenderSkillSlashPrompt function.
var RenderSkillSlashPrompt = clichat.RenderSkillSlashPrompt

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

// BrandWorkFrames re-exports the clichat.BrandWorkFrames variable.
var BrandWorkFrames = clichat.BrandWorkFrames

// ChatBlockSystem re-exports the clichat.ChatBlockSystem constant.
const ChatBlockSystem = clichat.ChatBlockSystem

// ChatBlockThinking re-exports the clichat.ChatBlockThinking constant.
const ChatBlockThinking = clichat.ChatBlockThinking

// ChatBlockTool re-exports the clichat.ChatBlockTool constant.
const ChatBlockTool = clichat.ChatBlockTool

// ChatBlockDivider re-exports the clichat.ChatBlockDivider constant.
const ChatBlockDivider = clichat.ChatBlockDivider

// ChatBlockUser re-exports the clichat.ChatBlockUser constant.
const ChatBlockUser = clichat.ChatBlockUser

// RenderDiffBody re-exports the clichat.RenderDiffBody function.
var RenderDiffBody = clichat.RenderDiffBody

// ChatBlockAssistant re-exports the clichat.ChatBlockAssistant constant.
const ChatBlockAssistant = clichat.ChatBlockAssistant

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
