// Package cli - theme is the single semantic color/style source for terminal UI.
//
// Render files compose on these tokens; brand.go still owns the phase-color ramp
// (BrandColorThinking, brandColorError, …). Inline content error (themeColorError
// "9") is deliberately distinct from brand/status error (brandColorError "160").
package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/charmbracelet/lipgloss"
)

// Theme - the single semantic color/style source.
//
// 256-color indices (raw-string source of truth for P2.2 / P3 consumers):
//
// ThemeColorDim, ThemeColorDiffAdd, ThemeColorDiffDel are relocated to
// internal/cli (self-contained literals shared with the classic-mode
// renderer); aliased here so this package's own call sites are unchanged.
const (
	ThemeColorDim          = cli.ThemeColorDim
	themeColorError        = "9"   // inline error red (tool ✗, slash errors) - not brandColorError
	themeColorInfo         = "14"  // cyan accent / info
	themeColorUser         = "12"  // user label blue
	themeColorWaitGray     = "243" // waiting-state mid-gray
	themeColorCardBg       = "236" // user-card / bar dark bg
	themeColorSelBg        = "237" // tool selection bg
	ThemeColorDiffAdd      = cli.ThemeColorDiffAdd
	ThemeColorDiffDel      = cli.ThemeColorDiffDel // same index as themeColorError; distinct role name
	themeColorDiffAddBg    = "22"
	themeColorDiffDelBg    = "88"
	themeColorDiffHunk     = "5"  // magenta @@ hunk headers (SGR 35 / AnsiMagenta)
	themeColorTime         = "11" // tool-time yellow
	themeColorOk           = "2"  // run-dashboard "running" / tool ok green
	themeColorStatusFailed = "9"
	themeColorStatusDone   = "8"
	themeThinkingDim       = "6"  // thinkingDimStyle
	themeColorBright       = "15" // bright white (name, selection fg)
)

// ANSI SGR codes - one vocabulary for markdown + highlight (hl* block
// deleted). Relocated to internal/cli (self-contained literals shared with
// the classic-mode markdown/highlight renderers); aliased here so this
// package's own call sites are unchanged.
const (
	AnsiBold    = cli.AnsiBold
	AnsiBoldEnd = cli.AnsiBoldEnd
	AnsiItalic  = cli.AnsiItalic
	AnsiDim     = cli.AnsiDim
	AnsiDimEnd  = cli.AnsiDimEnd
	AnsiYellow  = cli.AnsiYellow
	AnsiCyan    = cli.AnsiCyan
	AnsiBlue    = cli.AnsiBlue
	AnsiGreen   = cli.AnsiGreen
	AnsiRed     = cli.AnsiRed
	AnsiMagenta = cli.AnsiMagenta
	AnsiBgDark  = cli.AnsiBgDark
	ansiBgReset = "\033[49m"
	AnsiReset   = cli.AnsiReset
)

// ansiBgDiffDel / ansiBgDiffAdd (diff-highlight SGR) relocated to
// diff_style.go: that file was their sole caller. ansiUnderline relocated
// to markdown.go for the same reason.

// Consolidated semantic styles (duplicates collapse onto these).
var (
	dimStyle    = cli.TUIDimStyle   // relocated to internal/cli; aliased here
	errStyle    = cli.TUIErrorStyle // relocated to internal/cli; aliased here
	infoStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo))
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo)).Bold(true)
	waitStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorWaitGray))
	userLabel   = cli.UserLabelStyle // relocated to internal/cli; aliased here
	userStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorUser))
)

// Backward-compat aliases - same value as consolidated styles.
// Collapse when call sites migrate off the legacy names (P1.1 follow-up).
//
// TUIDimStyle, ToolDimStyle, TUIErrorStyle, ToolErrStyle, UserLabelStyle,
// UserRailStyle are relocated to internal/cli (needed there by the
// classic-mode block renderer); aliased here so this package's own call
// sites are unchanged.
var (
	TUIDimStyle     = cli.TUIDimStyle
	ToolDimStyle    = cli.ToolDimStyle
	TUIErrorStyle   = cli.TUIErrorStyle
	ToolErrStyle    = cli.ToolErrStyle
	tuiInfoStyle    = infoStyle   // alias of infoStyle
	tuiAccentStyle  = accentStyle // alias of accentStyle
	tuiWaitingStyle = waitStyle   // alias of waitStyle
	tuiUserLabel    = userLabel   // alias of userLabel
	tuiUserStyle    = userStyle   // alias of userStyle
	UserLabelStyle  = cli.UserLabelStyle
	UserRailStyle   = cli.UserRailStyle
)
