package cli

// clichat_aliases.go re-exports symbols that moved to internal/clichat so
// staying consumers (internal/legacytui) compile without per-file import
// updates. Use the clichat-qualified form in new code. These aliases are
// intentional shims while the extraction stabilises.

import clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"

// AnsiBoldEnd re-exports the clichat.AnsiBoldEnd constant.
const AnsiBoldEnd = clichat.AnsiBoldEnd

// AnsiDimEnd re-exports the clichat.AnsiDimEnd constant.
const AnsiDimEnd = clichat.AnsiDimEnd

// DialogRectFor re-exports the clichat.DialogRectFor function.
var DialogRectFor = clichat.DialogRectFor

// FitDialogRow re-exports the clichat.FitDialogRow function.
var FitDialogRow = clichat.FitDialogRow

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

// MakeDialogLayout re-exports the clichat.MakeDialogLayout function.
var MakeDialogLayout = clichat.MakeDialogLayout

// StripANSI re-exports the clichat.StripANSI function.
var StripANSI = clichat.StripANSI

// TUIDimStyle re-exports the clichat.TUIDimStyle variable.
var TUIDimStyle = clichat.TUIDimStyle

// TUIHelpContentFor re-exports the clichat.TUIHelpContentFor function.
var TUIHelpContentFor = clichat.TUIHelpContentFor

// ThemeColorDim re-exports the clichat.ThemeColorDim constant.
const ThemeColorDim = clichat.ThemeColorDim

// TuiHelpCommands re-exports the clichat.TuiHelpCommands function.
var TuiHelpCommands = clichat.TuiHelpCommands

// AnsiBold re-exports the clichat.AnsiBold constant.
const AnsiBold = clichat.AnsiBold

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

// AgentBadgeStyle re-exports the clichat.AgentBadgeStyle variable.
var AgentBadgeStyle = clichat.AgentBadgeStyle

// TUIThinkingStyle re-exports the clichat.TUIThinkingStyle variable.
var TUIThinkingStyle = clichat.TUIThinkingStyle

// ToolNameStyle re-exports the clichat.ToolNameStyle variable.
var ToolNameStyle = clichat.ToolNameStyle

// ToolPathStyle re-exports the clichat.ToolPathStyle variable.
var ToolPathStyle = clichat.ToolPathStyle

// ToolTimeStyle re-exports the clichat.ToolTimeStyle variable.
var ToolTimeStyle = clichat.ToolTimeStyle

// DialogLayout re-exports the clichat.DialogLayout type.
type DialogLayout = clichat.DialogLayout

// DialogPrefs re-exports the clichat.DialogPrefs type.
type DialogPrefs = clichat.DialogPrefs

// TuiFocus re-exports the clichat.TuiFocus type.
type TuiFocus = clichat.TuiFocus
