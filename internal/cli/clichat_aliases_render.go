package cli

// clichat_aliases.go re-exports symbols that moved to internal/cli/chat so
// staying consumers (internal/legacytui) compile without per-file import
// updates. Use the clichat-qualified form in new code. These aliases are
// intentional shims while the extraction stabilises.

import clichat "github.com/MiviaLabs/mivia-agent/internal/cli/chat"

// AnsiBgDiffAdd re-exports the clichat.AnsiBgDiffAdd constant.
const AnsiBgDiffAdd = clichat.AnsiBgDiffAdd

// AnsiBgDiffDel re-exports the clichat.AnsiBgDiffDel constant.
const AnsiBgDiffDel = clichat.AnsiBgDiffDel

// BrandColorThinking re-exports the clichat.BrandColorThinking constant.
const BrandColorThinking = clichat.BrandColorThinking

// ChatRenderer re-exports the clichat.ChatRenderer type.
type ChatRenderer = clichat.ChatRenderer

// CollapseConversations re-exports the clichat.CollapseConversations function.
var CollapseConversations = clichat.CollapseConversations

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

// ShortenModel re-exports the clichat.ShortenModel function.
var ShortenModel = clichat.ShortenModel

// ShortenWorkspacePath re-exports the clichat.ShortenWorkspacePath function.
var ShortenWorkspacePath = clichat.ShortenWorkspacePath

// ThemeColorDiffAdd re-exports the clichat.ThemeColorDiffAdd constant.
const ThemeColorDiffAdd = clichat.ThemeColorDiffAdd

// ToolRenderItem re-exports the clichat.ToolRenderItem type.
type ToolRenderItem = clichat.ToolRenderItem

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

// ToolStatusLine re-exports the clichat.ToolStatusLine function.
var ToolStatusLine = clichat.ToolStatusLine

// RenderMarkdown re-exports the clichat.RenderMarkdown function.
var RenderMarkdown = clichat.RenderMarkdown

// NewMarkdownWriter re-exports the clichat.NewMarkdownWriter function.
var NewMarkdownWriter = clichat.NewMarkdownWriter

// WrapANSIv2 re-exports the clichat.WrapANSIv2 function.
var WrapANSIv2 = clichat.WrapANSIv2

// NewChatRenderer re-exports the clichat.NewChatRenderer function.
var NewChatRenderer = clichat.NewChatRenderer
