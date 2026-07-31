// Package cli — theme is the single semantic color/style source for terminal UI.
//
// Render files compose on these tokens; brand.go still owns the phase-color ramp
// (brandColorThinking, brandColorError, …). Inline content error (themeColorError
// "9") is deliberately distinct from brand/status error (brandColorError "160").
package cli

import "github.com/charmbracelet/lipgloss"

// Theme — the single semantic color/style source.
//
// 256-color indices (raw-string source of truth for P2.2 / P3 consumers):
const (
	themeColorDim          = "8"   // dim/structural text
	themeColorError        = "9"   // inline error red (tool ✗, slash errors) — not brandColorError
	themeColorInfo         = "14"  // cyan accent / info
	themeColorUser         = "12"  // user label blue
	themeColorWaitGray     = "243" // waiting-state mid-gray
	themeColorCardBg       = "236" // user-card / bar dark bg
	themeColorSelBg        = "237" // tool selection bg
	themeColorDiffAdd      = "10"
	themeColorDiffDel      = "9" // same index as themeColorError; distinct role name
	themeColorDiffAddBg    = "22"
	themeColorDiffDelBg    = "88"
	themeColorDiffHunk     = "5"  // magenta @@ hunk headers (SGR 35 / ansiMagenta)
	themeColorTime         = "11" // tool-time yellow
	themeColorOk           = "2"  // run-dashboard "running" / tool ok green
	themeColorStatusFailed = "9"
	themeColorStatusDone   = "8"
	themeThinkingDim       = "6"  // thinkingDimStyle
	themeColorBright       = "15" // bright white (name, selection fg)
)

// ANSI SGR codes — one vocabulary for markdown + highlight (hl* block deleted).
const (
	ansiBold      = "\033[1m"
	ansiBoldEnd   = "\033[22m"
	ansiItalic    = "\033[3m"
	ansiUnderline = "\033[4m"
	ansiDim       = "\033[2m"
	ansiDimEnd    = "\033[22m"
	ansiYellow    = "\033[33m"
	ansiCyan      = "\033[36m"
	ansiBlue      = "\033[34m"
	ansiGreen     = "\033[32m"
	ansiRed       = "\033[31m"
	ansiMagenta   = "\033[35m"
	ansiBgDark    = "\033[48;5;236m"
	ansiBgReset   = "\033[49m"
	ansiReset     = "\033[0m"
)

// Diff-highlight SGR (highlight only; not duplicated elsewhere).
const (
	ansiBgDiffDel = "\033[48;5;88m" // dark red background for deletions
	ansiBgDiffAdd = "\033[48;5;22m" // dark green background for additions
)

// Consolidated semantic styles (duplicates collapse onto these).
var (
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorDim))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorError))
	infoStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo))
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo)).Bold(true)
	waitStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorWaitGray))
	userLabel   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorUser)).Bold(true)
	userStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorUser))
)

// Backward-compat aliases — same value as consolidated styles.
// Collapse when call sites migrate off the legacy names (P1.1 follow-up).
var (
	tuiDimStyle     = dimStyle    // alias of dimStyle
	toolDimStyle    = dimStyle    // alias of dimStyle
	tuiErrorStyle   = errStyle    // alias of errStyle
	toolErrStyle    = errStyle    // alias of errStyle
	tuiInfoStyle    = infoStyle   // alias of infoStyle
	tuiAccentStyle  = accentStyle // alias of accentStyle
	tuiWaitingStyle = waitStyle   // alias of waitStyle
	tuiUserLabel    = userLabel   // alias of userLabel
	tuiUserStyle    = userStyle   // alias of userStyle
	userLabelStyle  = userLabel   // alias of userLabel
	userRailStyle   = userLabel   // alias of userLabel
)
